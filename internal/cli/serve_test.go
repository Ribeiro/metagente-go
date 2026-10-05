package cli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testToken = "kkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkkk"

const helloAgent = `agent Hello
  goal "Greets people"
  accepts greet name
  on greet
    reply "Hello, {name}!"
`

// liveServer is a `serve` that is running on a port the system chose.
type liveServer struct {
	address string
	token   string
	cancel  context.CancelFunc
	done    chan int
	stderr  *bytes.Buffer
}

func startServe(t *testing.T, args []string, env map[string]string, terminal bool) *liveServer {
	t.Helper()
	t.Setenv("METAGENTE_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	live := &liveServer{cancel: cancel, done: make(chan int, 1), stderr: &bytes.Buffer{}}
	ready := make(chan struct{})
	go func() {
		live.done <- serveCommand(ctx, args, io.Discard, live.stderr, serveEnv{
			getenv:   func(name string) string { return env[name] },
			terminal: terminal,
			ready:    func(address, token string) { live.address, live.token = address, token; close(ready) },
		})
	}()
	select {
	case <-ready:
	case code := <-live.done:
		t.Fatalf("serve ended with %d before it was ready:\n%s", code, live.stderr.String())
	case <-time.After(10 * time.Second):
		t.Fatal("serve was not ready in time")
	}
	return live
}

// stop ends the server the way Ctrl-C does and returns what it said.
func (l *liveServer) stop(t *testing.T) (code int, stderr string) {
	t.Helper()
	l.cancel()
	select {
	case code = <-l.done:
		return code, l.stderr.String()
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
	}
	return 0, ""
}

var client = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 10 * time.Second}

func (l *liveServer) do(t *testing.T, method, path, body, token string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://"+l.address+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(raw)
}

