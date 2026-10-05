package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"metagente/internal/diag"
	"metagente/internal/tools"
	"metagente/internal/value"
)

// fakeAgent pretends to be an A2A agent: a card, and a JSON-RPC endpoint whose
// answers each test decides.
type fakeAgent struct {
	server *httptest.Server

	mu          sync.Mutex
	card        func(base string) (status int, body string)
	answer      func(method string, params map[string]any) (status int, body string)
	requests    []map[string]any
	headers     []http.Header
	cardCalls   int
	cardHeaders http.Header
}

func okResult(result string) (int, string) {
	return 200, `{"jsonrpc":"2.0","id":1,"result":` + result + `}`
}

func defaultCard(base string) (int, string) {
	return 200, fmt.Sprintf(`{"name":"Bob","supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
 "skills":[{"id":"plan","name":"Plan","description":"plan a trip"},{"id":"ask","name":"Ask","description":"answer a question about a city"}]}`, base)
}

func newFakeAgent(t *testing.T) *fakeAgent {
	t.Helper()
	f := &fakeAgent{card: defaultCard}
	f.answer = func(string, map[string]any) (int, string) {
		return okResult(`{"message":{"role":"ROLE_AGENT","parts":[{"text":"hello"}]}}`)
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/.well-known/agent-card.json" {
			f.cardCalls++
			f.cardHeaders = r.Header.Clone()
			status, body := f.card("http://" + r.Host)
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var request map[string]any
		_ = json.Unmarshal(raw, &request)
		f.requests = append(f.requests, request)
		f.headers = append(f.headers, r.Header.Clone())
		params, _ := request["params"].(map[string]any)
		status, body := f.answer(request["method"].(string), params)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f *fakeAgent) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for _, r := range f.requests {
		names = append(names, r["method"].(string))
	}
	return names
}

func (f *fakeAgent) tool(t *testing.T, tweak func(*Options)) tools.Tool {
	t.Helper()
	opts := Options{Allow: func(Spec) error { return nil }, PollEvery: 5 * time.Millisecond}
	if tweak != nil {
		tweak(&opts)
	}
	return NewPool(opts).Tool("Bob", Spec{Name: "Bob", URL: f.server.URL})
}

func rendered(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a problem")
	}
	if d, ok := diag.From(err); ok {
		return d.Render()
	}
	return err.Error()
}

