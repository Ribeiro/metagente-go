package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"metagente/internal/value"
)

// put writes a file, creating its folders, and returns its path.
func put(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runFile(t *testing.T, rt *Runtime, path, message string, params ...string) (value.Value, error) {
	t.Helper()
	return RunFile(context.Background(), rt, Options{File: path, Message: message, Params: params})
}

func helperAgent(word string) string {
	return "agent Helper\n  goal \"h\"\n  accepts hi\n  on hi\n    reply \"" + word + "\"\n"
}

const bossAgent = "agent Boss\n  goal \"b\"\n  link Helper\n  accepts go\n  on go\n    reply Helper.hi\n"

// ---------- resolution and changes (from dynamic_link) ----------

func TestLinkResolutionOrderIsSameFileThenNextToThenAgentsFolderThenQuotedPath(t *testing.T) {
	dir := t.TempDir()
	rt := newRT(dir, nil)
	boss := put(t, dir, "boss.ag", bossAgent)

	// 1. the agents folder, when nothing else has the agent
	put(t, dir, "agents/Helper.ag", helperAgent("from agents folder"))
	if got, err := runFile(t, rt, boss, "go"); err != nil || got.Text != "from agents folder" {
		t.Fatalf("agents folder: %v %v", got.Display(), err)
	}

	// 2. next to the calling file wins over the agents folder (a lower case file name works too)
	put(t, dir, "helper.ag", helperAgent("from next to"))
	if got, err := runFile(t, rt, boss, "go"); err != nil || got.Text != "from next to" {
		t.Fatalf("next to the caller: %v %v", got.Display(), err)
	}

	// 3. the same file wins over everything
	put(t, dir, "boss.ag", bossAgent+helperAgent("from same file"))
	if got, err := runFile(t, rt, boss, "go"); err != nil || got.Text != "from same file" {
		t.Fatalf("same file: %v %v", got.Display(), err)
	}

	// 4. a quoted path is used as written, from the folder of the calling file
	put(t, dir, "elsewhere/x.ag", helperAgent("from quoted path"))
	boss2 := put(t, dir, "boss2.ag", strings.Replace(bossAgent, "link Helper", "link Helper from \"elsewhere/x.ag\"", 1))
	if got, err := runFile(t, rt, boss2, "go"); err != nil || got.Text != "from quoted path" {
		t.Fatalf("quoted path: %v %v", got.Display(), err)
	}
}

func TestAChangeToTheLinkedAgentIsPickedUpWithoutEditingTheCaller(t *testing.T) {
	dir := t.TempDir()
	rt := newRT(dir, nil)
	weather := func(text string) string {
		return "agent Weather\n  goal \"w\"\n  accepts ask city\n  on ask\n    reply \"" + text + " {city}\"\n"
	}
	planner := put(t, dir, "planner.ag", "agent Planner\n  goal \"p\"\n  link Weather\n  accepts plan city\n  on plan\n"+
		"    answer = Weather.ask city: city\n    reply \"Pack for this: {answer}\"\n")
	linked := put(t, dir, "weather.ag", weather("sunny in"))

	got, err := runFile(t, rt, planner, "plan", "city=Lisbon")
	if err != nil || got.Text != "Pack for this: sunny in Lisbon" {
		t.Fatalf("first run: %v %v", got.Display(), err)
	}

	// Change only the linked agent, in the same running process.
	before, _ := os.ReadFile(planner)
	put(t, dir, "weather.ag", weather("forecast for"))
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(linked, later, later); err != nil {
		t.Fatal(err)
	}
	got, err = runFile(t, rt, planner, "plan", "city=Lisbon")
	if err != nil || got.Text != "Pack for this: forecast for Lisbon" {
		t.Fatalf("second run: %v %v", got.Display(), err)
	}
	if after, _ := os.ReadFile(planner); string(after) != string(before) {
		t.Error("the caller was edited")
	}
}

func TestAQuotedFileWithOneAgentIsUsedWhateverTheAgentIsCalled(t *testing.T) {
	dir := t.TempDir()
	rt := newRT(dir, nil)
	put(t, dir, "elsewhere/x.ag", strings.Replace(helperAgent("from the impostor"), "agent Helper", "agent Impostor", 1))
	boss := put(t, dir, "boss.ag", strings.Replace(bossAgent, "link Helper", "link Helper from \"elsewhere/x.ag\"", 1))
	if got, err := runFile(t, rt, boss, "go"); err != nil || got.Text != "from the impostor" {
		t.Errorf("got %v %v", got.Display(), err)
	}

	// With several agents in the file, the name must match one of them.
	changed := put(t, dir, "elsewhere/x.ag", strings.Replace(helperAgent("a"), "Helper", "Alpha", 1)+strings.Replace(helperAgent("b"), "Helper", "Beta", 1))
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(changed, later, later); err != nil {
		t.Fatal(err)
	}
	_, err := runFile(t, rt, boss, "go")
	mustContain(t, errText(t, err), "x.ag has no agent called Helper", "it has: Alpha, Beta")
}

// ---------- cycles (from link_cycle) ----------

func TestAgentsCallingEachOtherStopWithTheCircleShown(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"cycle_a.ag", "cycle_b.ag"} {
		text, err := os.ReadFile(filepath.Join(examples, name))
		if err != nil {
			t.Fatal(err)
		}
		put(t, dir, name, string(text))
	}
	_, err := runFile(t, newRT(dir, nil), filepath.Join(dir, "cycle_a.ag"), "go")
	mustContain(t, errText(t, err),
		"CycleA asked CycleB, and CycleB asked CycleA again (CycleA -> CycleB -> CycleA)",
		"make one of them answer without calling the other")
}