func greet(name string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"data":{"skill":"greet","arguments":{"name":"` + name + `"}}}]}}}`
}

// req: S1
func TestServeAnswersWithTheTokenAndNeverWithoutIt(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	live := startServe(t, []string{"hello.ag", "--port", "0"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)

	code, card := live.do(t, "GET", "/agents/Hello/.well-known/agent-card.json", "", testToken)
	if code != 200 || !strings.Contains(card, `"name":"Hello"`) || !strings.Contains(card, "Greets people") {
		t.Errorf("card: %d %s", code, card)
	}
	if code, _ := live.do(t, "GET", "/agents/Hello/.well-known/agent-card.json", "", ""); code != 401 {
		t.Errorf("the card without the token: %d", code)
	}
	if code, _ := live.do(t, "POST", "/agents/Hello", greet("Ana"), "not-the-token"); code != 401 {
		t.Errorf("a wrong token: %d", code)
	}
	code, answer := live.do(t, "POST", "/agents/Hello", greet("Ana"), testToken)
	if code != 200 || !strings.Contains(answer, "Hello, Ana!") {
		t.Errorf("answer: %d %s", code, answer)
	}

	exit, stderr := live.stop(t)
	if exit != 0 {
		t.Errorf("exit code = %d, want 0 for a server that was stopped", exit)
	}
	assertContains(t, stderr, "Serving 1 agent", "/agents/Hello", "Authorization: Bearer TOKEN",
		"method=GET path=/agents/Hello/.well-known/agent-card.json status=200", "method=POST path=/agents/Hello status=200",
		"status=401", "agent=Hello rpc=SendMessage message=greet task=task-", "result=ok")
	// A server on this computer with nothing in front of it is where the banner says it is.
	if strings.Contains(stderr, "Listening on") {
		t.Errorf("the banner says where it listens, and it is the address that it already shows:\n%s", stderr)
	}
	if strings.Contains(stderr, testToken) || strings.Contains(stderr, "not-the-token") {
		t.Errorf("a token reached the output:\n%s", stderr)
	}
	// Stopped means the port is free.
	if _, err := client.Get("http://" + live.address + "/"); err == nil {
		t.Error("the server still answers after it was stopped")
	}
}

// req: S1
func TestServeMakesAndShowsATokenOnlyForAPersonAtATerminalOnThisComputer(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	live := startServe(t, []string{"hello.ag", "--port", "0"}, nil, true)
	if len(live.token) < 43 {
		t.Fatalf("token = %q", live.token)
	}
	if code, answer := live.do(t, "POST", "/agents/Hello", greet("Bia"), live.token); code != 200 || !strings.Contains(answer, "Hello, Bia!") {
		t.Errorf("the token that was made does not open the door: %d %s", code, answer)
	}
	_, stderr := live.stop(t)
	assertContains(t, stderr, "Token for this run, kept nowhere else: "+live.token)

	// Nobody is at a terminal (a CI, a container): no token is made, and nothing starts.
	var out, errOut bytes.Buffer
	code := serveCommand(context.Background(), []string{"hello.ag", "--port", "0"}, &out, &errOut, serveEnv{getenv: func(string) string { return "" }})
	if code != 2 {
		t.Errorf("exit code = %d, want 2", code)
	}
	assertContains(t, errOut.String(), "METAGENTE_TOKEN is not set", "metagente token")
	if strings.Contains(errOut.String(), "Serving") {
		t.Error("a server started without a token")
	}
}

// req: S1
func TestServeRefusesATokenThatIsTooShortWithoutRepeatingIt(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	var errOut bytes.Buffer
	code := serveCommand(context.Background(), []string{"hello.ag", "--port", "0"}, io.Discard, &errOut,
		serveEnv{getenv: func(string) string { return "abc123short" }, terminal: true})
	if code != 2 {
		t.Errorf("exit code = %d", code)
	}
	assertContains(t, errOut.String(), "at least 32")
	if strings.Contains(errOut.String(), "abc123short") {
		t.Errorf("the token was repeated:\n%s", errOut.String())
	}
}

// req: T1
func TestServeNeverApprovesAndNeverAsks(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "blocked.ag", "agent Blocked\n  goal \"x\"\n  remote Bob at \"http://127.0.0.1:1\"\n  accepts go\n  on go\n    reply \"hi\"\n")
	atTheKeyboard(t, "y\n") // a person is there and would say yes, if asked
	var errOut bytes.Buffer
	ready := false
	code := serveCommand(context.Background(), []string{"blocked.ag", "--port", "0"}, io.Discard, &errOut, serveEnv{
		getenv: func(string) string { return testToken }, terminal: true,
		ready: func(string, string) { ready = true },
	})
	if code != 1 || ready {
		t.Errorf("exit code %d, ready %v", code, ready)
	}
	assertContains(t, errOut.String(), "have not approved", "connects to: http://127.0.0.1:1", "metagente trust blocked.ag")
}

func TestServeSaysWhatIsWrongWithTheOptions(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	for name, tt := range map[string]struct {
		args []string
		code int
		want string
	}{
		"no file":        {[]string{"--port", "0"}, 2, "needs the name of at least one file"},
		"no arguments":   {nil, 2, "needs the name of at least one file"},
		"unknown option": {[]string{"hello.ag", "--frobnicate"}, 2, "flag provided but not defined"},
		"bad port":       {[]string{"hello.ag", "--port", "abc"}, 2, "invalid value"},
		"huge port":      {[]string{"hello.ag", "--port", "70000"}, 2, "is not a port"},
		"public alone":   {[]string{"hello.ag", "--public"}, 2, "needs TLS of its own"},
		"open bind":      {[]string{"hello.ag", "--bind", "0.0.0.0"}, 2, "only listens beyond this computer when it is told to be public"},
		"proxy open":     {[]string{"hello.ag", "--behind-proxy", "--bind", "0.0.0.0", "--host", "a.example", "--public-url", "https://a.example"}, 2, "only counts when the server listens on this same computer"},
		"half a pair":    {[]string{"hello.ag", "--tls-cert", "c.pem"}, 2, "go together"},
		"no such file":   {[]string{"nope.ag"}, 1, "the file does not exist"},
		"no such agent":  {[]string{"hello.ag", "--agent", "Nobody"}, 1, "there is no agent called `Nobody`"},
	} {
		var errOut bytes.Buffer
		code := serveCommand(context.Background(), tt.args, io.Discard, &errOut, serveEnv{getenv: func(string) string { return testToken }, terminal: true})
		if code != tt.code || !strings.Contains(errOut.String(), tt.want) {
			t.Errorf("%s: exit %d, stderr:\n%s\nwant %d containing %q", name, code, errOut.String(), tt.code, tt.want)
		}
	}
}

func TestServeServesOnlyTheAgentsAskedFor(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "two.ag", "agent A\n  goal \"first\"\n  accepts ping\n  on ping\n    reply \"a\"\n\nagent B\n  goal \"second\"\n  accepts ping\n  on ping\n    reply \"b\"\n")
	live := startServe(t, []string{"two.ag", "--port", "0", "--agent", "B"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)
	ping := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","parts":[{"data":{"skill":"ping"}}]}}}`
	if code, answer := live.do(t, "POST", "/agents/B", ping, testToken); code != 200 || !strings.Contains(answer, `"text":"b"`) {
		t.Errorf("B: %d %s", code, answer)
	}
	if code, _ := live.do(t, "POST", "/agents/A", ping, testToken); code != 404 {
		t.Errorf("A was not asked for, and answered with %d", code)
	}
	_, stderr := live.stop(t)
	if strings.Contains(stderr, "  A ") || !strings.Contains(stderr, "/agents/B") {
		t.Errorf("the banner:\n%s", stderr)
	}
}