func mustContain(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func call(t *testing.T, tool tools.Tool, action string, pairs ...string) (value.Value, error) {
	t.Helper()
	args := tools.Args{}
	for i := 0; i+1 < len(pairs); i += 2 {
		args[pairs[i]] = value.Text(pairs[i+1])
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return tool.Call(ctx, action, args)
}

func TestTheSkillsOfTheCardAreTheActionsOfTheTool(t *testing.T) {
	f := newFakeAgent(t)
	actions, err := f.tool(t, nil).Actions(context.Background())
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if len(actions) != 2 || actions[0].Name != "ask" || actions[1].Name != "plan" {
		t.Fatalf("actions = %+v", actions)
	}
	if actions[0].Description != "answer a question about a city" {
		t.Errorf("description = %q", actions[0].Description)
	}
	if actions[0].Schema == nil || !actions[0].Mutates {
		t.Errorf("a skill takes values we do not know, and may change things: %+v", actions[0])
	}
}

func TestTheCardIsAskedForOnceAndAFailureIsNotKept(t *testing.T) {
	f := newFakeAgent(t)
	tool := f.tool(t, nil)
	for i := 0; i < 3; i++ {
		if _, err := tool.Actions(context.Background()); err != nil {
			t.Fatal(rendered(t, err))
		}
	}
	if f.cardCalls != 1 {
		t.Errorf("the card was asked for %d times", f.cardCalls)
	}

	g := newFakeAgent(t)
	g.card = func(string) (int, string) { return 503, "down" }
	tool = g.tool(t, nil)
	if _, err := tool.Actions(context.Background()); err == nil {
		t.Fatal("a card that fails was accepted")
	}
	g.card = defaultCard
	if _, err := tool.Actions(context.Background()); err != nil {
		t.Errorf("the failure of the card was kept: %s", rendered(t, err))
	}
}

func TestAMessageAnswerIsItsText(t *testing.T) {
	f := newFakeAgent(t)
	f.answer = func(string, map[string]any) (int, string) {
		return okResult(`{"message":{"role":"ROLE_AGENT","parts":[{"text":"In Lisbon"},{"text":"it is sunny"}]}}`)
	}
	got, err := call(t, f.tool(t, nil), "ask", "city", "Lisbon")
	if err != nil || got.Text != "In Lisbon\nit is sunny" {
		t.Fatalf("got %v %v", got.Display(), err)
	}
}

func TestTheRequestHasTheShapeOfA2A(t *testing.T) {
	f := newFakeAgent(t)
	ctx := WithTrail(context.Background(), []string{"Planner", "Helper"})
	if _, err := f.tool(t, nil).Call(ctx, "ask", tools.Args{"city": value.Text("Lisbon"), "days": value.Number(3)}); err != nil {
		t.Fatal(rendered(t, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	request := f.requests[0]
	if request["jsonrpc"] != "2.0" || request["method"] != "SendMessage" {
		t.Errorf("request = %v", request)
	}
	if got := f.headers[0].Get("A2A-Version"); got != "1.0" {
		t.Errorf("A2A-Version = %q", got)
	}
	if got := f.headers[0].Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	params := request["params"].(map[string]any)
	message := params["message"].(map[string]any)
	if id, _ := message["messageId"].(string); !strings.HasPrefix(id, "msg-") || message["role"] != "ROLE_USER" {
		t.Errorf("message = %v", message)
	}
	part := message["parts"].([]any)[0].(map[string]any)
	data := part["data"].(map[string]any)
	arguments := data["arguments"].(map[string]any)
	if data["skill"] != "ask" || arguments["city"] != "Lisbon" || arguments["days"] != float64(3) || part["mediaType"] != "application/json" {
		t.Errorf("part = %v", part)
	}
	metadata := message["metadata"].(map[string]any)
	chain := metadata["metagente"].(map[string]any)["chain"].([]any)
	if metadata["skill"] != "ask" || len(chain) != 2 || chain[0] != "Planner" || chain[1] != "Helper" {
		t.Errorf("metadata = %v", metadata)
	}
	if params["configuration"].(map[string]any)["returnImmediately"] != true {
		t.Errorf("configuration = %v", params["configuration"])
	}
}

func TestACompletedTaskAnswersWithItsArtifact(t *testing.T) {
	for name, tt := range map[string]struct {
		parts string
		check func(t *testing.T, v value.Value)
	}{
		"data": {`[{"data":{"summary":"sunny","temp":21},"mediaType":"application/json"}]`, func(t *testing.T, v value.Value) {
			summary, _ := v.Field("summary")
			temp, _ := v.Field("temp")
			if summary.Text != "sunny" || temp.Number != 21 {
				t.Errorf("record = %v", v.Display())
			}
		}},
		"text": {`[{"text":"sunny"},{"text":"21 degrees"}]`, func(t *testing.T, v value.Value) {
			if v.Text != "sunny\n21 degrees" {
				t.Errorf("text = %q", v.Text)
			}
		}},
		"nothing": {`[]`, func(t *testing.T, v value.Value) {
			if v.Kind != value.KindNothing {
				t.Errorf("got %v", v.Display())
			}
		}},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAgent(t)
			f.answer = func(string, map[string]any) (int, string) {
				return okResult(`{"task":{"id":"t1","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"artifactId":"a","parts":` + tt.parts + `}]}}`)
			}
			got, err := call(t, f.tool(t, nil), "ask", "city", "Lisbon")
			if err != nil {
				t.Fatal(rendered(t, err))
			}
			tt.check(t, got)
		})
	}
}

func TestATaskThatIsStillWorkingIsLookedAtAgain(t *testing.T) {
	f := newFakeAgent(t)
	polls := 0
	f.answer = func(method string, params map[string]any) (int, string) {
		if method == "SendMessage" {
			return okResult(`{"task":{"id":"t1","status":{"state":"TASK_STATE_WORKING"}}}`)
		}
		polls++
		if polls < 2 {
			return okResult(`{"id":"t1","status":{"state":"TASK_STATE_WORKING"}}`)
		}
		return okResult(`{"id":"t1","status":{"state":"TASK_STATE_COMPLETED"},"artifacts":[{"parts":[{"text":"done"}]}]}`)
	}
	got, err := call(t, f.tool(t, nil), "ask", "city", "Lisbon")
	if err != nil || got.Text != "done" {
		t.Fatalf("got %v %v", got.Display(), err)
	}
	if strings.Join(f.methods(), " ") != "SendMessage GetTask GetTask" {
		t.Errorf("methods = %v", f.methods())
	}
	if id := f.requests[1]["params"].(map[string]any)["id"]; id != "t1" {
		t.Errorf("the task asked about = %v", id)
	}
}

func TestATaskThatFailsSaysWhyAndKeepsItsOwnWords(t *testing.T) {
	f := newFakeAgent(t)
	f.answer = func(string, map[string]any) (int, string) {
		return okResult(`{"task":{"id":"t1","status":{"state":"TASK_STATE_FAILED","message":{"parts":[{"text":"the file is missing\nsecond line"}]}}}}`)
	}
	_, err := call(t, f.tool(t, nil), "ask", "city", "Lisbon")
	mustContain(t, rendered(t, err), "agent Bob could not answer `ask`", "  the file is missing", "  second line")
}

func TestATaskThatNeedsMoreInputIsExplained(t *testing.T) {
	f := newFakeAgent(t)
	f.answer = func(string, map[string]any) (int, string) {
		return okResult(`{"task":{"id":"t1","status":{"state":"TASK_STATE_INPUT_REQUIRED"}}}`)
	}
	_, err := call(t, f.tool(t, nil), "ask")
	mustContain(t, rendered(t, err), "needs more from you before it can answer")
}

func TestAJSONRPCErrorAndAnUnreadableAnswerAreExplained(t *testing.T) {
	f := newFakeAgent(t)
	f.answer = func(string, map[string]any) (int, string) {
		return 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32602,"message":"unknown   skill\nplease check"}}`
	}
	_, err := call(t, f.tool(t, nil), "ask")
	mustContain(t, rendered(t, err), "agent Bob refused the request: unknown skill please check")

	f.answer = func(string, map[string]any) (int, string) { return 502, "bad gateway" }
	_, err = call(t, f.tool(t, nil), "ask")
	mustContain(t, rendered(t, err), "answered 502")

	f.answer = func(string, map[string]any) (int, string) { return 200, `not json` }
	_, err = call(t, f.tool(t, nil), "ask")
	mustContain(t, rendered(t, err), "answered without a result")
}

func TestAnUnknownSkillListsWhatTheAgentHandles(t *testing.T) {
	f := newFakeAgent(t)
	_, err := call(t, f.tool(t, nil), "asc")
	mustContain(t, rendered(t, err), "`Bob` has no action called `asc`", "did you mean `Bob.ask`?", "ask, plan")
	if len(f.methods()) != 0 {
		t.Error("a message was sent for a skill the card does not have")
	}
}

// ---------- what the card may say ----------

// req: T1
func TestTheCardCannotSendTheCallsToAnotherAddress(t *testing.T) {
	f := newFakeAgent(t)
	f.card = func(string) (int, string) {
		return 200, `{"supportedInterfaces":[{"url":"http://169.254.169.254:80","protocolBinding":"JSONRPC"}],"skills":[{"id":"ask"}]}`
	}
	_, err := call(t, f.tool(t, nil), "ask")
	mustContain(t, rendered(t, err), "sends the calls to 169.254.169.254:80, which is not the address that was approved")
	if len(f.methods()) != 0 {
		t.Error("a call was made")
	}
}

func TestACardThatCannotBeUsedIsExplained(t *testing.T) {
	for name, tt := range map[string]struct {
		status int
		body   string
		want   string
	}{
		"missing":      {404, `{}`, "answered 404 when I asked for the agent card"},
		"redirect":     {302, ``, "answered 302 when I asked for the agent card"},
		"not json":     {200, `<html>`, "did not give me a readable agent card"},
		"only grpc":    {200, `{"supportedInterfaces":[{"url":"http://x","protocolBinding":"GRPC"}]}`, "does not offer the JSON-RPC way of talking"},
		"newer":        {200, `{"supportedInterfaces":[{"url":"http://x","protocolBinding":"JSONRPC","protocolVersion":"2.0"}]}`, "speaks A2A 2.0"},
		"no address":   {200, `{"supportedInterfaces":[{"url":"","protocolBinding":"JSONRPC"}]}`, "has no address to send tasks to"},
		"odd scheme":   {200, `{"supportedInterfaces":[{"url":"ftp://x","protocolBinding":"JSONRPC"}]}`, "has no address to send tasks to"},
		"lower case":   {200, `{"supportedInterfaces":[{"url":"__BASE__","protocolBinding":"jsonrpc","protocolVersion":"1"}],"skills":[{"id":"ask"}]}`, ""},
		"old protocol": {200, `{"supportedInterfaces":[{"url":"__BASE__","protocolBinding":"JSONRPC","protocolVersion":"1.2"}],"skills":[{"id":"ask"}]}`, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFakeAgent(t)
			f.card = func(base string) (int, string) { return tt.status, strings.ReplaceAll(tt.body, "__BASE__", base) }
			_, err := f.tool(t, nil).Actions(context.Background())
			if tt.want == "" {
				if err != nil {
					t.Errorf("a good card was refused: %s", rendered(t, err))
				}
				return
			}
			mustContain(t, rendered(t, err), tt.want)
		})
	}
}

func TestACardOrAnAnswerLargerThanTheLimitIsRefused(t *testing.T) {
	f := newFakeAgent(t)
	_, err := f.tool(t, func(o *Options) { o.MaxResponse = 60 }).Actions(context.Background())
	mustContain(t, rendered(t, err), "the agent card of Bob is larger than 60 bytes")

	g := newFakeAgent(t)
	g.answer = func(string, map[string]any) (int, string) {
		return okResult(`{"message":{"parts":[{"text":"` + strings.Repeat("x", 500) + `"}]}}`)
	}
	_, err = call(t, g.tool(t, func(o *Options) { o.MaxResponse = 400 }), "ask")
	mustContain(t, rendered(t, err), "the answer of agent Bob is larger than 400 bytes", "max_http_bytes")
}

// ---------- the pool ----------

// req: T1
func TestNothingIsReachedWithoutApproval(t *testing.T) {
	f := newFakeAgent(t)
	tool := f.tool(t, func(o *Options) {
		o.Allow = func(Spec) error { return diag.New("this address was not approved") }
	})
	_, err := call(t, tool, "ask")
	mustContain(t, rendered(t, err), "this address was not approved")
	if _, err := tool.Actions(context.Background()); err == nil {
		t.Error("the actions were listed without approval")
	}
	if f.cardCalls != 0 || len(f.methods()) != 0 {
		t.Errorf("the agent was reached: %d cards, %v", f.cardCalls, f.methods())
	}
	pool := NewPool(Options{})
	_, err = pool.Tool("Bob", Spec{Name: "Bob", URL: f.server.URL}).Actions(context.Background())
	mustContain(t, rendered(t, err), "not set up to call remote agents")
}

func TestAnAddressThatIsNotAnAddressIsExplained(t *testing.T) {
	for _, address := range []string{"not an address", "ftp://host", "https://user:pass@host", ""} {
		pool := NewPool(Options{Allow: func(Spec) error { return nil }})
		_, err := pool.Tool("Bob", Spec{Name: "Bob", URL: address}).Actions(context.Background())
		text := rendered(t, err)
		mustContain(t, text, "the address of the remote agent Bob is not valid")
		if strings.Contains(text, "pass") {
			t.Errorf("a password was repeated:\n%s", text)
		}
	}
}

func TestAnAgentThatIsNotThereIsExplained(t *testing.T) {
	f := newFakeAgent(t)
	address := f.server.URL
	f.server.Close()
	pool := NewPool(Options{Allow: func(Spec) error { return nil }})
	_, err := pool.Tool("Bob", Spec{Name: "Bob", URL: address}).Actions(context.Background())
	mustContain(t, rendered(t, err), "I could not reach 127.0.0.1", "remote agent Bob", "metagente serve")
}

// A call that is given up is cancelled on the other side too.
func TestGivingUpCancelsTheTaskOfTheOtherSide(t *testing.T) {
	f := newFakeAgent(t)
	f.answer = func(method string, params map[string]any) (int, string) {
		switch method {
		case "SendMessage":
			return okResult(`{"task":{"id":"t9","status":{"state":"TASK_STATE_WORKING"}}}`)
		case "CancelTask":
			return okResult(`{"id":"t9","status":{"state":"TASK_STATE_CANCELED"}}`)
		}
		return okResult(`{"id":"t9","status":{"state":"TASK_STATE_WORKING"}}`) // GetTask: the task itself
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, err := f.tool(t, nil).Call(ctx, "ask", tools.Args{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cancelled := false
	for _, request := range f.requests {
		if request["method"] == "CancelTask" && request["params"].(map[string]any)["id"] == "t9" {
			cancelled = true
		}
	}
	if !cancelled {
		t.Errorf("the task was not cancelled on the other side: %v", f.requests)
	}
}

// ---------- credentials (E5) ----------

func (f *fakeAgent) toolWithCredential(t *testing.T, credential string, env map[string]string) tools.Tool {
	t.Helper()
	pool := NewPool(Options{
		Allow:     func(Spec) error { return nil },
		PollEvery: 5 * time.Millisecond,
		Getenv:    func(name string) string { return env[name] },
	})
	return pool.Tool("Bob", Spec{Name: "Bob", URL: f.server.URL, Credential: credential})
}

// req: E5
func TestTheCredentialIsSentWithTheCardAndWithTheCallsToTheApprovedAddress(t *testing.T) {
	f := newFakeAgent(t)
	tool := f.toolWithCredential(t, "BOB_TOKEN", map[string]string{"BOB_TOKEN": "  tok-live-0123456789\n"})
	if _, err := call(t, tool, "ask", "city", "Lisbon"); err != nil {
		t.Fatal(rendered(t, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.headers[0].Get("Authorization"); got != "Bearer tok-live-0123456789" {
		t.Errorf("Authorization of the call = %q", got)
	}
	// A server may keep its card for those who have the token. The card is asked
	// for at the address that was approved, and the card cannot name another one.
	if got := f.cardHeaders.Get("Authorization"); got != "Bearer tok-live-0123456789" {
		t.Errorf("Authorization of the card = %q", got)
	}
}

func TestWithoutACredentialNothingIsSentEvenIfAVariableExists(t *testing.T) {
	f := newFakeAgent(t)
	tool := f.toolWithCredential(t, "", map[string]string{"BOB_TOKEN": "tok-live-0123456789"})
	if _, err := call(t, tool, "ask"); err != nil {
		t.Fatal(rendered(t, err))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if got := f.headers[0].Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q", got)
	}
	if got := f.cardHeaders.Get("Authorization"); got != "" {
		t.Errorf("Authorization of the card = %q", got)
	}
}

// req: E5
func TestACredentialWhoseVariableIsEmptyStopsTheCallBeforeItLeaves(t *testing.T) {
	f := newFakeAgent(t)
	tool := f.toolWithCredential(t, "BOB_TOKEN", map[string]string{})
	_, err := call(t, tool, "ask")
	mustContain(t, rendered(t, err), "the credential for Bob is not set: the variable BOB_TOKEN is empty", "export BOB_TOKEN")
	if len(f.methods()) != 0 || f.cardCalls != 0 {
		t.Errorf("the agent was reached without the credential: %d cards, %v", f.cardCalls, f.methods())
	}
}

// req: E5, L8
func TestTheTokenIsTakenOutOfWhatTheOtherSideEchoes(t *testing.T) {
	const token = "tok-live-0123456789"
	env := map[string]string{"BOB_TOKEN": token}

	f := newFakeAgent(t)
	f.answer = func(string, map[string]any) (int, string) {
		return 200, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"invalid token ` + token + ` for this agent"}}`
	}
	_, err := call(t, f.toolWithCredential(t, "BOB_TOKEN", env), "ask")
	text := rendered(t, err)
	mustContain(t, text, "invalid token [hidden] for this agent")
	if strings.Contains(text, token) {
		t.Errorf("the token is in the error:\n%s", text)
	}

	g := newFakeAgent(t)
	g.answer = func(string, map[string]any) (int, string) {
		return okResult(`{"task":{"id":"t1","status":{"state":"TASK_STATE_FAILED","message":{"parts":[{"text":"cannot use ` + token + `"}]}}}}`)
	}
	_, err = call(t, g.toolWithCredential(t, "BOB_TOKEN", env), "ask")
	text = rendered(t, err)
	mustContain(t, text, "cannot use [hidden]")
	if strings.Contains(text, token) {
		t.Errorf("the token is in the error:\n%s", text)
	}
}

// req: E5
func TestTheTokenNeverTravelsWithoutEncryptionBeyondThisMachine(t *testing.T) {
	pool := NewPool(Options{Getenv: func(string) string { return "tok-live-0123456789" }})
	a := &agent{pool: pool, spec: Spec{Name: "Bob", Credential: "BOB_TOKEN"}}
	for _, tt := range []struct {
		address string
		ok      bool
	}{
		{"https://bob.example/rpc", true},
		{"http://127.0.0.1:8080/rpc", true},
		{"http://localhost:8080/rpc", true},
		{"http://[::1]:8080/rpc", true},
		{"http://bob.example/rpc", false},
		{"http://10.0.0.5:8080/rpc", false},
	} {
		req, err := http.NewRequest(http.MethodPost, tt.address, nil)
		if err != nil {
			t.Fatal(err)
		}
		err = a.authorize(req)
		switch {
		case tt.ok && err != nil:
			t.Errorf("%s was refused: %s", tt.address, rendered(t, err))
		case tt.ok && req.Header.Get("Authorization") == "":
			t.Errorf("%s: the token was not added", tt.address)
		case !tt.ok && err == nil:
			t.Errorf("%s: the token would travel without encryption", tt.address)
		case !tt.ok && req.Header.Get("Authorization") != "":
			t.Errorf("%s: the token was added although the call was refused", tt.address)
		case !tt.ok:
			mustContain(t, rendered(t, err), "would travel without encryption")
		}
	}
}

func TestAMessageThatAnswersWithDataKeepsTheStructure(t *testing.T) {
	f := newFakeAgent(t)
	f.answer = func(string, map[string]any) (int, string) {
		return okResult(`{"message":{"role":"ROLE_AGENT","parts":[{"data":{"summary":"sunny","temp":21},"mediaType":"application/json"}]}}`)
	}
	got, err := call(t, f.tool(t, nil), "ask", "city", "Lisbon")
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	summary, _ := got.Field("summary")
	temp, _ := got.Field("temp")
	if summary.Text != "sunny" || temp.Number != 21 {
		t.Errorf("got %v", got.Display())
	}
}

func TestABusyAgentIsToldAsSuchAndTheCallCanBeTriedAgain(t *testing.T) {
	f := newFakeAgent(t)
	f.answer = func(string, map[string]any) (int, string) { return 503, "the server is busy; try again in a moment" }
	_, err := call(t, f.tool(t, nil), "ask")
	mustContain(t, rendered(t, err), "agent Bob is busy and cannot take the call now", "try again in a moment")
}

// cardWithModes is a card whose agent says what it takes: the modes of the card, and
// optionally the modes of the skill (a list written in JSON, such as ["text"]).
func cardWithModes(defaults, skillModes string) func(base string) (int, string) {
	return func(base string) (int, string) {
		extra := ""
		if skillModes != "" {
			extra = `,"inputModes":` + skillModes
		}
		return 200, fmt.Sprintf(`{"name":"Bob","supportedInterfaces":[{"url":%q,"protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
 "defaultInputModes":%s,
 "skills":[{"id":"ask","name":"Ask","description":"answer a question"%s}]}`, base, defaults, extra)
	}
}

// sentPart is the first part of the first message that the fake agent was sent.
func sentPart(t *testing.T, f *fakeAgent) map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("nothing was sent")
	}
	params := f.requests[0]["params"].(map[string]any)
	return params["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)
}

