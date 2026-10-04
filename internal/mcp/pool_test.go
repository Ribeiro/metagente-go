package mcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"metagente/internal/config"
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/tools"
	"metagente/internal/value"
)

// The tests start this very test binary as the tool server, so no other
// program is needed. When the variable below is set, the binary is the server.
const helperEnv = "MCPPOOL_SERVER"

// sleeperEnv makes the binary a program that only waits: the child of the tool server.
const sleeperEnv = "MCPPOOL_SLEEPER"

func TestMain(m *testing.M) {
	if os.Getenv(sleeperEnv) == "1" {
		time.Sleep(10 * time.Minute)
		os.Exit(0)
	}
	if os.Getenv(helperEnv) == "1" {
		for i, arg := range os.Args {
			if arg == "--marker" && i+1 < len(os.Args) {
				_ = os.WriteFile(os.Args[i+1], []byte("started"), 0o644)
			}
			if arg == "--child" && i+1 < len(os.Args) {
				startSleeper(os.Args[i+1])
			}
		}
		_ = newHelperServer().Run(context.Background(), &sdk.StdioTransport{})
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// startSleeper starts a program that outlives its parent unless something ends it, and writes
// its number in the file.
func startSleeper(pidFile string) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = []string{sleeperEnv + "=1"}
	if err := cmd.Start(); err != nil {
		return
	}
	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644)
}

type textIn struct {
	Text string `json:"text" jsonschema:"text to send back"`
}

type nameIn struct {
	Name string `json:"name" jsonschema:"name of an environment variable"`
}

type millisIn struct {
	Millis int `json:"millis" jsonschema:"how long to wait, in milliseconds"`
}

func reply(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}

func newHelperServer() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "helper", Version: "v0.0.1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "send the text back"},
		func(_ context.Context, _ *sdk.CallToolRequest, in textIn) (*sdk.CallToolResult, any, error) {
			return reply(in.Text), nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "env", Description: "read a variable of this process"},
		func(_ context.Context, _ *sdk.CallToolRequest, in nameIn) (*sdk.CallToolResult, any, error) {
			return reply(os.Getenv(in.Name)), nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "slow", Description: "wait, then answer"},
		func(ctx context.Context, _ *sdk.CallToolRequest, in millisIn) (*sdk.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.Millis) * time.Millisecond):
			case <-ctx.Done():
			}
			return reply("done"), nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "fail", Description: "always fails"},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "no luck"}}}, nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "pid", Description: "the number of this process"},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return reply(strconv.Itoa(os.Getpid())), nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "peek", Description: "only reads", Annotations: &sdk.ToolAnnotations{ReadOnlyHint: true}},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			return reply("peeked"), nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "die", Description: "ends this process"},
		func(context.Context, *sdk.CallToolRequest, struct{}) (*sdk.CallToolResult, any, error) {
			os.Exit(3)
			return nil, nil, nil
		})
	return server
}

func helperSpec() Spec {
	return Spec{Command: `"` + os.Args[0] + `"`, Env: []string{helperEnv}}
}

func newPool(t *testing.T, tweak func(*Options)) (*Pool, Spec) {
	t.Helper()
	t.Setenv(helperEnv, "1")
	opts := Options{
		Root:   t.TempDir(),
		Limits: config.Default().Limits,
		Hidden: []string{"ANTHROPIC_API_KEY"},
		Allow:  func(Spec) error { return nil },
	}
	if tweak != nil {
		tweak(&opts)
	}
	pool := NewPool(opts)
	t.Cleanup(func() { _ = pool.Close() })
	return pool, helperSpec()
}