func TestServeTakesSeveralFilesAndOptionsInAnyOrder(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	writeFile(t, dir, "other.ag", "agent Other\n  goal \"x\"\n  accepts ping\n  on ping\n    reply \"pong\"\n")
	live := startServe(t, []string{"--port", "0", "hello.ag", "--quiet", "other.ag"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)
	for _, name := range []string{"Hello", "Other"} {
		if code, _ := live.do(t, "GET", "/agents/"+name+"/.well-known/agent-card.json", "", testToken); code != 200 {
			t.Errorf("%s: %d", name, code)
		}
	}
	_, stderr := live.stop(t)
	if strings.Contains(stderr, "method=GET") {
		t.Errorf("--quiet still logged the requests:\n%s", stderr)
	}
	assertContains(t, stderr, "Serving 2 agents")
}

func TestServeSaysThatItIgnoresTheOriginsOfAConfiguration(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	writeFile(t, dir, "metagente.toml", "[serve]\nallowed_origins = [\"https://example.com\"]\n")
	live := startServe(t, []string{"hello.ag", "--port", "0"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)
	_, stderr := live.stop(t)
	assertContains(t, stderr, "serve.allowed_origins is ignored", "never accepts a request made by a page in a browser")
}

// req: S5
func TestServePutsTheNamesOfAProxyInTheCardAndNothingElseGetsThrough(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	live := startServe(t, []string{"hello.ag", "--port", "0", "--behind-proxy", "--host", "agents.example.com", "--public-url", "https://agents.example.com"},
		map[string]string{"METAGENTE_TOKEN": testToken}, false)
	call := func(host string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, "http://"+live.address+"/agents/Hello/.well-known/agent-card.json", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host // what a proxy writes: the name clients use, not the address of this computer
		req.Header.Set("Authorization", "Bearer "+testToken)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	code, card := call("agents.example.com")
	if code != 200 || !strings.Contains(card, `"url":"https://agents.example.com/agents/Hello"`) {
		t.Errorf("through the proxy: %d %s", code, card)
	}
	// The way a person on this computer would reach it is not the way it is meant.
	if code, _ := call(live.address); code != http.StatusMisdirectedRequest {
		t.Errorf("a request with the host of the loopback: %d, want 421", code)
	}
	// Behind a proxy, five wrong tokens from anyone do not stop the others.
	for i := 0; i < 8; i++ {
		req, _ := http.NewRequest(http.MethodGet, "http://"+live.address+"/agents/Hello/.well-known/agent-card.json", nil)
		req.Host = "agents.example.com"
		req.Header.Set("Authorization", "Bearer wrong")
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
		}
	}
	if code, _ := call("agents.example.com"); code != 200 {
		t.Errorf("the right token was refused after wrong ones: %d", code)
	}
	_, stderr := live.stop(t)
	assertContains(t, stderr, "https://agents.example.com/agents/Hello",
		// where the proxy has to send the requests, and with which Host
		"Listening on "+live.address+", in plain HTTP, for the Host: agents.example.com",
		// the log tells the request that came through the proxy from the one that went around it
		"host=agents.example.com method=GET path=/agents/Hello/.well-known/agent-card.json status=200",
		"host="+live.address+" method=GET path=/agents/Hello/.well-known/agent-card.json status=421")
}
