package serve

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"

	"metagente/internal/config"
	"metagente/internal/lang"
	"metagente/internal/remote"
	"metagente/internal/runtime"
	"metagente/internal/tools"
	"metagente/internal/trust"
	"metagente/internal/value"
)

// listen starts the server on a real port, with the Host it will be asked for.
func listen(t *testing.T, tweak func(*Config), agents ...Agent) (base string, server *Server) {
	t.Helper()
	httpServer := httptest.NewUnstartedServer(nil)
	address := httpServer.Listener.Addr().String()
	cfg := Config{Token: goodToken, Hosts: []string{address}, Version: "1.2.3"}
	if tweak != nil {
		tweak(&cfg)
	}
	server, err := New(cfg, agents)
	if err != nil {
		t.Fatal(err)
	}
	httpServer.Config.Handler = server
	httpServer.Start()
	t.Cleanup(func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
		server.Close()
	})
	return "http://" + address, server
}

func remoteTool(base, credential string, env map[string]string) tools.Tool {
	pool := remote.NewPool(remote.Options{
		Allow:     func(remote.Spec) error { return nil },
		PollEvery: 5 * time.Millisecond,
		Getenv:    func(name string) string { return env[name] },
	})
	return pool.Tool("Bob", remote.Spec{Name: "Bob", URL: base + "/agents/Bob", Credential: credential})
}

func callTool(t *testing.T, tool tools.Tool, trail []string, action string, pairs ...string) (value.Value, error) {
	t.Helper()
	args := tools.Args{}
	for i := 0; i+1 < len(pairs); i += 2 {
		args[pairs[i]] = value.Text(pairs[i+1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return tool.Call(remote.WithTrail(ctx, trail), action, args)
}

// This pair is what Metagente to Metagente is: the client of one process, with its
// token and the card kept for those who have it, against the server of another.
func TestOurClientTalksToOurServerWithTheToken(t *testing.T) {
	base, _ := listen(t, nil, newFake("Bob", defaultSkills...))
	tool := remoteTool(base, "BOB_TOKEN", map[string]string{"BOB_TOKEN": goodToken})

	checkRemoteActions(t, tool)
	checkRemoteCalls(t, tool)
	checkRemoteFailure(t, tool)
}

// checkRemoteActions wants one action for each skill of the other agent, with what it is for.
func checkRemoteActions(t *testing.T, tool tools.Tool) {
	t.Helper()
	actions, err := tool.Actions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var echo string
	for _, a := range actions {
		if a.Name == "echo" {
			echo = a.Description
		}
	}
	if len(actions) != len(defaultSkills) || echo != "repeats the text Takes: text." {
		t.Fatalf("%d actions, echo = %q", len(actions), echo)
	}
}

// checkRemoteCalls calls three skills: one that repeats a text, one that answers with a record, and
// one that is passed on through two agents.
func checkRemoteCalls(t *testing.T, tool tools.Tool) {
	t.Helper()
	if got, err := callTool(t, tool, nil, "echo", "text", "hello"); err != nil || got.Text != "echo: hello" {
		t.Errorf("echo: %v %v", got.Display(), err)
	}
	record, err := callTool(t, tool, nil, "record")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := record.Field("a")
	n, _ := record.Field("n")
	if a.Text != "x" || n.Number != 2 {
		t.Errorf("a record lost its fields on the way: %v", record.Display())
	}
	if got, err := callTool(t, tool, []string{"Planner", "Helper"}, "chain"); err != nil || got.Text != "Planner>Helper" {
		t.Errorf("the chain: %v %v", got.Display(), err)
	}
}

// checkRemoteFailure calls a skill that fails, and wants the reason to arrive without anything of the
// other computer.
func checkRemoteFailure(t *testing.T, tool tools.Tool) {
	t.Helper()
	_, err := callTool(t, tool, nil, "fail")
	text := err.Error()
	if d, ok := asDiag(err); ok {
		text = d.Render()
	}
	for _, want := range []string{"agent Bob could not answer `fail`", "the order is missing"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "/home/secret") {
		t.Errorf("a path of the other computer reached this one:\n%s", text)
	}
}

func TestWithoutTheRightTokenOurClientCannotEvenReadTheCard(t *testing.T) {
	base, server := listen(t, nil, newFake("Bob", defaultSkills...))
	for name, env := range map[string]map[string]string{
		"no credential": {},
		"wrong token":   {"BOB_TOKEN": strings.Repeat("w", 40)},
	} {
		credential := ""
		if name == "wrong token" {
			credential = "BOB_TOKEN"
		}
		tool := remoteTool(base, credential, env)
		_, err := tool.Actions(context.Background())
		if err == nil || !strings.Contains(err.Error(), "401") && !strings.Contains(renderedText(err), "answered 401") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if server.contexts.Len() != 0 {
		t.Error("a conversation was opened without the token")
	}
}

// The official client, written by someone else, against our server: the proof that
// the server speaks A2A and not only a dialect of it that our own client reads.
func TestTheClientOfTheSDKTalksToOurServer(t *testing.T) {
	httpServer := httptest.NewUnstartedServer(nil)
	address := httpServer.Listener.Addr().String()
	server, err := New(Config{Token: goodToken, Hosts: []string{address}},
		[]Agent{newFake("Bob", Skill{ID: "echo", Params: []string{"text"}})})
	if err != nil {
		t.Fatal(err)
	}
	// The client of the SDK is not told our token, so a small door in front adds it.
	httpServer.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+goodToken)
		server.ServeHTTP(w, r)
	})
	httpServer.Start()
	defer httpServer.Close()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	card := &a2a.AgentCard{
		Name: "Bob",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface("http://"+address+"/agents/Bob", a2a.TransportProtocolJSONRPC),
		},
	}
	client, err := a2aclient.NewFromCard(ctx, card)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.SendMessage(ctx, &a2a.SendMessageRequest{Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("ping"))})
	if err != nil {
		t.Fatalf("the client of the SDK was refused by our server: %v", err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "echo: ping") {
		t.Errorf("the SDK did not understand the answer, or the agent never ran:\n%s", raw)
	}
}