func call(t *testing.T, tool tools.Tool, action string, pairs ...string) (value.Value, error) {
	t.Helper()
	args := tools.Args{}
	for i := 0; i+1 < len(pairs); i += 2 {
		args[pairs[i]] = value.Text(pairs[i+1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return tool.Call(ctx, action, args)
}

func mustWork(t *testing.T, tool tools.Tool, action string, pairs ...string) string {
	t.Helper()
	got, err := call(t, tool, action, pairs...)
	if err != nil {
		t.Fatalf("%s: %s", action, rendered(t, err))
	}
	return got.Text
}

func rendered(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a problem")
	}
	d, ok := diag.From(err)
	if !ok {
		return err.Error()
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

func TestAToolServerAnswersThroughThePool(t *testing.T) {
	pool, spec := newPool(t, nil)
	srv := pool.Tool("srv", spec)
	if got := mustWork(t, srv, "echo", "text", "hi"); got != "hi" {
		t.Errorf("echo returned %q", got)
	}

	actions, err := srv.Actions(context.Background())
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	var names []string
	for _, a := range actions {
		names = append(names, a.Name)
		if a.Name == "echo" && (len(a.Params) != 1 || a.Params[0].Name != "text" || !a.Params[0].Required) {
			t.Errorf("the parameters of echo are %+v", a.Params)
		}
	}
	if got := strings.Join(names, " "); got != "die echo env fail peek pid slow" {
		t.Errorf("actions: %s", got)
	}
}

// req: T1
func TestNothingStartsWithoutApproval(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "started")
	approved := false
	pool, spec := newPool(t, func(o *Options) {
		o.Allow = func(Spec) error {
			if !approved {
				return diag.New("this server was not approved")
			}
			return nil
		}
	})
	spec.Command += ` --marker "` + marker + `"`
	srv := pool.Tool("srv", spec)

	_, err := call(t, srv, "echo", "text", "hi")
	mustContain(t, rendered(t, err), "this server was not approved")
	time.Sleep(300 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the program was started although it was not approved")
	}

	approved = true
	if got := mustWork(t, srv, "echo", "text", "hi"); got != "hi" {
		t.Errorf("echo returned %q", got)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Error("the program did not start once it was approved")
	}
}

func TestAPoolThatWasNotSetUpRefusesEverything(t *testing.T) {
	pool := NewPool(Options{Root: t.TempDir(), Limits: config.Default().Limits})
	defer pool.Close()
	_, err := call(t, pool.Tool("srv", helperSpec()), "echo", "text", "hi")
	mustContain(t, rendered(t, err), "not set up to use tool servers")
}

// req: E1
func TestTheProgramSeesOnlyTheMinimalEnvironment(t *testing.T) {
	t.Setenv("MCPPOOL_SECRET", "must-not-leak")
	t.Setenv("MCPPOOL_PASSED", "yes")
	pool, spec := newPool(t, nil)
	spec.Env = append(spec.Env, "MCPPOOL_PASSED")
	srv := pool.Tool("srv", spec)

	for _, tt := range []struct{ name, want string }{
		{"MCPPOOL_PASSED", "yes"}, // named in the agent file
		{"MCPPOOL_SECRET", ""},    // not named
	} {
		if got := mustWork(t, srv, "env", "name", tt.name); got != tt.want {
			t.Errorf("%s = %q, want %q", tt.name, got, tt.want)
		}
	}
	if got := mustWork(t, srv, "env", "name", "PATH"); got == "" {
		t.Error("the program did not receive PATH, so it could not find other programs")
	}
}

// req: E1, E6
func TestASecretCannotBePassedEvenWhenItIsNamed(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_API_KEY", "METAGENTE_TOKEN", "metagente_anything"} {
		pool, spec := newPool(t, nil)
		spec.Env = append(spec.Env, name)
		_, err := call(t, pool.Tool("srv", spec), "echo", "text", "hi")
		mustContain(t, rendered(t, err), "`"+name+"` holds a secret")
	}
}

// req: E3
func TestCallsOverlapUpToTheLimit(t *testing.T) {
	run := func(limit, calls int) time.Duration {
		pool, spec := newPool(t, func(o *Options) { o.Limits.MaxMCPCalls = limit })
		srv := pool.Tool("srv", spec)
		mustWork(t, srv, "echo", "text", "warm up") // start the program before timing

		start := time.Now()
		var wg sync.WaitGroup
		errs := make(chan error, calls)
		for i := 0; i < calls; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := srv.Call(context.Background(), "slow", tools.Args{"millis": value.Number(300)}); err != nil {
					errs <- err
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Error(rendered(t, err))
		}
		return time.Since(start)
	}
	limited := run(2, 4)
	free := run(8, 4)
	t.Logf("4 calls of 300 ms: %v with a limit of 2, %v with a limit of 8", limited, free)
	if limited < 550*time.Millisecond {
		t.Errorf("the limit of 2 was not respected: the calls took %v", limited)
	}
	if free > 900*time.Millisecond {
		t.Errorf("the calls did not run at the same time: they took %v", free)
	}
}

// req: E3
func TestTheLimitOfCallsIsForEachServerAndNotForAllOfThem(t *testing.T) {
	t.Setenv("MCPPOOL_PASSED", "yes")
	pool, first := newPool(t, func(o *Options) { o.Limits.MaxMCPCalls = 2 })
	// The same program with another set of variables is another server, with its own places.
	second := first
	second.Env = append(append([]string(nil), first.Env...), "MCPPOOL_PASSED")
	a, b := pool.Tool("a", first), pool.Tool("b", second)
	mustWork(t, a, "echo", "text", "start a")
	mustWork(t, b, "echo", "text", "start b")

	start := time.Now()
	var wg sync.WaitGroup
	for _, srv := range []tools.Tool{a, b} {
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := srv.Call(context.Background(), "slow", tools.Args{"millis": value.Number(300)}); err != nil {
					t.Error(rendered(t, err))
				}
			}()
		}
	}
	wg.Wait()
	took := time.Since(start)
	t.Logf("2 servers, 4 calls of 300 ms each, a limit of 2 for each: %v", took)
	if took < 550*time.Millisecond {
		t.Errorf("the limit of 2 was not respected on each server: %v", took)
	}
	if took > 1000*time.Millisecond {
		t.Errorf("the two servers held each other back, as if the limit were for both: %v", took)
	}
}

