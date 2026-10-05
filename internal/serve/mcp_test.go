package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"metagente/internal/applog"
	"metagente/internal/diag"
)

func newMCP(t *testing.T, tweak func(*MCPConfig), agents ...Agent) *MCPServer {
	t.Helper()
	cfg := MCPConfig{Version: "1.2.3"}
	if tweak != nil {
		tweak(&cfg)
	}
	if len(agents) == 0 {
		agents = []Agent{newFake("Bob", defaultSkills...)}
	}
	m, err := NewMCP(cfg, agents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

// connect gives a client of the SDK, joined to the server by a channel in memory.
func connect(t *testing.T, m *MCPServer) *sdk.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	serverSide, clientSide := sdk.NewInMemoryTransports()
	if _, err := m.Server().Connect(ctx, serverSide, nil); err != nil {
		t.Fatal(err)
	}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, clientSide, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func mcpCall(t *testing.T, s *sdk.ClientSession, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := s.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result
}

func textOfResult(t *testing.T, r *sdk.CallToolResult) string {
	t.Helper()
	if len(r.Content) != 1 {
		t.Fatalf("%d parts of content", len(r.Content))
	}
	text, ok := r.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T", r.Content[0])
	}
	return text.Text
}

func TestEveryMessageOfEveryAgentIsATool(t *testing.T) {
	m := newMCP(t, nil, newFake("Bob", defaultSkills...), newFake("Carol", Skill{ID: "echo", Params: []string{"text"}}))
	session := connect(t, m)
	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*sdk.Tool{}
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
	}
	if len(byName) != len(defaultSkills)+1 {
		t.Fatalf("%d tools: %v", len(byName), m.ToolNames())
	}
	echo := byName["Bob__echo"]
	if echo == nil || byName["Carol__echo"] == nil {
		t.Fatalf("tools = %v", m.ToolNames())
	}
	if !strings.Contains(echo.Description, "repeats the text Takes: text.") || !strings.Contains(echo.Description, "Agent Bob: Goal of Bob") {
		t.Errorf("description = %q", echo.Description)
	}
	schema, _ := json.Marshal(echo.InputSchema)
	for _, want := range []string{`"type":"object"`, `"required":["text"]`, `"additionalProperties":false`, `"text":{}`} {
		if !strings.Contains(string(schema), want) {
			t.Errorf("missing %s in the schema %s", want, schema)
		}
	}
	none, _ := json.Marshal(byName["Bob__count"].InputSchema)
	if strings.Contains(string(none), "required") {
		t.Errorf("a message without values requires some: %s", none)
	}
}

func TestToolNamesUseOnlyWhatAToolNameMayHaveAndAreNeverTheSame(t *testing.T) {
	long := strings.Repeat("a", 70)
	agents := []Agent{
		newFake("Café", Skill{ID: "x"}), newFake("Caf_", Skill{ID: "x"}), newFake("Cafe", Skill{ID: "x"}),
		newFake(long, Skill{ID: "ask"}), newFake(long+"b", Skill{ID: "ask"}),
	}
	valid := regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	seen := map[string]bool{}
	for _, tool := range planTools(agents) {
		if !valid.MatchString(tool.name) {
			t.Errorf("%q is not a name a tool may have", tool.name)
		}
		if seen[tool.name] {
			t.Errorf("%q twice", tool.name)
		}
		seen[tool.name] = true
	}
	if len(seen) != 5 {
		t.Errorf("%d names for 5 messages", len(seen))
	}
}

func TestACallRunsTheAgentAndAnswersWithText(t *testing.T) {
	session := connect(t, newMCP(t, nil))
	r := mcpCall(t, session, "Bob__echo", map[string]any{"text": "hi"})
	if r.IsError || textOfResult(t, r) != "echo: hi" {
		t.Errorf("result = %+v", r)
	}
}

func TestARecordIsAnsweredAsJSONSoItsFieldsCanBeRead(t *testing.T) {
	session := connect(t, newMCP(t, nil))
	r := mcpCall(t, session, "Bob__record", nil)
	var got map[string]any
	if err := json.Unmarshal([]byte(textOfResult(t, r)), &got); err != nil || got["a"] != "x" || got["n"] != float64(2) {
		t.Errorf("got %v, err %v", got, err)
	}
}