func TestAnAgentThatTakesOnlyTextIsSentTheValueAsText(t *testing.T) {
	f := newFakeAgent(t)
	f.card = cardWithModes(`["text"]`, "")
	if _, err := call(t, f.tool(t, nil), "ask", "text", "hello"); err != nil {
		t.Fatal(rendered(t, err))
	}
	part := sentPart(t, f)
	if part["text"] != "hello" || part["mediaType"] != "text/plain" || part["data"] != nil {
		t.Errorf("part = %v", part)
	}
}

func TestSeveralValuesGoAsOneLineEachInTheOrderOfTheirNames(t *testing.T) {
	f := newFakeAgent(t)
	f.card = cardWithModes(`["text/plain"]`, "")
	args := tools.Args{"days": value.Number(3), "city": value.Text("Lisbon")}
	if _, err := f.tool(t, nil).Call(context.Background(), "ask", args); err != nil {
		t.Fatal(rendered(t, err))
	}
	if part := sentPart(t, f); part["text"] != "city: Lisbon\ndays: 3" {
		t.Errorf("part = %v", part)
	}
}

func TestAnAgentThatTakesOnlyTextIsNotSentAnEmptyCall(t *testing.T) {
	f := newFakeAgent(t)
	f.card = cardWithModes(`["text"]`, "")
	_, err := f.tool(t, nil).Call(context.Background(), "ask", tools.Args{})
	mustContain(t, rendered(t, err), "agent Bob takes only text", "no value to send as text", "Fix:")
	for _, method := range f.methods() {
		if method == "SendMessage" {
			t.Error("a call with nothing to say was sent")
		}
	}
}