func TestAnUnknownActionListsWhatTheServerCanDo(t *testing.T) {
	pool, spec := newPool(t, nil)
	_, err := call(t, pool.Tool("srv", spec), "ecco", "text", "hi")
	mustContain(t, rendered(t, err), "`srv` has no action called `ecco`", "did you mean `srv.echo`?", "die, echo, env, fail, peek, pid, slow")
}

func TestAnAnswerLargerThanTheLimitIsRefused(t *testing.T) {
	pool, spec := newPool(t, func(o *Options) { o.Limits.MaxMCPResultBytes = 10 })
	srv := pool.Tool("srv", spec)
	_, err := call(t, srv, "echo", "text", strings.Repeat("x", 100))
	mustContain(t, rendered(t, err), "the answer of `srv.echo` is larger than the limit of 10 bytes", "max_mcp_result_bytes")
	if got := mustWork(t, srv, "echo", "text", "short"); got != "short" {
		t.Errorf("an answer under the limit came back as %q", got)
	}
}

func TestAnErrorOfTheToolIsExplained(t *testing.T) {
	pool, spec := newPool(t, nil)
	_, err := call(t, pool.Tool("srv", spec), "fail")
	mustContain(t, rendered(t, err), "`srv.fail` failed: no luck")
}

// req: E4
func TestClosingThePoolEndsTheProgram(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this test looks for the process with a signal, which Windows does not have")
	}
	pool, spec := newPool(t, nil)
	srv := pool.Tool("srv", spec)
	pid, err := strconv.Atoi(mustWork(t, srv, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if process.Signal(syscall.Signal(0)) != nil {
			_, err := call(t, srv, "echo", "text", "hi")
			mustContain(t, rendered(t, err), "already shut down")
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Errorf("the program %d was still running 5 seconds after the pool was closed", pid)
}

func TestAServerThatStoppedIsStartedAgain(t *testing.T) {
	pool, spec := newPool(t, nil)
	srv := pool.Tool("srv", spec)
	first := mustWork(t, srv, "pid")

	_, err := call(t, srv, "die")
	if err == nil {
		t.Fatal("a call that ends the server must report a problem")
	}
	t.Logf("the call that ended the server said: %s", strings.TrimSpace(rendered(t, err)))

	second := mustWork(t, srv, "pid")
	if first == second {
		t.Errorf("the same process answered after it ended (%s)", first)
	}
}

func TestAServerThatIsAnAddressIsReachedOverHTTP(t *testing.T) {
	server := newHelperServer()
	httpServer := httptest.NewServer(sdk.NewStreamableHTTPHandler(
		func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{}))
	// Cleanups run last in, first out: the pool (registered below) closes its
	// session first, and only then is the HTTP server closed. Closing the server
	// first would wait for the open stream of the session.
	t.Cleanup(func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
	})

	pool, _ := newPool(t, nil)
	srv := pool.Tool("srv", Spec{Command: httpServer.URL})
	if got := mustWork(t, srv, "echo", "text", "over http"); got != "over http" {
		t.Errorf("echo returned %q", got)
	}
}

// ---------- no process needed ----------

func TestChildEnvHasTheMinimalSetAndWhatWasNamed(t *testing.T) {
	values := map[string]string{
		"PATH": "/bin", "HOME": "/home/x", "LANG": "C",
		"SECRET": "s", "WANTED": "w", "ANTHROPIC_API_KEY": "k",
	}
	getenv := func(name string) string { return values[name] }
	env, err := ChildEnv([]string{"WANTED", "MISSING"}, []string{"ANTHROPIC_API_KEY"}, getenv)
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	want := "HOME=/home/x LANG=C PATH=/bin WANTED=w"
	if got := strings.Join(env, " "); got != want {
		t.Errorf("environment: %s, want %s", got, want)
	}
}

func TestChildEnvRefusesSecretsByNameWhateverTheCase(t *testing.T) {
	getenv := func(string) string { return "x" }
	for _, name := range []string{"ANTHROPIC_API_KEY", "anthropic_api_key", "METAGENTE_TOKEN", "Metagente_Other"} {
		if _, err := ChildEnv([]string{name}, []string{"ANTHROPIC_API_KEY"}, getenv); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestTheParametersComeFromTheSchemaTheServerPublished(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"b": map[string]any{}, "a": map[string]any{}},
		"required":   []any{"a"},
	}
	got := paramsOf(schema)
	want := []lang.ParamInfo{{Name: "a", Required: true}, {Name: "b", Required: false}}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("paramsOf = %+v, want %+v", got, want)
	}
	if len(paramsOf(nil)) != 0 || len(paramsOf("not a schema")) != 0 {
		t.Error("a schema that says nothing must give no parameters")
	}
}