// ---------- a real agent behind the server ----------

const notesSource = `agent Notes
  goal "Remember one thing"
  tool state
  accepts remember what
  accepts recall
  on remember
    state.set key: "thing" value: what
    reply "ok"
  on recall
    reply state.get key: "thing"
`

func realRuntime(t *testing.T) *runtime.Runtime {
	t.Helper()
	cfg := config.Default()
	cfg.Root = t.TempDir()
	rt := runtime.New(cfg)
	rt.Trust = trust.NewRegistry(filepath.Join(t.TempDir(), "config"))
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

// req: S7, S9
func TestARealAgentRemembersInItsConversationAndForgetsWhenItEnds(t *testing.T) {
	rt := realRuntime(t)
	defs, err := lang.ParseFile("notes.ag", "", notesSource)
	if err != nil {
		t.Fatal(err)
	}
	notes := NewRuntimeAgent(rt, defs[0])
	if notes.Name() != "Notes" || notes.Goal() != "Remember one thing" || len(notes.Skills()) != 2 {
		t.Fatalf("agent = %s, %q, %v", notes.Name(), notes.Goal(), notes.Skills())
	}
	s := newTestServer(t, nil, notes)
	clock := &fakeClock{now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	s.contexts.now = clock.Now
	say := func(path, params string) (string, string) {
		a := decodeAnswer(t, do(s, http.MethodPost, path, rpcBody("SendMessage", params), nil))
		return reply(t, a)
	}

	ok, ctxA := say("/agents/Notes", message(dataPart("remember", `{"what":"Ana"}`), ""))
	got, _ := say("/agents/Notes", message(dataPart("recall", `{}`), `"contextId":"`+ctxA+`"`))
	other, ctxB := say("/agents/Notes", message(dataPart("recall", `{}`), ""))
	if ok != "ok" || got != "Ana" || other != "" || ctxA == ctxB {
		t.Fatalf("remember %q, recall %q, another conversation recalls %q", ok, got, other)
	}

	// The conversation ends, and what it knew is gone from the memory of the runtime.
	clock.Advance(time.Hour)
	s.contexts.Sweep()
	again, err := runtime.NewAgent(rt, defs[0], ctxA)
	if err != nil {
		t.Fatal(err)
	}
	forgotten, err := again.Handle(context.Background(), &runtime.Call{TaskID: "t"}, "recall", tools.Args{})
	if err != nil || forgotten.Text != "" {
		t.Errorf("the memory of an ended conversation is still there: %q %v", forgotten.Text, err)
	}
}

// req: P1
func TestAMistakeInARealAgentIsReportedWithoutItsFileOrItsLines(t *testing.T) {
	rt := realRuntime(t)
	defs, err := lang.ParseFile("/home/someone/private/notes.ag", "", notesSource)
	if err != nil {
		t.Fatal(err)
	}
	s := newTestServer(t, nil, NewRuntimeAgent(rt, defs[0]))
	a := decodeAnswer(t, do(s, http.MethodPost, "/agents/Notes", rpcBody("SendMessage", message(dataPart("remember", `{}`), "")), nil))
	if a.Error != nil || a.Result["task"] == nil {
		t.Fatalf("a missing value should be a failed task: %s", a.Raw)
	}
	for _, leak := range []string{"/home/someone", "notes.ag", "state.set", "on remember"} {
		if strings.Contains(a.Raw, leak) {
			t.Errorf("the answer tells %q:\n%s", leak, a.Raw)
		}
	}
	if !strings.Contains(a.Raw, "needs a value for `what`") {
		t.Errorf("the reason was lost:\n%s", a.Raw)
	}
}

// The door hangs up for real: after a 401 the server closes the connection.
func TestAConnectionThatWasTurnedAwayIsClosed(t *testing.T) {
	base, _ := listen(t, nil, newFake("Bob", defaultSkills...))
	address := strings.TrimPrefix(base, "http://")
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte("GET /agents/Bob/.well-known/agent-card.json HTTP/1.1\r\nHost: " + address + "\r\n\r\n")); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if _, err := reader.ReadByte(); err != io.EOF {
		t.Errorf("the connection was kept open: %v", err)
	}
}
