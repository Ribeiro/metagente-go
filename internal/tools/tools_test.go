package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// ---------- env ----------

func newEnv(names []string, hidden []string) *Env {
	return NewEnv(&lang.ToolDecl{Name: "env", Kind: lang.ToolEnv, EnvNames: names}, hidden)
}

func TestOnlyTheDeclaredVariablesCanBeRead(t *testing.T) {
	t.Setenv("MG_TEST_ENV_A", "alpha")
	e := newEnv([]string{"MG_TEST_ENV_A"}, nil)
	ctx := context.Background()

	got, err := e.Call(ctx, "get", args("name", "MG_TEST_ENV_A"))
	if err != nil || got.Text != "alpha" {
		t.Fatalf("reading a declared variable: %v %v", got, err)
	}
	_, err = e.Call(ctx, "get", args("name", "PATH"))
	mustContain(t, rendered(t, err),
		"did not declare the variable `PATH`",
		`tool env "MG_TEST_ENV_A" "PATH"`)
}

func TestADeclaredVariableThatIsNotSetGivesNothing(t *testing.T) {
	e := newEnv([]string{"MG_TEST_ENV_NOT_SET"}, nil)
	got, err := e.Call(context.Background(), "get", args("name", "MG_TEST_ENV_NOT_SET"))
	if err != nil || got.Kind != value.KindNothing {
		t.Errorf("got %v, %v; want nothing", got, err)
	}
}

// req: E6
func TestSecretsAreUnreadableEvenWhenDeclared(t *testing.T) {
	t.Setenv("MG_TEST_KEY_HIDDEN", "very-secret")
	t.Setenv("METAGENTE_TOKEN", "token-value")
	cfg := config.Default()
	cfg.LLM.APIKeyEnv = "MG_TEST_KEY_HIDDEN"
	e := newEnv([]string{"MG_TEST_KEY_HIDDEN", "ANTHROPIC_API_KEY", "METAGENTE_TOKEN", "OTHER"}, cfg.HiddenEnv())

	for _, name := range []string{"MG_TEST_KEY_HIDDEN", "ANTHROPIC_API_KEY", "OPENAI_API_KEY", "METAGENTE_TOKEN"} {
		_, err := e.Call(context.Background(), "get", args("name", name))
		text := rendered(t, err)
		mustContain(t, text, "is a secret", "agents can never read it")
		if strings.Contains(text, "very-secret") || strings.Contains(text, "token-value") {
			t.Errorf("the message leaks the secret:\n%s", text)
		}
	}
}

func TestEnvNeedsAName(t *testing.T) {
	e := newEnv([]string{"A"}, nil)
	_, err := e.Call(context.Background(), "get", Args{})
	mustContain(t, rendered(t, err), "`env.get` needs a value for `name`")
	_, err = e.Call(context.Background(), "set", Args{})
	mustContain(t, rendered(t, err), "`env` can do: get")
}

// ---------- state ----------

func newStateTool(store *StateStore, agent, ctx string) *State {
	return NewState(&lang.ToolDecl{Name: "state", Kind: lang.ToolState}, store.For(agent, ctx))
}

func TestStateRemembersAndRecalls(t *testing.T) {
	s := newStateTool(NewStateStore(config.Default().Limits), "Memory", "c1")
	ctx := context.Background()
	if _, err := s.Call(ctx, "set", Args{"key": value.Text("thing"), "value": value.Text("blue")}); err != nil {
		t.Fatal(rendered(t, err))
	}
	got, err := s.Call(ctx, "get", args("key", "thing"))
	if err != nil || got.Text != "blue" {
		t.Errorf("recalled %v, %v", got, err)
	}
	if got, _ := s.Call(ctx, "get", args("key", "unknown")); got.Kind != value.KindNothing {
		t.Errorf("an unknown key should give nothing, got %v", got)
	}
}

// req: S7
func TestStateIsSharedOnlyWithinTheSameAgentAndContext(t *testing.T) {
	store := NewStateStore(config.Default().Limits)
	ctx := context.Background()
	first := newStateTool(store, "Memory", "conversation-1")
	if _, err := first.Call(ctx, "set", Args{"key": value.Text("thing"), "value": value.Text("blue")}); err != nil {
		t.Fatal(rendered(t, err))
	}

	// A new tool over the same agent and context sees the memory.
	same := newStateTool(store, "Memory", "conversation-1")
	if got, _ := same.Call(ctx, "get", args("key", "thing")); got.Text != "blue" {
		t.Errorf("the same context should share its memory, got %v", got)
	}
	// Another context does not.
	other := newStateTool(store, "Memory", "conversation-2")
	if got, _ := other.Call(ctx, "get", args("key", "thing")); got.Kind != value.KindNothing {
		t.Errorf("another context must not see the memory, got %v", got)
	}
	// Another agent does not either.
	stranger := newStateTool(store, "Other", "conversation-1")
	if got, _ := stranger.Call(ctx, "get", args("key", "thing")); got.Kind != value.KindNothing {
		t.Errorf("another agent must not see the memory, got %v", got)
	}
}

