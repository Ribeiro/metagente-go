package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"metagente/internal/config"
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/tools"
	"metagente/internal/trust"
	"metagente/internal/value"
)

const examples = "../../testdata/examples"

func newRT(dir string, tweak func(*config.Config)) *Runtime {
	cfg := config.Default()
	cfg.Root = dir
	if tweak != nil {
		tweak(cfg)
	}
	return New(cfg)
}

func errText(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a problem")
	}
	d, ok := diag.From(err)
	if !ok {
		t.Fatalf("the error is not a diagnostic: %v", err)
	}
	return d.Render()
}

func mustContain(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

// runSource writes the source next to the project and runs it the way
// `metagente run` does, checks included.
func runSource(t *testing.T, rt *Runtime, source, message string, params ...string) (value.Value, error) {
	t.Helper()
	file := filepath.Join(rt.Config.Root, "agent.ag")
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	return RunFile(context.Background(), rt, Options{File: file, Message: message, Params: params})
}

func runExample(t *testing.T, rt *Runtime, name, message string, params ...string) (value.Value, error) {
	t.Helper()
	return RunFile(context.Background(), rt, Options{File: filepath.Join(examples, name), Message: message, Params: params})
}

// agentFrom prepares an agent without running the static checks, to prove
// that the runtime defends itself on its own.
func agentFrom(t *testing.T, rt *Runtime, source, contextID string) *Agent {
	t.Helper()
	agents, err := lang.ParseFile("test.ag", "", source)
	if err != nil {
		t.Fatal(errText(t, err))
	}
	agent, err := NewAgent(rt, agents[0], contextID)
	if err != nil {
		t.Fatal(errText(t, err))
	}
	return agent
}

func ask(agent *Agent, message string, params ...string) (value.Value, error) {
	args := tools.Args{}
	for i := 0; i+1 < len(params); i += 2 {
		args[params[i]] = value.Text(params[i+1])
	}
	return agent.Handle(context.Background(), &Call{TaskID: "t"}, message, args)
}

// ---------- built in tools (from builtin_tools) ----------

func TestTheReadFileExampleReadsAFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello from notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := runExample(t, newRT(dir, nil), "read_file.ag", "summarize")
	if err != nil {
		t.Fatal(errText(t, err))
	}
	if got.Kind != value.KindText || got.Text != "hello from notes" {
		t.Errorf("got %v", got.Display())
	}
}

func TestAMissingFileGivesAPlainMessageWithTheLine(t *testing.T) {
	_, err := runExample(t, newRT(t.TempDir(), nil), "read_file.ag", "summarize")
	mustContain(t, errText(t, err), "the file `notes.txt` does not exist", "line 6", "of read_file.ag")
}

func TestFileWriteThenReadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	source := "agent Notes\n  goal \"Keep notes\"\n  tool file\n  accepts save text\n  on save\n" +
		"    file.write path: \"out/a.txt\" text: text\n    back = file.read path: \"out/a.txt\"\n    reply back\n"
	got, err := runSource(t, newRT(dir, nil), source, "save", "text=remember me")
	if err != nil {
		t.Fatal(errText(t, err))
	}
	if got.Text != "remember me" {
		t.Errorf("got %q", got.Text)
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "out", "a.txt")); string(onDisk) != "remember me" {
		t.Errorf("on disk: %q", onDisk)
	}
}

func TestTheClockExampleReportsTheTimeAndWaits(t *testing.T) {
	start := time.Now()
	got, err := runExample(t, newRT(t.TempDir(), nil), "clock.ag", "now")
	if err != nil {
		t.Fatal(errText(t, err))
	}
	if time.Since(start) < time.Second {
		t.Errorf("the example should wait a second, took %v", time.Since(start))
	}
	if !strings.HasPrefix(got.Text, "It was 20") || !strings.HasSuffix(got.Text, "a second ago") {
		t.Errorf("got %q", got.Text)
	}
}

func TestControlFlowAndLists(t *testing.T) {
	source := "agent Flow\n  goal \"Try the language\"\n  tool state\n  accepts go n\n  on go\n" +
		"    total = 0\n    for item in [1, 2, 3]\n      state.set key: \"last\" value: item\n" +
		"    last = state.get key: \"last\"\n    if n is more than 5 and last is 3\n      reply \"big {last}\"\n" +
		"    otherwise\n      reply \"small {last}\"\n"
	rt := newRT(t.TempDir(), nil)
	if got, err := runSource(t, rt, source, "go", "n=9"); err != nil || got.Text != "big 3" {
		t.Errorf("n=9: %v %v", got.Display(), err)
	}
	if got, err := runSource(t, rt, source, "go", "n=2"); err != nil || got.Text != "small 3" {
		t.Errorf("n=2: %v %v", got.Display(), err)
	}
}