// req: L7
func TestToolsPublishTheirSchemaAndAreTakenAsChangingThingsUnlessTheySayOtherwise(t *testing.T) {
	pool, spec := newPool(t, nil)
	actions, err := pool.Tool("srv", spec).Actions(context.Background())
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	byName := map[string]lang.ActionInfo{}
	for _, a := range actions {
		byName[a.Name] = a
	}
	if byName["echo"].Schema == nil {
		t.Error("the schema the server published was not kept")
	}
	if !byName["echo"].Mutates {
		t.Error("a tool that does not say it only reads must be taken as one that changes things")
	}
	if byName["peek"].Mutates {
		t.Error("a tool that says it only reads was taken as one that changes things")
	}
}

// ---------- credentials (E5) ----------

// protectedServer is a tool server that is an address and wants a bearer token.
func protectedServer(t *testing.T, token string) (url string, refused, reached *atomic.Int32) {
	t.Helper()
	refused, reached = new(atomic.Int32), new(atomic.Int32)
	server := newHelperServer()
	inner := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		if r.Header.Get("Authorization") != "Bearer "+token {
			refused.Add(1)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
	})
	return httpServer.URL, refused, reached
}

// req: E5
func TestABearerTokenIsSentToAToolServerThatIsAnAddress(t *testing.T) {
	const token = "tok-live-0123456789"
	address, refused, _ := protectedServer(t, token)
	pool, _ := newPool(t, func(o *Options) {
		o.Getenv = func(name string) string {
			if name == "SRV_TOKEN" {
				return token
			}
			return os.Getenv(name)
		}
	})
	srv := pool.Tool("srv", Spec{Command: address, Credential: "SRV_TOKEN"})
	if got := mustWork(t, srv, "echo", "text", "authorised"); got != "authorised" {
		t.Errorf("echo returned %q", got)
	}
	if refused.Load() != 0 {
		t.Errorf("%d requests went without the token", refused.Load())
	}
}