func TestAnAgentThatTakesJSONIsSentTheBlockOfDataEvenIfItAlsoTakesText(t *testing.T) {
	f := newFakeAgent(t)
	f.card = cardWithModes(`["application/json","text/plain"]`, "")
	if _, err := call(t, f.tool(t, nil), "ask", "city", "Lisbon"); err != nil {
		t.Fatal(rendered(t, err))
	}
	part := sentPart(t, f)
	data, _ := part["data"].(map[string]any)
	if data["skill"] != "ask" || part["mediaType"] != "application/json" || part["text"] != nil {
		t.Errorf("part = %v", part)
	}
}

func TestWhatTheSkillTakesIsWhatCountsOverWhatTheCardTakes(t *testing.T) {
	f := newFakeAgent(t)
	f.card = cardWithModes(`["application/json"]`, `["text/plain"]`)
	if _, err := call(t, f.tool(t, nil), "ask", "text", "hi"); err != nil {
		t.Fatal(rendered(t, err))
	}
	if part := sentPart(t, f); part["text"] != "hi" {
		t.Errorf("part = %v", part)
	}
}

func TestWhichModesTakeOnlyText(t *testing.T) {
	for _, tt := range []struct {
		modes []string
		want  bool
	}{
		{nil, false},
		{[]string{"text"}, true},
		{[]string{"text/plain"}, true},
		{[]string{"TEXT/Plain"}, true},
		{[]string{"application/json"}, false},
		{[]string{"text", "application/json"}, false},
		{[]string{"application/vnd.example+json"}, false},
		{[]string{"image/png"}, false},
	} {
		if got := takesOnlyText(tt.modes); got != tt.want {
			t.Errorf("takesOnlyText(%q) = %v, want %v", tt.modes, got, tt.want)
		}
	}
}

