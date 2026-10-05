package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// listenMCP starts a server that serves MCP too, on a real port, and keeps its
// access log.
func listenMCP(t *testing.T, tweak func(*Config), agents ...Agent) (base string, server *Server, log *syncBuffer) {
	t.Helper()
	httpServer := httptest.NewUnstartedServer(nil)
	address := httpServer.Listener.Addr().String()
	// The Host of `do` too, so a test can also call the server in the same process.
	cfg := Config{Token: goodToken, Hosts: []string{address, "127.0.0.1:8080"}, Version: "1.2.3", MCP: true}
	if tweak != nil {
		tweak(&cfg)
	}
	if len(agents) == 0 {
		agents = []Agent{newFake("Bob", defaultSkills...)}
	}
	server, err := New(cfg, agents)
	if err != nil {
		t.Fatal(err)
	}
	log = &syncBuffer{}
	httpServer.Config.Handler = accessLog(server, jsonLogger(log), time.Now)
	httpServer.Start()
	t.Cleanup(func() {
		server.Close()
		httpServer.CloseClientConnections()
		httpServer.Close()
	})
	return "http://" + address, server, log
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// withToken sends the token with every request, as a client that was given it does.
type withToken struct{ token string }

func (w withToken) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if w.token != "" {
		r.Header.Set("Authorization", "Bearer "+w.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// connectHTTP is a client of the SDK that speaks the streamable HTTP of MCP to the
// server, with the token. It is closed at the end of the test.
func connectHTTP(t *testing.T, base, token string) (*sdk.ClientSession, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &sdk.StreamableClientTransport{
		Endpoint:   base + MCPPath,
		HTTPClient: &http.Client{Transport: withToken{token}},
		MaxRetries: -1,
	}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		return nil, err
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, nil
}

func mustConnectHTTP(t *testing.T, base string) *sdk.ClientSession {
	t.Helper()
	session, err := connectHTTP(t, base, goodToken)
	if err != nil {
		t.Fatal(err)
	}
	return session
}

// eventually waits for a condition that another goroutine makes true.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("never: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

const initializeBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func mcpRequest(mutate func(*http.Request)) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("Accept", "application/json, text/event-stream")
		if mutate != nil {
			mutate(r)
		}
	}
}

// req: S10
func TestAClientOfTheSDKUsesTheAgentsOverHTTPAndASessionIsAConversation(t *testing.T) {
	base, _, _ := listenMCP(t, nil, newFake("Bob", defaultSkills...), newFake("Carol", Skill{ID: "echo", Params: []string{"text"}}))
	session := mustConnectHTTP(t, base)

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != len(defaultSkills)+1 {
		t.Errorf("%d tools", len(listed.Tools))
	}
	if got := textOfResult(t, mcpCall(t, session, "Carol__echo", map[string]any{"text": "hi"})); got != "echo: hi" {
		t.Errorf("Carol__echo = %q", got)
	}
	for _, want := range []string{"1", "2"} {
		if got := textOfResult(t, mcpCall(t, session, "Bob__count", nil)); got != want {
			t.Errorf("Bob__count = %q, want %q", got, want)
		}
	}
	other := mustConnectHTTP(t, base)
	if got := textOfResult(t, mcpCall(t, other, "Bob__count", nil)); got != "1" {
		t.Errorf("another session knew what the first one kept: %q", got)
	}
}

// req: S10, S1
func TestAnMCPClientWithoutTheTokenIsRefused(t *testing.T) {
	base, _, _ := listenMCP(t, nil)
	if _, err := connectHTTP(t, base, ""); err == nil {
		t.Error("a client without the token was answered")
	}
	if _, err := connectHTTP(t, base, goodToken+"x"); err == nil {
		t.Error("a client with a wrong token was answered")
	}
}

// req: S10, S1, S2, S3, S4, S6
func TestTheDoorOfMCPIsTheDoorOfA2A(t *testing.T) {
	s := newTestServer(t, func(c *Config) { c.MCP = true; c.MaxBody = 1024 })
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		mutate func(*http.Request)
		want   int
	}{
		{"no token", http.MethodPost, MCPPath, initializeBody, func(r *http.Request) { r.Header.Del("Authorization") }, http.StatusUnauthorized},
		{"a wrong token", http.MethodPost, MCPPath, initializeBody, func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, http.StatusUnauthorized},
		{"another host", http.MethodPost, MCPPath, initializeBody, func(r *http.Request) { r.Host = "evil.example" }, http.StatusMisdirectedRequest},
		{"a page in a browser", http.MethodPost, MCPPath, initializeBody, func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, http.StatusForbidden},
		{"the fetch of a browser", http.MethodPost, MCPPath, initializeBody, func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, http.StatusForbidden},
		{"a stream of the server", http.MethodGet, MCPPath, "", func(r *http.Request) { r.Header.Set("Accept", "text/event-stream") }, http.StatusMethodNotAllowed},
		{"another method", http.MethodPut, MCPPath, initializeBody, nil, http.StatusMethodNotAllowed},
		{"a query", http.MethodPost, MCPPath + "?x=1", initializeBody, nil, http.StatusBadRequest},
		{"not JSON", http.MethodPost, MCPPath, initializeBody, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		{"too large", http.MethodPost, MCPPath, strings.Repeat(" ", 2048) + initializeBody, nil, http.StatusRequestEntityTooLarge},
		{"another path", http.MethodPost, MCPPath + "/", initializeBody, nil, http.StatusNotFound},
		{"a session that was never issued", http.MethodPost, MCPPath, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, func(r *http.Request) { r.Header.Set("Mcp-Session-Id", "made-up") }, http.StatusNotFound},
		{"all is well", http.MethodPost, MCPPath, initializeBody, nil, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(s, c.method, c.path, c.body, mcpRequest(c.mutate))
			if rec.Code != c.want {
				t.Fatalf("HTTP %d, want %d: %s", rec.Code, c.want, rec.Body.String())
			}
			if c.want == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != "POST, DELETE" {
				t.Errorf("Allow = %q", rec.Header().Get("Allow"))
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != "" {
				t.Error("the server said that another site may call it")
			}
			if c.want == http.StatusOK && rec.Header().Get("Mcp-Session-Id") == "" {
				t.Error("the server did not issue the id of the session")
			}
		})
	}
}

func TestMCPIsNotServedUnlessAskedFor(t *testing.T) {
	s := newTestServer(t, nil)
	if s.ServesMCP() {
		t.Error("MCP is served without being asked for")
	}
	rec := do(s, http.MethodPost, MCPPath, initializeBody, mcpRequest(nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
}

// req: S10, S9
func TestASessionThatEndsLetsGoOfWhatItsAgentsKept(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	base, s, _ := listenMCP(t, nil, bob)
	session := mustConnectHTTP(t, base)
	mcpCall(t, session, "Bob__count", nil)
	if err := session.Close(); err != nil { // the client says that it is done: a DELETE
		t.Fatal(err)
	}
	eventually(t, "the conversation of the session is let go", func() bool {
		_, closed := bob.counts()
		return closed == 1 && len(s.room) == 0
	})
}

// req: S10, S9
func TestASessionThatIsNotUsedExpires(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	base, s, _ := listenMCP(t, func(c *Config) { c.ConversationTTL = 100 * time.Millisecond }, bob)
	session := mustConnectHTTP(t, base)
	mcpCall(t, session, "Bob__count", nil)
	eventually(t, "the session expires and its conversation is let go", func() bool {
		_, closed := bob.counts()
		return closed == 1 && len(s.room) == 0 && s.mcp.sessions() == 0
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "Bob__count"}); err == nil {
		t.Error("a session that expired was still answered")
	}
}

// req: S10, S6
func TestThereAreNoMoreSessionsThanConversations(t *testing.T) {
	base, _, _ := listenMCP(t, func(c *Config) { c.MaxConversations = 1 })
	mustConnectHTTP(t, base)
	if _, err := connectHTTP(t, base, goodToken); err == nil {
		t.Fatal("a second session was opened with room for one")
	}
	req, _ := http.NewRequest(http.MethodPost, base+MCPPath, strings.NewReader(initializeBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := (&http.Client{Transport: withToken{goodToken}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") == "" {
		t.Errorf("HTTP %d, Retry-After %q", resp.StatusCode, resp.Header.Get("Retry-After"))
	}
}

// req: S10, S6
func TestMCPAndA2AShareTheLimitOfConversations(t *testing.T) {
	base, s, _ := listenMCP(t, func(c *Config) { c.MaxConversations = 2 })
	if _, contextID := reply(t, send(t, s, message(dataPart("count", `{}`), ""))); contextID == "" {
		t.Fatal("no A2A conversation")
	}
	first := mustConnectHTTP(t, base)
	if r := mcpCall(t, first, "Bob__count", nil); r.IsError {
		t.Fatalf("the second conversation was refused: %q", textOfResult(t, r))
	}
	second := mustConnectHTTP(t, base)
	r := mcpCall(t, second, "Bob__count", nil)
	if !r.IsError || !strings.Contains(textOfResult(t, r), "as many conversations as it may") {
		t.Errorf("a third conversation, with room for two: %+v", r)
	}
}

// req: S10, S6
func TestMCPAndA2AShareTheLimitOfCallsAtTheSameTime(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	bob.hold = make(chan struct{})
	base, s, _ := listenMCP(t, func(c *Config) { c.MaxInFlight = 1 }, bob)
	session := mustConnectHTTP(t, base)
	done := make(chan struct{})
	go func() {
		defer close(done)
		send(t, s, message(dataPart("hold", `{}`), ""))
	}()
	eventually(t, "the A2A call takes the only place", func() bool { return bob.entered.Load() == 1 })
	r := mcpCall(t, session, "Bob__echo", map[string]any{"text": "x"})
	close(bob.hold)
	<-done
	if !r.IsError || !strings.Contains(textOfResult(t, r), "busy") {
		t.Errorf("an MCP call ran beside an A2A call with one place: %+v", r)
	}
}

// req: S10, P5
func TestTheAccessLogSaysWhatAnMCPCallRan(t *testing.T) {
	base, _, log := listenMCP(t, nil)
	session := mustConnectHTTP(t, base)
	mcpCall(t, session, "Bob__echo", map[string]any{"text": "a secret value"})
	_ = session.Close()

	var call map[string]any
	for _, line := range strings.Split(strings.TrimSpace(log.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("a line that is not an entry: %q", line)
		}
		if entry["path"] != MCPPath || entry["status"] == float64(http.StatusOK) && entry["rpc"] != "mcp" {
			t.Errorf("a request to MCP logged as %v", entry)
		}
		if entry["message"] == "echo" {
			call = entry
		}
	}
	if call == nil {
		t.Fatalf("the call is not in the log:\n%s", log.String())
	}
	if call["agent"] != "Bob" || call["result"] != resultOK || !strings.HasPrefix(call["task"].(string), "task-") {
		t.Errorf("the call was logged as %v", call)
	}
	for _, secret := range []string{goodToken, "a secret value"} {
		if strings.Contains(log.String(), secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
}

// req: S10
func TestStoppingTheServerEndsTheSessionsAndLetsGoOfTheirConversations(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	base, s, _ := listenMCP(t, nil, bob)
	session := mustConnectHTTP(t, base)
	mcpCall(t, session, "Bob__count", nil)
	s.Close()
	if _, closed := bob.counts(); closed != 1 {
		t.Errorf("%d conversations let go", closed)
	}
	if n := s.mcp.sessions(); n != 0 {
		t.Errorf("%d sessions left", n)
	}
	if len(s.room) != 0 {
		t.Errorf("%d places of conversations still taken", len(s.room))
	}
}

// req: S10
func TestStoppingTheServerDoesNotWaitForACallThatIsRunning(t *testing.T) {
	base, s, _ := listenMCP(t, func(c *Config) { c.RequestTimeout = time.Minute })
	session := mustConnectHTTP(t, base)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = session.CallTool(ctx, &sdk.CallToolParams{Name: "Bob__slow"})
	}()
	eventually(t, "the call is running", func() bool { return len(s.inflight) == 1 })
	stopped := make(chan struct{})
	go func() { s.Close(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stopping the server waited for the call")
	}
}