func TestAnAgentLinkingToItselfIsACycle(t *testing.T) {
	dir := t.TempDir()
	loop := put(t, dir, "loop.ag", "agent Loop\n  goal \"l\"\n  link Loop\n  accepts go\n  on go\n    reply Loop.go\n")
	_, err := runFile(t, newRT(dir, nil), loop, "go")
	mustContain(t, errText(t, err), "Loop -> Loop")
}

func TestAMissingTargetSaysWhereItLooked(t *testing.T) {
	dir := t.TempDir()
	boss := put(t, dir, "boss.ag", "agent Boss\n  goal \"b\"\n  link Ghost\n  accepts go\n  on go\n    reply Ghost.hi\n")
	_, err := runFile(t, newRT(dir, nil), boss, "go")
	mustContain(t, errText(t, err),
		"Problem on line 3 of boss.ag",
		"I could not find an agent called Ghost",
		"I looked:",
		"Ghost.ag",
		`link Ghost from "path/to/file.ag"`)
}

func TestAQuotedFileThatIsNotThereIsExplained(t *testing.T) {
	dir := t.TempDir()
	boss := put(t, dir, "boss.ag", "agent Boss\n  goal \"b\"\n  link Ghost from \"nowhere/ghost.ag\"\n  accepts go\n  on go\n    reply Ghost.hi\n")
	_, err := runFile(t, newRT(dir, nil), boss, "go")
	mustContain(t, errText(t, err),
		"I could not find the agent file `nowhere/ghost.ag`",
		`link Ghost from "nowhere/ghost.ag"`,
		"starts from the folder of the file that has the link line")
}

func TestAnErrorInsideTheLinkedAgentKeepsItsOwnFileAndLine(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "inner.ag", "agent Inner\n  goal \"i\"\n  tool file\n  accepts go\n  on go\n    reply file.read path: \"missing.txt\"\n")
	outer := put(t, dir, "outer.ag", "agent Outer\n  goal \"o\"\n  link Inner\n  accepts go\n  on go\n    reply Inner.go\n")
	_, err := runFile(t, newRT(dir, nil), outer, "go")
	mustContain(t, errText(t, err),
		"Problem on line 6 of outer.ag: Inner could not answer `go`",
		"Problem on line 6 of inner.ag",
		"the file `missing.txt` does not exist")
}

// ---------- the interface of the target (from link_interface) ----------

const writerAgent = "agent Writer\n  goal \"Write a marker file\"\n  tool file\n  accepts note text\n  on note\n" +
	"    file.write path: \"marker.txt\" text: text\n    reply \"written\"\n"

func bossCalling(call string) string {
	return "agent Boss\n  goal \"b\"\n  link Writer\n  accepts go\n  on go\n    r = " + call + "\n    reply r\n"
}

func TestACallTheTargetDoesNotAcceptIsRejectedBeforeItRuns(t *testing.T) {
	for _, tt := range []struct{ call, want string }{
		{`Writer.nots text: "x"`, "did you mean `note`?"},
		{`Writer.note`, "needs a value for `text`"},
		{`Writer.note text: "x" extra: "y"`, "does not take `extra`"},
		{`Writer.note txt: "x"`, "needs a value for `text`"},
	} {
		dir := t.TempDir()
		put(t, dir, "writer.ag", writerAgent)
		boss := put(t, dir, "boss.ag", bossCalling(tt.call))
		_, err := runFile(t, newRT(dir, nil), boss, "go")
		mustContain(t, errText(t, err), tt.want, "Problem on line 6")
		if _, statErr := os.Stat(filepath.Join(dir, "marker.txt")); statErr == nil {
			t.Errorf("the target ran for %s", tt.call)
		}
	}
}