func TestWithoutACredentialAProtectedServerRefusesAndTheTokenIsNotInTheError(t *testing.T) {
	const token = "tok-live-0123456789"
	address, refused, _ := protectedServer(t, token)
	pool, _ := newPool(t, nil)
	_, err := call(t, pool.Tool("srv", Spec{Command: address}), "echo", "text", "x")
	text := rendered(t, err)
	// With the newer protocol there is no handshake, so the refusal may only show
	// at the first question to the server instead of at the connection.
	if !strings.Contains(text, "I could not reach the tool server") && !strings.Contains(text, "did not say what it can do") {
		t.Errorf("the refusal was not explained:\n%s", text)
	}
	if refused.Load() == 0 {
		t.Error("the server was never refused, so the test proves nothing")
	}
	if strings.Contains(text, token) {
		t.Errorf("the token is in the error:\n%s", text)
	}
}

// req: E5
func TestACredentialWhoseVariableIsEmptyStopsBeforeAnythingIsSent(t *testing.T) {
	address, _, reached := protectedServer(t, "tok-live-0123456789")
	pool, _ := newPool(t, func(o *Options) { o.Getenv = func(string) string { return "" } })
	_, err := call(t, pool.Tool("srv", Spec{Command: address, Credential: "SRV_TOKEN"}), "echo", "text", "x")
	mustContain(t, rendered(t, err), "the credential for the tool server", "the variable SRV_TOKEN is empty", "export SRV_TOKEN")
	if reached.Load() != 0 {
		t.Error("the server was reached without the credential")
	}
}

// req: E5
func TestTheTokenIsAddedOnlyToTheHostOfTheServer(t *testing.T) {
	var seenHere, seenElsewhere string
	here := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHere = r.Header.Get("Authorization")
	}))
	defer here.Close()
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenElsewhere = r.Header.Get("Authorization")
	}))
	defer elsewhere.Close()

	hostOf := func(u string) string { return strings.TrimPrefix(u, "http://") }
	transport := (&bearerTransport{host: hostOf(here.URL), token: "tok-live-0123456789"}).wrap(&http.Transport{})
	client := &http.Client{Transport: transport}
	for _, u := range []string{here.URL, elsewhere.URL} {
		resp, err := client.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if seenHere != "Bearer tok-live-0123456789" {
		t.Errorf("the host of the server got %q", seenHere)
	}
	if seenElsewhere != "" {
		t.Errorf("another host got the token: %q", seenElsewhere)
	}
}

// req: E5
func TestAProgramNeverGetsAToken(t *testing.T) {
	pool, spec := newPool(t, func(o *Options) { o.Getenv = func(name string) string { return os.Getenv(name) } })
	spec.Credential = "SOME_TOKEN" // ignored: a program gets variables by `env`
	t.Setenv("SOME_TOKEN", "tok-live-0123456789")
	if got := mustWork(t, pool.Tool("srv", spec), "env", "name", "SOME_TOKEN"); got != "" {
		t.Errorf("the program received the token: %q", got)
	}
}