// req: S7
func TestAConversationBelongsToASessionAndRemembersOnlyWithinIt(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	m := newMCP(t, nil, bob)
	one, other := connect(t, m), connect(t, m)
	counts := []string{
		textOfResult(t, mcpCall(t, one, "Bob__count", nil)),
		textOfResult(t, mcpCall(t, one, "Bob__count", nil)),
		textOfResult(t, mcpCall(t, other, "Bob__count", nil)),
		textOfResult(t, mcpCall(t, one, "Bob__count", nil)),
	}
	if strings.Join(counts, " ") != "1 2 1 3" {
		t.Errorf("counts = %v: what one session knows reached the other, or was lost", counts)
	}
	m.Close()
	if _, closed := bob.counts(); closed != 2 {
		t.Errorf("%d conversations were let go, want 2", closed)
	}
}

// req: P1
func TestAFailureIsAnErrorOfTheToolWithoutThePlaceItCameFrom(t *testing.T) {
	session := connect(t, newMCP(t, nil))
	r := mcpCall(t, session, "Bob__fail", nil)
	text := textOfResult(t, r)
	if !r.IsError || !strings.Contains(text, "the order is missing") || !strings.Contains(text, "add the order") {
		t.Errorf("result = %+v %q", r, text)
	}
	for _, leak := range []string{"/home/secret", "bob.ag", "secret line", "other.ag"} {
		if strings.Contains(text, leak) {
			t.Errorf("the answer tells %q: %s", leak, text)
		}
	}
}

// req: P4, P2
func TestAPanicInAnAgentIsAnErrorOfTheToolAndTheServerGoesOn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	m := newMCP(t, func(c *MCPConfig) { c.Log = applog.New(dir) })
	session := connect(t, m)
	r := mcpCall(t, session, "Bob__boom", nil)
	if !r.IsError || textOfResult(t, r) != "something went wrong inside the server" {
		t.Errorf("result = %+v %q", r, textOfResult(t, r))
	}
	raw, err := os.ReadFile(filepath.Join(dir, "metagente.log"))
	if err != nil || !strings.Contains(string(raw), "PANIC mcp Bob.boom: kaboom") {
		t.Errorf("the cause is not in the log: %v\n%s", err, raw)
	}
	if again := mcpCall(t, session, "Bob__echo", map[string]any{"text": "still"}); textOfResult(t, again) != "echo: still" {
		t.Error("the server did not go on")
	}
}

func TestACallThatTakesTooLongSaysSo(t *testing.T) {
	session := connect(t, newMCP(t, func(c *MCPConfig) { c.RequestTimeout = 40 * time.Millisecond }))
	r := mcpCall(t, session, "Bob__slow", nil)
	if !r.IsError || textOfResult(t, r) != "the agent took too long to answer" {
		t.Errorf("result = %+v %q", r, textOfResult(t, r))
	}
}

// req: S6
func TestMCPCallsAreRefusedWhenTheServerIsBusy(t *testing.T) {
	session := connect(t, newMCP(t, func(c *MCPConfig) { c.MaxInFlight = 1 }))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = session.CallTool(ctx, &sdk.CallToolParams{Name: "Bob__slow"})
	}()
	deadline := time.Now().Add(3 * time.Second)
	var busy *sdk.CallToolResult
	for time.Now().Before(deadline) {
		r := mcpCall(t, session, "Bob__echo", map[string]any{"text": "x"})
		if r.IsError {
			busy = r
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if busy == nil || !strings.Contains(textOfResult(t, busy), "busy") {
		t.Errorf("the server never said it was busy: %+v", busy)
	}
}

func TestTheValuesOfACallAreCheckedLikeAnyOtherRequest(t *testing.T) {
	session := connect(t, newMCP(t, nil))
	tooMany := map[string]any{}
	for i := 0; i < 40; i++ {
		tooMany["k"+strings.Repeat("a", i)] = i
	}
	for name, tt := range map[string]struct {
		args map[string]any
		want string
	}{
		"a bad name":    {map[string]any{"te xt": "x"}, "name that is not valid"},
		"too many":      {tooMany, "too many values"},
		"an odd letter": {map[string]any{"tëxt": "x"}, ""},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		r, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "Bob__echo", Arguments: tt.args})
		cancel()
		if tt.want == "" {
			continue // letters of any language are fine in a name
		}
		if err != nil || !r.IsError || !strings.Contains(textOfResult(t, r), tt.want) {
			t.Errorf("%s: %v %+v", name, err, r)
		}
	}
	// A call whose values are not an object: either the SDK refuses it or we do.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "Bob__echo", Arguments: []any{1, 2}})
	if err == nil && (r == nil || !r.IsError) {
		t.Errorf("values that are not an object were accepted: %+v", r)
	}
}