func TestAReplyInsideALoopEndsTheHandler(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  accepts go\n  on go\n    for x in [1, 2, 3]\n      if x is 2\n        reply x\n    reply \"never\"\n"
	got, err := runSource(t, newRT(t.TempDir(), nil), source, "go")
	if err != nil || got.Display() != "2" {
		t.Errorf("got %v, %v", got.Display(), err)
	}
}

func TestExpressions(t *testing.T) {
	tests := []struct {
		expr string
		want string
	}{
		{`"5" is 5`, "yes"},
		{`5 is "5"`, "yes"},
		{`3 is not 4`, "yes"},
		{`"abc" contains "b"`, "yes"},
		{`[1, 2] contains 2`, "yes"},
		{`["a", "b"] contains "c"`, "no"},
		{`2 is more than 10`, "no"},
		{`"10" is more than 9`, "yes"},
		{`3 is less than 4`, "yes"},
		{`not no`, "yes"},
		{`yes and no`, "no"},
		{`no or yes`, "yes"},
		{`nothing`, "nothing"},
		{`[1, "a", yes]`, "[1, a, yes]"},
	}
	rt := newRT(t.TempDir(), nil)
	for _, tt := range tests {
		source := "agent A\n  goal \"x\"\n  accepts go\n  on go\n    reply " + tt.expr + "\n"
		got, err := runSource(t, rt, source, "go")
		if err != nil {
			t.Errorf("%s: %s", tt.expr, errText(t, err))
			continue
		}
		if got.Display() != tt.want {
			t.Errorf("%s = %q, want %q", tt.expr, got.Display(), tt.want)
		}
	}
}

func TestRuntimeErrorsInExpressionsAreExplained(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []string
	}{
		{"comparing text with a number", `reply "x" is more than 1`, []string{"I can only compare numbers", "text and a number"}},
		{"contains on a number", `reply 5 contains 1`, []string{"`contains` needs text or a list on its left", "a number"}},
		{"for over a number", "for x in 5\n      reply x", []string{"`for` needs a list, but it got a number", "[1, 2, 3]"}},
		{"field of text", "x = \"a\"\n    reply x.y", []string{"`x` is text, so it has no field `y`"}},
		{"misspelled field", "t = clock.now\n    reply t.unx", []string{"`t` has no field called `unx`", "did you mean `t.unix`?"}},
		{"tool used as a value", "reply clock", []string{"`clock` is a tool, not a value", "clock.action"}},
		{"fail with its own words", `fail "I need a city"`, []string{"Problem on line 6", "I need a city"}},
	}
	rt := newRT(t.TempDir(), nil)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := "agent A\n  goal \"x\"\n  tool clock\n  accepts go\n  on go\n    " + tt.body + "\n"
			_, err := runSource(t, rt, source, "go")
			mustContain(t, errText(t, err), tt.want...)
		})
	}
}

// ---------- permissions (from permissions) ----------

func TestAnUndeclaredToolIsRefusedAndTheFileIsUntouched(t *testing.T) {
	dir := t.TempDir()
	source := "agent Sneaky\n  goal \"x\"\n  accepts go\n  on go\n    file.write path: \"x.txt\" text: \"hi\"\n    reply \"done\"\n"

	// Through `run`, the static check refuses it before anything happens.
	_, err := runSource(t, newRT(dir, nil), source, "go")
	mustContain(t, errText(t, err),
		"Problem on line 5",
		"agent Sneaky uses `file` but never declared it",
		"add the line `tool file` under `agent Sneaky`")

	// And the runtime refuses it on its own, when the static check is skipped.
	agent := agentFrom(t, newRT(dir, nil), source, "c")
	_, err = ask(agent, "go")
	mustContain(t, errText(t, err), "Problem on line 5", "never declared it")

	if _, statErr := os.Stat(filepath.Join(dir, "x.txt")); statErr == nil {
		t.Error("an undeclared tool wrote a file")
	}
}

func TestLinksRemotesAndMCPServersMustBeDeclaredToo(t *testing.T) {
	rt := newRT(t.TempDir(), nil)
	for _, tt := range []struct{ target, call string }{
		{"Other", `Other.ask city: "x"`},
		{"weather", `weather.forecast city: "x"`},
	} {
		source := "agent A\n  goal \"x\"\n  accepts go\n  on go\n    r = " + tt.call + "\n    reply r\n"
		_, err := ask(agentFrom(t, rt, source, "c"), "go")
		mustContain(t, errText(t, err),
			"does not know anything called `"+tt.target+"`", "`link", "`remote", "from mcp")
	}
}