func TestTheSameMismatchIsCaughtWhenRunningIfTheFileChangedAfterChecking(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "writer.ag", writerAgent)
	agent := agentFrom(t, newRT(dir, nil), bossCalling(`Writer.nots text: "x"`), "c")
	_, err := ask(agent, "go")
	mustContain(t, errText(t, err), "does not accept the message `nots`", "Problem on line 6")
	if _, statErr := os.Stat(filepath.Join(dir, "marker.txt")); statErr == nil {
		t.Error("the target ran")
	}
}

func TestALinkedAgentListsTheMessagesItAccepts(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "writer.ag", writerAgent)
	agent := agentFrom(t, newRT(dir, nil), bossCalling(`Writer.note text: "x"`), "c")
	actions, err := agent.Tools["Writer"].Actions(context.Background())
	if err != nil {
		t.Fatal(errText(t, err))
	}
	if len(actions) != 1 || actions[0].Name != "note" || len(actions[0].Params) != 1 ||
		actions[0].Params[0].Name != "text" || !actions[0].Params[0].Required {
		t.Errorf("actions = %+v", actions)
	}
}

func TestALinkedAgentRunsAndAnswers(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "writer.ag", writerAgent)
	boss := put(t, dir, "boss.ag", bossCalling(`Writer.note text: "hello"`))
	got, err := runFile(t, newRT(dir, nil), boss, "go")
	if err != nil || got.Text != "written" {
		t.Fatalf("got %v %v", got.Display(), err)
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "marker.txt")); string(onDisk) != "hello" {
		t.Errorf("the linked agent wrote %q", onDisk)
	}
}

func TestACycleIsFoundBeforeTheSecondCallNotByATimeout(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "a.ag", "agent A\n  goal \"a\"\n  link B\n  accepts go\n  on go\n    reply B.go\n")
	put(t, dir, "b.ag", "agent B\n  goal \"b\"\n  link C\n  accepts go\n  on go\n    reply C.go\n")
	put(t, dir, "c.ag", "agent C\n  goal \"c\"\n  link A\n  accepts go\n  on go\n    reply A.go\n")
	start := time.Now()
	_, err := runFile(t, newRT(dir, nil), filepath.Join(dir, "a.ag"), "go")
	if time.Since(start) > 5*time.Second {
		t.Errorf("the cycle took %v to be found", time.Since(start))
	}
	mustContain(t, errText(t, err), "these agents are calling each other in a circle (A -> B -> C -> A)")
}

func TestExactCaseNamesTheFileTheWayItIsWrittenOnDisk(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "inner.ag", helperAgent("x"))
	want := filepath.Join(dir, "inner.ag")
	if got := exactCase(filepath.Join(dir, "Inner.ag")); got != want {
		t.Errorf("exactCase(Inner.ag) = %q, want %q", got, want)
	}
	if got := exactCase(want); got != want {
		t.Errorf("a name that already matches must stay as it is, got %q", got)
	}
	missing := filepath.Join(dir, "nothing.ag")
	if got := exactCase(missing); got != missing {
		t.Errorf("a file that is not there must stay as it is, got %q", got)
	}
}

func TestWhenNamesDifferOnlyByCaseTheExactOneWins(t *testing.T) {
	dir := t.TempDir()
	put(t, dir, "Helper.ag", helperAgent("upper"))
	put(t, dir, "helper.ag", helperAgent("lower"))
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Skip("this file system ignores case, so the two names cannot exist together")
	}
	if got := exactCase(filepath.Join(dir, "Helper.ag")); got != filepath.Join(dir, "Helper.ag") {
		t.Errorf("exactCase(Helper.ag) = %q", got)
	}
	if got := exactCase(filepath.Join(dir, "helper.ag")); got != filepath.Join(dir, "helper.ag") {
		t.Errorf("exactCase(helper.ag) = %q", got)
	}
}

// req: T5
func TestALinkToAFileThatDoesNotExistYetIsJudgedByWhereItWouldBe(t *testing.T) {
	// On some systems the temporary folder is reached through a link; a path
	// that does not exist yet must still be recognised as inside the project.
	project := t.TempDir()
	boss := put(t, project, "boss.ag", "agent Boss\n  goal \"b\"\n  link Near from \"sub/not-there-yet.ag\"\n  accepts go\n  on go\n    reply \"x\"\n")
	if warnings := LinkWarnings(project, mustLoad(t, boss)); len(warnings) != 0 {
		t.Errorf("a link inside the project was warned about: %s", warnings[0].Render())
	}
}
