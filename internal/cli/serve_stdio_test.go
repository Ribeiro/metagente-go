package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stdioServer is a `serve --stdio` whose input and output are pipes a client of the
// SDK is joined to.
type stdioServer struct {
	session *sdk.ClientSession
	done    chan int
	stderr  *bytes.Buffer
	cancel  context.CancelFunc
}

func startStdio(t *testing.T, args []string) *stdioServer {
	t.Helper()
	t.Setenv("METAGENTE_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	toServer, clientWrites := io.Pipe()
	clientReads, serverWrites := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	s := &stdioServer{done: make(chan int, 1), stderr: &bytes.Buffer{}, cancel: cancel}
	go func() {
		code := serveCommand(ctx, args, serverWrites, s.stderr, serveEnv{getenv: func(string) string { return "" }, stdin: toServer})
		_ = serverWrites.Close()
		s.done <- code
	}()
	transport := &sdk.IOTransport{Reader: clientReads, Writer: clientWrites}
	// If the server never answers, the join is given up after a while; the context
	// itself is not given a deadline, because the session lives as long as it does.
	giveUp := time.AfterFunc(10*time.Second, cancel)
	defer giveUp.Stop()
	session, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		cancel()
		t.Fatalf("the client could not join the server: %v\n%s", err, s.stderr.String())
	}
	s.session = session
	t.Cleanup(cancel)
	return s
}

// end closes the streams the way a client that goes away does, and returns the exit
// code and what the server said.
func (s *stdioServer) end(t *testing.T) (int, string) {
	t.Helper()
	_ = s.session.Close()
	select {
	case code := <-s.done:
		return code, s.stderr.String()
	case <-time.After(10 * time.Second):
		t.Fatal("serve --stdio did not end when the client left")
	}
	return 0, ""
}

func (s *stdioServer) call(t *testing.T, name string, args map[string]any) *sdk.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r, err := s.session.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return r
}

func resultText(t *testing.T, r *sdk.CallToolResult) string {
	t.Helper()
	if len(r.Content) != 1 {
		t.Fatalf("%d parts of content", len(r.Content))
	}
	return r.Content[0].(*sdk.TextContent).Text
}

func TestServeStdioOffersTheAgentsAsToolsAndNeedsNoToken(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	s := startStdio(t, []string{"hello.ag", "--stdio"})

	listed, err := s.session.ListTools(context.Background(), nil)
	if err != nil || len(listed.Tools) != 1 || listed.Tools[0].Name != "Hello__greet" {
		t.Fatalf("tools = %+v, err = %v", listed, err)
	}
	if r := s.call(t, "Hello__greet", map[string]any{"name": "Ana"}); r.IsError || resultText(t, r) != "Hello, Ana!" {
		t.Errorf("result = %+v", r)
	}

	code, stderr := s.end(t)
	if code != 0 {
		t.Errorf("exit code = %d, want 0 for a client that left", code)
	}
	assertContains(t, stderr, "Serving 1 tool over MCP on standard input and output.", "Hello__greet")
}

// req: P1
func TestServeStdioReportsAMistakeWithoutTheFileOrItsLines(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	s := startStdio(t, []string{"hello.ag", "--stdio"})
	r := s.call(t, "Hello__greet", map[string]any{}) // the value `name` is missing
	text := resultText(t, r)
	if !r.IsError || !strings.Contains(text, "needs a value for `name`") {
		t.Errorf("result = %+v %q", r, text)
	}
	for _, leak := range []string{"hello.ag", dir, "reply", "on greet"} {
		if strings.Contains(text, leak) {
			t.Errorf("the answer tells %q: %s", leak, text)
		}
	}
	s.end(t)
}

func TestServeStdioKeepsTheMemoryOfAConversationOnlyInItsSession(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "notes.ag", "agent Notes\n  goal \"Remember one thing\"\n  tool state\n  accepts remember what\n  accepts recall\n  on remember\n    state.set key: \"thing\" value: what\n    reply \"ok\"\n  on recall\n    reply state.get key: \"thing\"\n")
	s := startStdio(t, []string{"notes.ag", "--stdio"})
	if r := s.call(t, "Notes__remember", map[string]any{"what": "Ana"}); resultText(t, r) != "ok" {
		t.Fatalf("remember: %+v", r)
	}
	if r := s.call(t, "Notes__recall", nil); resultText(t, r) != "Ana" {
		t.Errorf("recall: %+v", r)
	}
	s.end(t)
}

func TestServeStdioRefusesTheOptionsOfTheNetwork(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	for _, option := range [][]string{
		{"--port", "9000"}, {"--bind", "127.0.0.1"}, {"--public"}, {"--behind-proxy"}, {"--public-card"},
		{"--tls-cert", "c.pem"}, {"--tls-key", "k.pem"}, {"--public-url", "https://a.example"}, {"--host", "a.example"},
	} {
		var errOut bytes.Buffer
		args := append([]string{"hello.ag", "--stdio"}, option...)
		code := serveCommand(context.Background(), args, io.Discard, &errOut, serveEnv{getenv: func(string) string { return "" }, stdin: strings.NewReader("")})
		if code != 2 || !strings.Contains(errOut.String(), option[0]+" does not apply to --stdio") {
			t.Errorf("%v: exit %d\n%s", option, code, errOut.String())
		}
	}
}

// req: T1
func TestServeStdioNeverApprovesAndNeverAsks(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "blocked.ag", "agent Blocked\n  goal \"x\"\n  remote Bob at \"http://127.0.0.1:1\"\n  accepts go\n  on go\n    reply \"hi\"\n")
	atTheKeyboard(t, "y\n")
	var out, errOut bytes.Buffer
	code := serveCommand(context.Background(), []string{"blocked.ag", "--stdio"}, &out, &errOut,
		serveEnv{getenv: func(string) string { return "" }, stdin: strings.NewReader("")})
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, errOut.String(), "have not approved", "metagente trust blocked.ag")
	if out.Len() != 0 {
		t.Errorf("something went to the standard output, which belongs to the protocol: %q", out.String())
	}
}

func TestServeStdioWritesNothingToTheOutputButTheProtocol(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	writeFile(t, dir, "metagente.toml", "[serve]\nallowed_origins = [\"https://example.com\"]\n")
	var out, errOut bytes.Buffer
	// A client that goes away at once: the server starts, says what it serves, and ends.
	code := serveCommand(context.Background(), []string{"hello.ag", "--stdio"}, &out, &errOut,
		serveEnv{getenv: func(string) string { return "" }, stdin: strings.NewReader("")})
	if code != 0 {
		t.Errorf("exit code = %d\n%s", code, errOut.String())
	}
	if out.Len() != 0 {
		t.Errorf("something other than the protocol was written to the output: %q", out.String())
	}
	assertContains(t, errOut.String(), "Serving 1 tool over MCP", "serve.allowed_origins is ignored")
}