func TestADeclaredToolServerIsNotApprovedByDefault(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  tool weather from mcp \"weather-mcp@1.0.0\"\n  accepts go\n  on go\n    r = weather.forecast city: \"x\"\n    reply r\n"
	rt := newRT(t.TempDir(), nil)
	rt.Trust = trust.NewRegistry(filepath.Join(t.TempDir(), "config"))
	_, err := runSource(t, rt, source, "go")
	mustContain(t, errText(t, err), "have not approved for this project", "starts the program: weather-mcp@1.0.0", "Fix: ")
}

// ---------- timeouts (from timeout) ----------

func TestASlowCallTimesOutWithAPlainMessageAndTheAgentKeepsWorking(t *testing.T) {
	source := "agent Slow\n  goal \"Be slow\"\n  tool clock\n  accepts wait\n  accepts quick\n  on wait\n" +
		"    clock.wait seconds: 5 within 1 seconds\n    reply \"never\"\n  on quick\n    reply \"quick\"\n"
	agent := agentFrom(t, newRT(t.TempDir(), nil), source, "c")

	start := time.Now()
	_, err := ask(agent, "wait")
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("took %v", elapsed)
	}
	mustContain(t, errText(t, err), "did not finish within 1 seconds", "within 60 seconds", "Problem on line 7")

	got, err := ask(agent, "quick")
	if err != nil || got.Text != "quick" {
		t.Errorf("the agent should keep working after a timeout: %v %v", got.Display(), err)
	}
}

func TestTheDefaultTimeoutComesFromConfiguration(t *testing.T) {
	rt := newRT(t.TempDir(), func(c *config.Config) { c.Runtime.TimeoutSeconds = 1 })
	source := "agent S\n  goal \"s\"\n  tool clock\n  accepts go\n  on go\n    clock.wait seconds: 5\n    reply \"never\"\n"
	start := time.Now()
	_, err := runSource(t, rt, source, "go")
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Errorf("took %v", elapsed)
	}
	mustContain(t, errText(t, err), "did not finish within 1 seconds")
}

// req: D1
func TestWithinAboveTheMaximumIsRefused(t *testing.T) {
	rt := newRT(t.TempDir(), func(c *config.Config) { c.Runtime.MaxWaitSeconds = 10 })
	source := "agent S\n  goal \"s\"\n  tool clock\n  accepts go\n  on go\n    clock.wait seconds: 1 within 11 seconds\n    reply \"never\"\n"
	_, err := runSource(t, rt, source, "go")
	mustContain(t, errText(t, err), "`within 11 seconds` is more than the 10 seconds this setup allows", "max_wait_seconds")
}

// ---------- state across calls (from http_state_tools) ----------

// req: S7
func TestStateIsSharedOnlyWithinTheSameAgentAndContext(t *testing.T) {
	const memory = "agent Memory\n  goal \"Remember things\"\n  tool state\n  accepts remember what\n  accepts recall\n" +
		"  on remember\n    state.set key: \"thing\" value: what\n    reply \"ok\"\n  on recall\n    reply state.get key: \"thing\"\n"
	rt := newRT(t.TempDir(), nil)

	first := agentFrom(t, rt, memory, "conversation-1")
	if _, err := ask(first, "remember", "what", "blue"); err != nil {
		t.Fatal(errText(t, err))
	}
	// A new instance of the same agent in the same conversation shares the memory.
	if got, _ := ask(agentFrom(t, rt, memory, "conversation-1"), "recall"); got.Text != "blue" {
		t.Errorf("the same conversation should share its memory, got %v", got.Display())
	}
	// Another conversation does not.
	if got, _ := ask(agentFrom(t, rt, memory, "conversation-2"), "recall"); got.Kind != value.KindNothing {
		t.Errorf("another conversation must not see the memory, got %v", got.Display())
	}
	// Another agent in the same conversation does not either.
	other := strings.ReplaceAll(memory, "Memory", "Other")
	if got, _ := ask(agentFrom(t, rt, other, "conversation-1"), "recall"); got.Kind != value.KindNothing {
		t.Errorf("another agent must not see the memory, got %v", got.Display())
	}
}

// ---------- messages (from diagnostics_golden) ----------

func TestUnknownMessageAndMissingOrExtraValues(t *testing.T) {
	agent := agentFrom(t, newRT(t.TempDir(), nil), "agent A\n  goal \"x\"\n  accepts ask city\n  on ask\n    reply city\n", "c")
	tests := []struct {
		message string
		params  []string
		want    string
	}{
		{"aks", nil, "did you mean `ask`?"},
		{"ask", nil, "needs a value for `city`"},
		{"ask", []string{"cty", "x"}, "needs a value for `city`"},
		{"ask", []string{"city", "x", "extra", "y"}, "does not take `extra`"},
		{"dance", nil, "A accepts: ask"},
	}
	for _, tt := range tests {
		_, err := ask(agent, tt.message, tt.params...)
		text := errText(t, err)
		mustContain(t, text, tt.want, "Fix: ")
	}
}

