package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"metagente/internal/diag"
	"metagente/internal/llm"
)

// The tests that make something break inside must not write in the log of the
// person who runs them.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "metagente-state-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("METAGENTE_STATE_DIR", filepath.Join(dir, "state"))
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func readLog(t *testing.T, state string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(state, "metagente.log"))
	if err != nil {
		t.Fatalf("there is no log: %v", err)
	}
	return string(raw)
}

// req: P2, P1
func TestAToolThatPanicsInALineLeavesTheCauseInTheLogAndOnlyASentenceInTheError(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("METAGENTE_STATE_DIR", state)
	rt := newRT(t.TempDir(), nil)
	agent := agentFrom(t, rt, "agent A\n  goal \"x\"\n  tool clock\n  accepts go\n  on go\n    reply clock.now\n", "c")
	agent.Tools["clock"] = explodingTool{}

	_, err := ask(agent, "go")
	text := errText(t, err)
	mustContain(t, text, "Something went wrong inside Metagente", "The details were saved in "+filepath.Join(state, "metagente.log"))
	if strings.Contains(text, "kaboom") {
		t.Errorf("the cause is in the error:\n%s", text)
	}

	// What a remote caller or a model would read has no path and no cause.
	d, ok := diag.From(err)
	if !ok {
		t.Fatal("the error is not a diagnostic")
	}
	public := d.Public()
	if strings.Contains(public, state) || strings.Contains(public, "kaboom") || strings.Contains(public, "details were saved") {
		t.Errorf("the public text leaks:\n%s", public)
	}

	logged := readLog(t, state)
	mustContain(t, logged, "PANIC tool call clock.now: kaboom", "explodingTool", "goroutine")
}

// req: P2, P1
func TestTheCauseOfAPanicInAToolOfThinkIsInTheLogButNotInWhatTheModelReads(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("METAGENTE_STATE_DIR", state)
	model := llm.NewScripted(llm.Ask(llm.Use("c1", "boom__go", nil)), llm.Say("survived"))
	rt := thinkRT(t, model, nil)
	agent := agentFrom(t, rt, thinkSource(nil, `think "go"`), "c")
	agent.Tools["boom"] = explodingTool{}
	if _, err := ask(agent, "go"); err != nil {
		t.Fatal(errText(t, err))
	}
	result := model.Requests()[1].Messages[2].Parts[0].Text
	if strings.Contains(result, "kaboom") || strings.Contains(result, state) {
		t.Errorf("the model was told the cause or the place of the log:\n%s", result)
	}
	mustContain(t, readLog(t, state), "PANIC think tool call go: kaboom")
}

// req: P3
func TestTheSecretsOfTheSetupNeverReachTheLog(t *testing.T) {
	const key, token = "sk-ant-api03-abcdef0123456789", "tok-live-0123456789"
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("METAGENTE_STATE_DIR", state)
	rt := newRT(t.TempDir(), nil)
	rt.Config.LLM.APIKeyEnv = "MY_MODEL_KEY"
	rt.Config.Credentials = map[string]string{"Bob": "BOB_TOKEN"}
	rt.Getenv = func(name string) string {
		return map[string]string{"MY_MODEL_KEY": key, "BOB_TOKEN": token}[name]
	}
	if _, err := rt.Log.Panic("test", "failed with "+key+" and "+token, []byte("stack mentioning "+token)); err != nil {
		t.Fatal(err)
	}
	logged := readLog(t, state)
	if strings.Contains(logged, key) || strings.Contains(logged, token) {
		t.Errorf("a secret is in the log:\n%s", logged)
	}
	mustContain(t, logged, "failed with [hidden] and [hidden]", "stack mentioning [hidden]")
}