// req: S9
func TestMCPHasALimitOfConversationsAndSaysSo(t *testing.T) {
	m := newMCP(t, func(c *MCPConfig) { c.MaxConversations = 1 }, newFake("Bob", defaultSkills...), newFake("Carol", defaultSkills...))
	session := connect(t, m)
	if r := mcpCall(t, session, "Bob__count", nil); r.IsError {
		t.Fatalf("the first conversation: %+v", r)
	}
	r := mcpCall(t, session, "Carol__count", nil)
	if !r.IsError || !strings.Contains(textOfResult(t, r), "as many conversations as it may") {
		t.Errorf("result = %+v %q", r, textOfResult(t, r))
	}
}

func TestAnAgentThatCannotStartLeavesNothingBehind(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	bob.startErr = diag.New("the start section failed").Fix("fix it")
	m := newMCP(t, nil, bob)
	r := mcpCall(t, connect(t, m), "Bob__echo", map[string]any{"text": "x"})
	if !r.IsError || !strings.Contains(textOfResult(t, r), "the start section failed") {
		t.Errorf("result = %+v %q", r, textOfResult(t, r))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.slots) != 0 {
		t.Errorf("%d conversations were kept", len(m.slots))
	}
}

func TestThereHasToBeAnAgentToServe(t *testing.T) {
	if _, err := NewMCP(MCPConfig{}, nil); err == nil {
		t.Error("a server without agents was made")
	}
}

func TestAClientThatClosesItsStreamsEndsTheSessionWithoutAFailure(t *testing.T) {
	m := newMCP(t, nil)
	reader, writer := io.Pipe()
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- m.RunIO(context.Background(), reader, &out) }()
	_ = writer.Close() // the program that started the server went away
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a client that left was a failure: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not end when its input did")
	}
}

func TestAServerStoppedFromOutsideEndsToo(t *testing.T) {
	m := newMCP(t, nil)
	reader, writer := io.Pipe()
	defer writer.Close()
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.RunIO(ctx, reader, io.Discard) }()
	time.Sleep(20 * time.Millisecond)
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
}

// req: D2
func TestAnMCPCallCarriesTheChainOfAgentsAndIsStoppedInACircleOrTooDeep(t *testing.T) {
	session := connect(t, newMCP(t, func(c *MCPConfig) { c.MaxCallDepth = 3 }))
	callWith := func(chain any) *sdk.CallToolResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		r, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "Bob__chain", Meta: sdk.Meta{mcpChainMeta: chain}})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if got := textOfResult(t, callWith([]any{"Alice", "Carol"})); got != "Alice>Carol" {
		t.Errorf("the agent was told the chain %q", got)
	}
	for name, tt := range map[string]struct {
		chain any
		want  string
	}{
		"circle":    {[]any{"Alice", "Bob"}, "in a circle"},
		"too deep":  {[]any{"A", "B", "C"}, "too deep"},
		"not names": {[]any{"has space"}, "chain of agents is not valid"},
		"not list":  {"Alice", "chain of agents is not valid"},
	} {
		r := callWith(tt.chain)
		if !r.IsError || !strings.Contains(textOfResult(t, r), tt.want) {
			t.Errorf("%s: %+v %q", name, r, textOfResult(t, r))
		}
	}
	// A client that says nothing has an empty chain.
	if got := textOfResult(t, mcpCall(t, session, "Bob__chain", nil)); got != "" {
		t.Errorf("a call without a chain was told %q", got)
	}
}