func TestAnAgentThatAcceptsNothingExplainsHowToAddAMessage(t *testing.T) {
	agent := agentFrom(t, newRT(t.TempDir(), nil), "agent A\n  goal \"x\"\n", "c")
	_, err := ask(agent, "go")
	mustContain(t, errText(t, err), "agent A does not accept the message `go`", "add a line like `accepts go` to agent A")
}

func TestRunStopsOnProblemsAndSaysHowManyMore(t *testing.T) {
	dir := t.TempDir()
	source := "agent Bad\n  accepts go\n  on go\n    x = nme\n    reply q\n"
	_, err := runSource(t, newRT(dir, nil), source, "go")
	mustContain(t, errText(t, err), "agent Bad has no goal", "(2 more problems in this file; run `metagente check", "to see them all)")
}

// ---------- choosing the agent and the message ----------

const hello = "agent Hello\n  goal \"Say hello to someone\"\n  accepts greet name\n  on greet\n    reply \"Hello, {name}!\"\n"

func TestWithOneAcceptedMessageTheMessageCanBeLeftOut(t *testing.T) {
	rt := newRT(t.TempDir(), nil)
	for name, run := range map[string]func() (value.Value, error){
		"message and values": func() (value.Value, error) { return runSource(t, rt, hello, "greet", "name=Ana") },
		"values only":        func() (value.Value, error) { return runSource(t, rt, hello, "name=Ana") },
	} {
		got, err := run()
		if err != nil || got.Text != "Hello, Ana!" {
			t.Errorf("%s: %v %v", name, got.Display(), err)
		}
	}
}

func TestWithSeveralMessagesTheAgentMustBeTold(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  accepts one\n  accepts two\n  on one\n    reply \"1\"\n  on two\n    reply \"2\"\n"
	_, err := runSource(t, newRT(t.TempDir(), nil), source, "")
	mustContain(t, errText(t, err), "agent A needs to be told what to do", "it accepts: one, two", "metagente run")
}

func TestPickAgent(t *testing.T) {
	agents, err := lang.ParseFile("two.ag", "", "agent First\n  goal \"a\"\nagent Second\n  goal \"b\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := PickAgent(agents, "", "two.ag"); err != nil || got.Name != "First" {
		t.Errorf("default = %v, %v", got, err)
	}
	if got, err := PickAgent(agents, "Second", "two.ag"); err != nil || got.Name != "Second" {
		t.Errorf("Second = %v, %v", got, err)
	}
	_, err = PickAgent(agents, "Third", "dir/two.ag")
	mustContain(t, errText(t, err), "two.ag has no agent called `Third`", "it has: First, Second")
}

func TestParseParams(t *testing.T) {
	args, err := ParseParams([]string{"a=1", "b=x=y", " c = spaced "})
	if err != nil {
		t.Fatal(errText(t, err))
	}
	if args["a"].Text != "1" || args["b"].Text != "x=y" || args["c"].Text != " spaced " {
		t.Errorf("args = %v", args)
	}
	for _, bad := range []string{"novalue", "=x"} {
		_, err := ParseParams([]string{bad})
		mustContain(t, errText(t, err), "is not a key=value pair", "city=Lisbon")
	}
}

func TestShowValue(t *testing.T) {
	if _, ok := ShowValue(value.Nothing); ok {
		t.Error("nothing prints nothing")
	}
	if text, ok := ShowValue(value.Text("hi")); !ok || text != "hi" {
		t.Errorf("text = %q, %v", text, ok)
	}
	record := value.Record(map[string]value.Value{"b": value.Text("x"), "a": value.Number(1)})
	if text, _ := ShowValue(record); text != "{\n  \"a\": 1,\n  \"b\": \"x\"\n}" {
		t.Errorf("record = %q", text)
	}
	if text, _ := ShowValue(value.List([]value.Value{value.Number(1), value.Bool(true)})); text != "[\n  1,\n  true\n]" {
		t.Errorf("list = %q", text)
	}
}

func TestTheOnStartSectionRunsBeforeTheHandler(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  tool state\n  accepts go\n  on start\n    state.set key: \"k\" value: \"started\"\n" +
		"  on go\n    reply state.get key: \"k\"\n"
	got, err := runSource(t, newRT(t.TempDir(), nil), source, "go")
	if err != nil || got.Text != "started" {
		t.Errorf("got %v, %v", got.Display(), err)
	}
}