// req: D3
func TestStateRefusesToGrowPastItsLimits(t *testing.T) {
	limits := config.Default().Limits
	limits.MaxStateEntries = 3
	limits.MaxStateBytes = 200
	s := newStateTool(NewStateStore(limits), "A", "c")
	ctx := context.Background()
	set := func(key, text string) error {
		_, err := s.Call(ctx, "set", Args{"key": value.Text(key), "value": value.Text(text)})
		return err
	}

	for _, key := range []string{"a", "b", "c"} {
		if err := set(key, "x"); err != nil {
			t.Fatal(rendered(t, err))
		}
	}
	mustContain(t, rendered(t, set("d", "x")), "the agent's memory is full", "it already holds 3 things", "max_state_entries")

	// Replacing a key does not count as a new entry.
	if err := set("a", "y"); err != nil {
		t.Errorf("replacing a value should work: %v", rendered(t, err))
	}
	// A value that would take the memory past its byte ceiling is refused.
	mustContain(t, rendered(t, set("a", strings.Repeat("z", 300))), "would hold more than 200 bytes")
	// ... and the old value is still there.
	if got, _ := s.Call(ctx, "get", args("key", "a")); got.Text != "y" {
		t.Errorf("a refused write must leave the old value, got %q", got.Text)
	}
}

func TestStateNeedsAKey(t *testing.T) {
	s := newStateTool(NewStateStore(config.Default().Limits), "A", "c")
	_, err := s.Call(context.Background(), "set", Args{"value": value.Text("x")})
	mustContain(t, rendered(t, err), "`state.set` needs a value for `key`")
	_, err = s.Call(context.Background(), "drop", Args{})
	mustContain(t, rendered(t, err), "`state` can do: set, get")
}

// ---------- clock ----------

func newClock(maxWait int) *Clock {
	return NewClock(&lang.ToolDecl{Name: "clock", Kind: lang.ToolClock}, maxWait)
}

func TestClockNowHasTextAndUnix(t *testing.T) {
	got, err := newClock(10).Call(context.Background(), "now", Args{})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	unix, ok := got.Field("unix")
	if !ok || unix.Number < 1_700_000_000 {
		t.Errorf("unix = %v", unix)
	}
	text, ok := got.Field("text")
	if !ok || !strings.HasPrefix(text.Text, "20") {
		t.Errorf("text = %v", text)
	}
}

func TestClockWaitWaits(t *testing.T) {
	start := time.Now()
	if _, err := newClock(10).Call(context.Background(), "wait", Args{"seconds": value.Number(0.1)}); err != nil {
		t.Fatal(rendered(t, err))
	}
	if elapsed := time.Since(start); elapsed < 90*time.Millisecond {
		t.Errorf("waited only %v", elapsed)
	}
}

// req: D1
func TestClockWaitRefusesWhatIsTooLongOrNotANumber(t *testing.T) {
	c := newClock(5)
	ctx := context.Background()
	for _, seconds := range []value.Value{value.Number(6), value.Number(-1), value.Text("NaN"), value.Text("Inf"), value.Text("soon"), value.Nothing} {
		_, err := c.Call(ctx, "wait", Args{"seconds": seconds})
		if err == nil {
			t.Errorf("%v should be refused", seconds.Display())
			continue
		}
		text := rendered(t, err)
		if !strings.Contains(text, "`clock.wait` needs") {
			t.Errorf("unexpected message for %v:\n%s", seconds.Display(), text)
		}
	}
	_, err := c.Call(ctx, "wait", Args{"seconds": value.Number(6)})
	mustContain(t, rendered(t, err), "between 0 and 5 seconds", "max_wait_seconds")
}

func TestClockWaitStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := newClock(10).Call(ctx, "wait", Args{"seconds": value.Number(5)})
	if err == nil {
		t.Fatal("the wait should have been interrupted")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("the wait did not stop in time: %v", elapsed)
	}
}

// ---------- registry ----------

func TestBuildCreatesOnlyWhatTheAgentDeclared(t *testing.T) {
	agents, err := lang.ParseFile("a.ag", "",
		"agent A\n  goal \"x\"\n  tool file\n  tool clock\n  tool weather from mcp \"cmd@1.0.0\"\n  link Helper\n  remote Bob at \"http://127.0.0.1:1\"\n")
	if err != nil {
		t.Fatal(err)
	}
	registry, err := Build(agents[0], Options{Root: t.TempDir(), Limits: config.Default().Limits, MaxWaitSeconds: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file", "clock", "weather", "Helper", "Bob"} {
		if registry[name] == nil {
			t.Errorf("%s is missing from the registry", name)
		}
	}
	for _, name := range []string{"http", "env", "state"} {
		if registry[name] != nil {
			t.Errorf("%s was not declared and must not be reachable", name)
		}
	}
}

func TestToolsThatArriveLaterSayTheyAreNotAvailableYet(t *testing.T) {
	for _, tool := range []Tool{
		Unavailable("weather", "tool servers (MCP)"),
		Unavailable("Helper", "links to other agents"),
		Unavailable("Bob", "remote agents"),
	} {
		_, err := tool.Call(context.Background(), "anything", Args{})
		mustContain(t, rendered(t, err), "`"+tool.Name()+"` uses", "not available in this build yet", "Fix: ")
	}
}
