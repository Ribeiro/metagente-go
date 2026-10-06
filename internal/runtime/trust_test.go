package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/trust"
)

const missingServer = "/nonexistent/weather-server"

// trustedRT is a runtime whose approvals live in a private folder of the test,
// outside the project.
func trustedRT(t *testing.T) *Runtime {
	t.Helper()
	rt := newRT(t.TempDir(), nil)
	rt.Trust = trust.NewRegistry(filepath.Join(t.TempDir(), "config"))
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// serverSource is an agent that writes a file when it starts and then uses a
// tool server that is not installed, so no real program is ever started.
func serverSource(command string) string {
	return "agent A\n  goal \"x\"\n  tool file\n  tool weather from mcp \"" + command + "\"\n" +
		"  accepts go\n  on start\n    file.write path: \"started.txt\" text: \"ran\"\n" +
		"  on go\n    r = weather.forecast city: \"x\"\n    reply r\n"
}

func ranOnStart(rt *Runtime) bool {
	_, err := os.Stat(filepath.Join(rt.Config.Root, "started.txt"))
	return err == nil
}

// req: T1
func TestAnUnapprovedToolServerIsRefusedBeforeAnythingRuns(t *testing.T) {
	rt := trustedRT(t)
	_, err := runSource(t, rt, serverSource(missingServer), "go")
	mustContain(t, errText(t, err),
		"have not approved for this project",
		"starts the program: "+missingServer,
		"metagente trust")
	if ranOnStart(rt) {
		t.Error("`on start` ran before the person approved anything")
	}
}

// req: T1
func TestApprovingLetsTheRunGoOnAndTheApprovalStays(t *testing.T) {
	rt := trustedRT(t)
	file := filepath.Join(rt.Config.Root, "agent.ag")
	if err := os.WriteFile(file, []byte(serverSource(missingServer)), 0o644); err != nil {
		t.Fatal(err)
	}

	var asked []trust.Item
	opts := Options{File: file, Message: "go", Confirm: func(missing []trust.Item) bool {
		asked = missing
		return true
	}}
	_, err := RunFile(t.Context(), rt, opts)
	// The gate was passed: what fails now is the program that is not installed.
	mustContain(t, errText(t, err), "I could not start the tool server `"+missingServer+"`")
	if len(asked) != 1 || asked[0].Target != missingServer {
		t.Errorf("the person was asked about %v", asked)
	}

	// The next run needs no question.
	opts.Confirm = func([]trust.Item) bool {
		t.Error("the person was asked again about something already approved")
		return false
	}
	_, err = RunFile(t.Context(), rt, opts)
	mustContain(t, errText(t, err), "I could not start the tool server")
}

func TestDecliningKeepsTheRunFromStarting(t *testing.T) {
	rt := trustedRT(t)
	file := filepath.Join(rt.Config.Root, "agent.ag")
	if err := os.WriteFile(file, []byte(serverSource(missingServer)), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RunFile(t.Context(), rt, Options{File: file, Message: "go", Confirm: func([]trust.Item) bool { return false }})
	mustContain(t, errText(t, err), "have not approved")
	if ranOnStart(rt) {
		t.Error("`on start` ran although the person said no")
	}
	missing, _ := rt.Trust.Missing(rt.Config.Root, []trust.Item{trust.CommandItem(missingServer, nil)})
	if len(missing) != 1 {
		t.Error("declining must not approve anything")
	}
}

func TestTheApprovalOfOneProgramDoesNotCoverAnother(t *testing.T) {
	rt := trustedRT(t)
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.CommandItem(missingServer, nil)}); err != nil {
		t.Fatal(err)
	}
	_, err := runSource(t, rt, serverSource(missingServer+" --extra"), "go")
	mustContain(t, errText(t, err), "have not approved", missingServer+" --extra")
}

// req: T1
func TestWhatALinkedAgentStartsNeedsApprovalToo(t *testing.T) {
	rt := trustedRT(t)
	dir := rt.Config.Root
	put(t, dir, "helper.ag", "agent Helper\n  goal \"h\"\n  tool weather from mcp \""+missingServer+"\"\n"+
		"  accepts hi\n  on hi\n    r = weather.forecast city: \"x\"\n    reply r\n")
	boss := put(t, dir, "boss.ag", bossAgent)

	_, err := runFile(t, rt, boss, "go")
	mustContain(t, errText(t, err), "have not approved", "starts the program: "+missingServer)

	// Approving what the boss needs covers the helper it links to.
	needs := rt.Needs(mustLoad(t, boss))
	if len(needs) != 1 || needs[0].Target != missingServer {
		t.Fatalf("needs = %v", needs)
	}
	if err := rt.Trust.Approve(dir, needs); err != nil {
		t.Fatal(err)
	}
	_, err = runFile(t, rt, boss, "go")
	mustContain(t, errText(t, err), "I could not start the tool server") // the gate is behind us
}

func mustLoad(t *testing.T, path string) []*lang.AgentDef {
	t.Helper()
	agents, err := LoadAgents(path)
	if err != nil {
		t.Fatal(errText(t, err))
	}
	return agents
}

// A server can show up after the run began, for example in a linked agent that
// was edited. The check at the moment of reaching it catches that.
func TestAServerNobodyApprovedIsRefusedWhenItIsReached(t *testing.T) {
	rt := trustedRT(t)
	agent := agentFrom(t, rt, serverSource(missingServer), "c")
	_, err := ask(agent, "go")
	mustContain(t, errText(t, err), "the tool server `"+missingServer+"` has not been approved for this project", "metagente trust")
}

func TestAFileWithoutServersOrAddressesNeedsNoApproval(t *testing.T) {
	rt := trustedRT(t)
	got, err := runSource(t, rt, "agent A\n  goal \"x\"\n  accepts go\n  on go\n    reply \"fine\"\n", "go")
	if err != nil || got.Text != "fine" {
		t.Fatalf("got %v %v", got.Display(), err)
	}
	if _, statErr := os.Stat(rt.Trust.Path()); statErr == nil {
		t.Error("the file of approvals was created for nothing")
	}
}

// req: T5
func TestALinkThatLeavesTheProjectIsWarnedAbout(t *testing.T) {
	base := t.TempDir()
	project := filepath.Join(base, "project")
	boss := put(t, project, "boss.ag", "agent Boss\n  goal \"b\"\n  link Helper from \"../other/helper.ag\"\n  link Near from \"sub/near.ag\"\n  accepts go\n  on go\n    reply \"x\"\n")
	agents := mustLoad(t, boss)

	warnings := LinkWarnings(project, agents)
	if len(warnings) != 1 {
		t.Fatalf("got %d warnings, want 1", len(warnings))
	}
	text := warnings[0].Render()
	mustContain(t, text, "Warning on line 3 of boss.ag", "the link to `Helper` points outside this project", "../other/helper.ag")
	if strings.Contains(text, "Near") {
		t.Error("a link inside the project was warned about")
	}
}