// An agent like the cancellable one of the SDK in JavaScript answers only when the task ends, unless it is
// asked to answer at once. A caller that gives up while it waits has to be able to cancel the task.
func TestGivingUpWhileTheOtherSideWouldHaveKeptUsWaitingStillCancelsTheTask(t *testing.T) {
	f := newFakeAgent(t)
	f.answer = func(method string, params map[string]any) (int, string) {
		switch method {
		case "SendMessage":
			configuration, _ := params["configuration"].(map[string]any)
			if configuration["returnImmediately"] != true {
				time.Sleep(600 * time.Millisecond) // it answers when the task ends
				return okResult(`{"task":{"id":"t7","status":{"state":"TASK_STATE_COMPLETED"}}}`)
			}
			return okResult(`{"task":{"id":"t7","status":{"state":"TASK_STATE_WORKING"}}}`)
		case "CancelTask":
			return okResult(`{"id":"t7","status":{"state":"TASK_STATE_CANCELED"}}`)
		}
		return okResult(`{"id":"t7","status":{"state":"TASK_STATE_WORKING"}}`) // GetTask: the task itself
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, err := f.tool(t, nil).Call(ctx, "ask", tools.Args{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	cancelled := false
	for _, request := range f.requests {
		if request["method"] == "CancelTask" && request["params"].(map[string]any)["id"] == "t7" {
			cancelled = true
		}
	}
	if !cancelled {
		t.Errorf("the task was not cancelled on the other side: %v", f.requests)
	}
}
