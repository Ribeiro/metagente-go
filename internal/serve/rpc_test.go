package serve

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"metagente/internal/applog"
	"metagente/internal/diag"
	"metagente/internal/value"
)

// ---------- an agent that needs nothing to run ----------

var defaultSkills = []Skill{
	{ID: "echo", Description: "repeats the text", Params: []string{"text"}},
	{ID: "count"}, {ID: "record"}, {ID: "fail"}, {ID: "boom"}, {ID: "slow"}, {ID: "hold"}, {ID: "chain"},
	{ID: "sum", Params: []string{"a", "b"}},
}

type fakeAgent struct {
	name     string
	skills   []Skill
	startErr error
	// hold, when set, is what the skill `hold` waits for; entered counts the calls inside it.
	hold    chan struct{}
	entered atomic.Int32

	mu     sync.Mutex
	begun  int
	closed int
}

func newFake(name string, skills ...Skill) *fakeAgent { return &fakeAgent{name: name, skills: skills} }

func (a *fakeAgent) Name() string    { return a.name }
func (a *fakeAgent) Goal() string    { return "Goal of " + a.name }
func (a *fakeAgent) Skills() []Skill { return a.skills }

func (a *fakeAgent) Begin(_ context.Context, _ string, _ Call) (Conversation, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.startErr != nil {
		return nil, a.startErr
	}
	a.begun++
	return &fakeConversation{agent: a}, nil
}

func (a *fakeAgent) counts() (begun, closed int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.begun, a.closed
}

type fakeConversation struct {
	agent   *fakeAgent
	counter int
}

func (c *fakeConversation) Close() {
	c.agent.mu.Lock()
	defer c.agent.mu.Unlock()
	c.agent.closed++
}

func (c *fakeConversation) Run(ctx context.Context, call Call, skill string, args map[string]value.Value) (value.Value, error) {
	switch skill {
	case "echo":
		return value.Text("echo: " + args["text"].Text), nil
	case "count":
		c.counter++
		return value.Text(strconv.Itoa(c.counter)), nil
	case "record":
		return value.Record(map[string]value.Value{"a": value.Text("x"), "n": value.Number(2)}), nil
	case "sum":
		return value.Number(args["a"].Number + args["b"].Number), nil
	case "chain":
		return value.Text(strings.Join(call.Chain, ">")), nil
	case "fail":
		return value.Nothing, diag.New("the order is missing").
			At("/home/secret/agents/bob.ag", 7, 3).WithSource("a secret line of source\n").
			AddRelated("see /home/secret/other.ag").Fix("add the order")
	case "boom":
		panic("kaboom")
	case "slow":
		<-ctx.Done()
		return value.Nothing, ctx.Err()
	case "hold":
		c.agent.entered.Add(1)
		select {
		case <-c.agent.hold:
			return value.Text("released"), nil
		case <-ctx.Done():
			return value.Nothing, ctx.Err()
		}
	}
	return value.Nothing, nil
}

// ---------- helpers ----------

const bobPath = "/agents/Bob"

func newTestServer(t *testing.T, tweak func(*Config), agents ...Agent) *Server {
	t.Helper()
	cfg := Config{Token: goodToken, Hosts: LoopbackHosts(8080), Version: "1.2.3"}
	if tweak != nil {
		tweak(&cfg)
	}
	if len(agents) == 0 {
		agents = []Agent{newFake("Bob", defaultSkills...)}
	}
	s, err := New(cfg, agents)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func do(s *Server, method, path, body string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Host = "127.0.0.1:8080"
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Authorization", "Bearer "+goodToken)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if mutate != nil {
		mutate(req)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

type answer struct {
	ID     json.RawMessage
	Result map[string]any
	Error  *rpcError
	Raw    string
}

func decodeAnswer(t *testing.T, rec *httptest.ResponseRecorder) answer {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  map[string]any  `json:"result"`
		Error   *rpcError       `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil || raw.JSONRPC != "2.0" {
		t.Fatalf("not a JSON-RPC answer: %q", rec.Body.String())
	}
	return answer{ID: raw.ID, Result: raw.Result, Error: raw.Error, Raw: rec.Body.String()}
}

func rpcBody(method, params string) string {
	return fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":%q,"params":%s}`, method, params)
}

// message builds the params of a SendMessage; extra is more fields of the message.
func message(parts, extra string) string {
	if extra != "" {
		extra = "," + extra
	}
	return `{"message":{"messageId":"m1","role":"ROLE_USER","parts":[` + parts + `]` + extra + `}}`
}

func dataPart(skill, args string) string {
	return `{"data":{"skill":"` + skill + `","arguments":` + args + `}}`
}

func send(t *testing.T, s *Server, params string) answer {
	t.Helper()
	return decodeAnswer(t, do(s, http.MethodPost, bobPath, rpcBody("SendMessage", params), nil))
}

func reply(t *testing.T, a answer) (text, contextID string) {
	t.Helper()
	if a.Error != nil {
		t.Fatalf("error %d: %s", a.Error.Code, a.Error.Message)
	}
	msg, ok := a.Result["message"].(map[string]any)
	if !ok {
		t.Fatalf("not a message: %s", a.Raw)
	}
	parts := msg["parts"].([]any)
	text, _ = parts[0].(map[string]any)["text"].(string)
	contextID, _ = msg["contextId"].(string)
	if msg["role"] != "ROLE_AGENT" || msg["messageId"] == "" {
		t.Errorf("message = %v", msg)
	}
	return text, contextID
}

func wantError(t *testing.T, a answer, code int, contains string) {
	t.Helper()
	if a.Error == nil {
		t.Fatalf("expected error %d, got %s", code, a.Raw)
	}
	if a.Error.Code != code || !strings.Contains(a.Error.Message, contains) {
		t.Errorf("error = %d %q, want %d containing %q", a.Error.Code, a.Error.Message, code, contains)
	}
}

// ---------- the card ----------

// req: S8
func TestTheCardNeedsTheTokenAndSaysWhatTheAgentIsFor(t *testing.T) {
	s := newTestServer(t, nil)
	path := bobPath + "/.well-known/agent-card.json"
	if rec := do(s, http.MethodGet, path, "", func(r *http.Request) { r.Header.Del("Authorization") }); rec.Code != http.StatusUnauthorized {
		t.Fatalf("without the token: %d", rec.Code)
	}
	rec := do(s, http.MethodGet, path, "", nil)
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("code %d, type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	var card map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil {
		t.Fatal(err)
	}
	checkCard(t, card)
}

// checkCard wants the card of Bob to say who he is, where he is reached, how, and what his skills are for.
func checkCard(t *testing.T, card map[string]any) {
	t.Helper()
	if card["name"] != "Bob" || card["description"] != "Goal of Bob" || card["version"] != "1.2.3" {
		t.Errorf("card = %v", card)
	}
	checkCardInterface(t, card)
	checkCardSkills(t, card)
}

// checkCardInterface looks at the address, the protocol, the capabilities and the way to authenticate.
func checkCardInterface(t *testing.T, card map[string]any) {
	t.Helper()
	iface := card["supportedInterfaces"].([]any)[0].(map[string]any)
	if iface["url"] != "http://127.0.0.1:8080/agents/Bob" || iface["protocolBinding"] != "JSONRPC" || iface["protocolVersion"] != "1.0" {
		t.Errorf("interface = %v", iface)
	}
	caps := card["capabilities"].(map[string]any)
	if caps["streaming"] != false || caps["pushNotifications"] != false {
		t.Errorf("capabilities = %v", caps)
	}
	scheme := card["securitySchemes"].(map[string]any)["bearer"].(map[string]any)["httpAuthSecurityScheme"].(map[string]any)
	if scheme["scheme"] != "Bearer" {
		t.Errorf("security scheme = %v", scheme)
	}
}

// checkCardSkills wants the skill echo, and what it says it is for.
func checkCardSkills(t *testing.T, card map[string]any) {
	t.Helper()
	var echo map[string]any
	for _, sk := range card["skills"].([]any) {
		if m := sk.(map[string]any); m["id"] == "echo" {
			echo = m
		}
	}
	if echo == nil || echo["description"] != "repeats the text Takes: text." {
		t.Errorf("echo = %v", echo)
	}
}

// req: S8
func TestTheMinimalPublicCardSaysOnlyTheNames(t *testing.T) {
	s := newTestServer(t, func(c *Config) { c.PublicCard = true })
	rec := do(s, http.MethodGet, bobPath+"/.well-known/agent-card.json", "", func(r *http.Request) { r.Header.Del("Authorization") })
	if rec.Code != 200 {
		t.Fatalf("code %d", rec.Code)
	}
	body := rec.Body.String()
	for _, leak := range []string{"Goal of Bob", "Takes:", "repeats the text", "text"} {
		if leak == "text" {
			continue // "text/plain" is a mode, not a value
		}
		if strings.Contains(body, leak) {
			t.Errorf("the public card tells %q:\n%s", leak, body)
		}
	}
	if !strings.Contains(body, `"id":"echo"`) || !strings.Contains(body, `"name":"Bob"`) {
		t.Errorf("the public card is missing the names:\n%s", body)
	}
	for name := range rec.Header() {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Errorf("the public card sent %s", name)
		}
	}
	// The messages themselves are never public.
	if rec := do(s, http.MethodPost, bobPath, rpcBody("SendMessage", message(dataPart("echo", `{"text":"x"}`), "")), func(r *http.Request) { r.Header.Del("Authorization") }); rec.Code != http.StatusUnauthorized {
		t.Errorf("a message without the token: %d", rec.Code)
	}
}

// req: S8
func TestAStrangerCannotTellWhichAgentsExist(t *testing.T) {
	s := newTestServer(t, nil)
	paths := []string{bobPath, bobPath + "/", "/agents/bob", "/agents/Nobody", "/agents/Nobody/.well-known/agent-card.json",
		"/agents/Bob/extra", "/", "/agents/../agents/Bob", "/.well-known/agent-card.json", "/agents/"}
	// Each request comes from another place, so that the slowing down of one that
	// fails often (its own test is in guard_test.go) does not answer instead.
	for i, path := range paths {
		without := do(s, http.MethodPost, path, "{}", func(r *http.Request) {
			r.Header.Del("Authorization")
			r.RemoteAddr = fmt.Sprintf("198.51.100.%d:1", i+1)
		})
		if without.Code != http.StatusUnauthorized {
			t.Errorf("%s without the token: %d", path, without.Code)
		}
	}
	for _, path := range paths[1:] {
		with := do(s, http.MethodPost, path, "{}", nil)
		if with.Code != http.StatusNotFound {
			t.Errorf("%s with the token: %d, want 404", path, with.Code)
		}
	}
}

// ---------- a message ----------

func TestAMessageIsAnsweredWithAMessageAndAConversation(t *testing.T) {
	s := newTestServer(t, nil)
	text, contextID := reply(t, send(t, s, message(dataPart("echo", `{"text":"hi"}`), "")))
	if text != "echo: hi" || !strings.HasPrefix(contextID, "ctx-") {
		t.Errorf("text %q, context %q", text, contextID)
	}
}

func TestARecordIsAnsweredWithDataAndNumbersStayNumbers(t *testing.T) {
	s := newTestServer(t, nil)
	a := send(t, s, message(dataPart("record", `{}`), ""))
	part := a.Result["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	data := part["data"].(map[string]any)
	if data["a"] != "x" || data["n"] != float64(2) || part["mediaType"] != "application/json" {
		t.Errorf("part = %v", part)
	}
	sum := send(t, s, message(dataPart("sum", `{"a":2,"b":3.5}`), ""))
	sumPart := sum.Result["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	if sumPart["data"] != 5.5 {
		t.Errorf("sum = %v", sumPart)
	}
}

// req: S7
func TestTheSecondMessageOfAConversationContinuesIt(t *testing.T) {
	s := newTestServer(t, nil)
	one, ctxA := reply(t, send(t, s, message(dataPart("count", `{}`), "")))
	two, _ := reply(t, send(t, s, message(dataPart("count", `{}`), `"contextId":"`+ctxA+`"`)))
	other, ctxB := reply(t, send(t, s, message(dataPart("count", `{}`), "")))
	three, _ := reply(t, send(t, s, message(dataPart("count", `{}`), `"contextId":"`+ctxA+`"`)))
	if one != "1" || two != "2" || three != "3" || other != "1" || ctxA == ctxB {
		t.Errorf("counts %s %s %s, other %s", one, two, three, other)
	}
}

// req: S7
func TestOnlyAnIdTheServerIssuedContinuesAConversation(t *testing.T) {
	bob, carol := newFake("Bob", defaultSkills...), newFake("Carol", defaultSkills...)
	s := newTestServer(t, nil, bob, carol)
	_, ctxBob := reply(t, send(t, s, message(dataPart("count", `{}`), "")))
	for _, id := range []string{"ctx-00000000000000000000000000000000", "made-up", "", strings.Repeat("a", 500)} {
		if id == "" {
			continue
		}
		wantError(t, send(t, s, message(dataPart("count", `{}`), `"contextId":"`+id+`"`)), codeInvalidParams, "unknown or expired conversation")
	}
	// The conversation of one agent is not the conversation of another.
	rec := do(s, http.MethodPost, "/agents/Carol", rpcBody("SendMessage", message(dataPart("count", `{}`), `"contextId":"`+ctxBob+`"`)), nil)
	wantError(t, decodeAnswer(t, rec), codeInvalidParams, "unknown or expired conversation")
	if _, closed := carol.counts(); closed != 0 {
		t.Error("the conversation of Bob was touched through Carol")
	}
}

func TestTextAloneIsBoundToTheOneValueOfAMessage(t *testing.T) {
	one := newFake("Echoer", Skill{ID: "echo", Params: []string{"text"}})
	s := newTestServer(t, nil, one)
	rec := do(s, http.MethodPost, "/agents/Echoer", rpcBody("SendMessage", message(`{"text":"ping"},{"text":"pong"}`, "")), nil)
	if text, _ := reply(t, decodeAnswer(t, rec)); text != "echo: ping\npong" {
		t.Errorf("text = %q", text)
	}

	many := newTestServer(t, nil)
	wantError(t, send(t, many, message(`{"text":"ping"}`, "")), codeInvalidParams, `say which message to run`)
	// The skill can be named in the metadata while the text carries the value.
	a := send(t, many, message(`{"text":"ping"}`, `"metadata":{"skill":"echo"}`))
	if text, _ := reply(t, a); text != "echo: ping" {
		t.Errorf("text = %q", text)
	}
	// ...and a message with several values cannot take a text.
	wantError(t, send(t, many, message(`{"text":"1"}`, `"metadata":{"skill":"sum"}`)), codeInvalidParams, "takes several values")
	// Data without a skill is the values, when the metadata names the skill.
	b := send(t, many, message(`{"data":{"text":"from data"}}`, `"metadata":{"skill":"echo"}`))
	if text, _ := reply(t, b); text != "echo: from data" {
		t.Errorf("text = %q", text)
	}
}

func TestAMessageThatIsNotUnderstoodIsRefusedWithAReason(t *testing.T) {
	s := newTestServer(t, nil)
	nine := strings.TrimSuffix(strings.Repeat(`{"text":"x"},`, 9), ",")
	manyArgs := make([]string, 40)
	for i := range manyArgs {
		manyArgs[i] = fmt.Sprintf(`"k%d":1`, i)
	}
	for name, tt := range map[string]struct {
		params string
		code   int
		text   string
	}{
		"no message":      {`{}`, codeInvalidParams, "needs a message"},
		"not an object":   {`"x"`, codeInvalidParams, "needs a message"},
		"wrong role":      {`{"message":{"role":"ROLE_AGENT","parts":[{"text":"x"}]}}`, codeInvalidParams, "ROLE_USER"},
		"no role":         {`{"message":{"parts":[{"text":"x"}]}}`, codeInvalidParams, "ROLE_USER"},
		"no parts":        {message("", ""), codeInvalidParams, "between 1 and 8"},
		"nine parts":      {message(nine, ""), codeInvalidParams, "between 1 and 8"},
		"text and data":   {message(`{"text":"x","data":{"a":1}}`, ""), codeInvalidParams, "either text or data"},
		"empty part":      {message(`{}`, ""), codeInvalidParams, "either text or data"},
		"a file":          {message(`{"raw":"AAAA"}`, ""), codeContentType, "files are not supported"},
		"an address":      {message(`{"url":"http://x"}`, ""), codeContentType, "files are not supported"},
		"two data":        {message(dataPart("echo", `{}`)+","+dataPart("echo", `{}`), ""), codeInvalidParams, "one data part"},
		"data not object": {message(`{"data":[1,2]}`, ""), codeInvalidParams, "has to be an object"},
		"skill not text":  {message(`{"data":{"skill":5}}`, ""), codeInvalidParams, "`skill` has to be a text"},
		"args not object": {message(`{"data":{"skill":"echo","arguments":[1]}}`, ""), codeInvalidParams, "`arguments` has to be an object"},
		"unknown skill":   {message(dataPart("nope", `{}`), ""), codeInvalidParams, "handles: boom, chain, count, echo"},
		"many arguments":  {message(dataPart("echo", `{`+strings.Join(manyArgs, ",")+`}`), ""), codeInvalidParams, "too many values"},
		"bad name":        {message(dataPart("echo", `{"te xt":"x"}`), ""), codeInvalidParams, "name that is not valid"},
		"a task":          {message(dataPart("echo", `{"text":"x"}`), `"taskId":"task-1"`), codeTaskNotFound, "keeps no tasks"},
	} {
		a := send(t, s, tt.params)
		if a.Error == nil || a.Error.Code != tt.code || !strings.Contains(a.Error.Message, tt.text) {
			t.Errorf("%s: got %s, want %d %q", name, a.Raw, tt.code, tt.text)
		}
	}
	// Whatever is refused, no conversation is left behind.
	if s.contexts.Len() != 0 {
		t.Errorf("%d conversations were opened by requests that were refused", s.contexts.Len())
	}
}

func TestAnUnknownSkillNameIsNotRepeatedWithoutLimit(t *testing.T) {
	s := newTestServer(t, nil)
	long := strings.Repeat("A", 5000) + `\nsecond line` // the \n is a JSON escape, so the JSON stays valid
	a := send(t, s, message(dataPart(long, `{}`), ""))
	if a.Error == nil || len(a.Error.Message) > 400 || strings.Contains(a.Error.Message, "\n") {
		t.Errorf("answer of %d bytes: %s", len(a.Raw), a.Raw)
	}
}

// ---------- the chain of agents (D2) ----------

// req: D2
func TestTheChainOfAgentsIsHandedToTheAgent(t *testing.T) {
	s := newTestServer(t, nil)
	a := send(t, s, message(dataPart("chain", `{}`), `"metadata":{"metagente":{"chain":["Planner","Helper"]}}`))
	if text, _ := reply(t, a); text != "Planner>Helper" {
		t.Errorf("the agent saw the chain %q", text)
	}
}

// req: D2
func TestCallsGoingTooDeepOrInACircleAreStopped(t *testing.T) {
	s := newTestServer(t, func(c *Config) { c.MaxCallDepth = 3 })
	deep := send(t, s, message(dataPart("chain", `{}`), `"metadata":{"metagente":{"chain":["A","B","C"]}}`))
	wantError(t, deep, codeServer, "too deep")
	circle := send(t, s, message(dataPart("chain", `{}`), `"metadata":{"metagente":{"chain":["A","Bob"]}}`))
	wantError(t, circle, codeServer, "in a circle")
	if text, _ := reply(t, send(t, s, message(dataPart("chain", `{}`), `"metadata":{"metagente":{"chain":["A","B"]}}`))); text != "A>B" {
		t.Errorf("a chain inside the limit was refused: %q", text)
	}
	for name, chain := range map[string]string{
		"not a list":  `"A"`,
		"not texts":   `[1,2]`,
		"odd names":   `["A B"]`,
		"empty name":  `[""]`,
		"a long name": `["` + strings.Repeat("a", 100) + `"]`,
		"too many":    `[` + strings.TrimSuffix(strings.Repeat(`"A",`, 33), ",") + `]`,
	} {
		a := send(t, s, message(dataPart("chain", `{}`), `"metadata":{"metagente":{"chain":`+chain+`}}`))
		if a.Error == nil || a.Error.Code != codeInvalidParams {
			t.Errorf("%s: %s", name, a.Raw)
		}
	}
	if s.contexts.Len() != 1 {
		t.Errorf("%d conversations; only the one that worked should exist", s.contexts.Len())
	}
}

// ---------- failures (P1, P4) ----------

// req: P1
func TestAnAgentThatFailsAnswersWithATaskThatFailedAndNothingOfThisComputer(t *testing.T) {
	s := newTestServer(t, nil)
	a := send(t, s, message(dataPart("fail", `{}`), ""))
	if a.Error != nil {
		t.Fatalf("a failed agent is not a failed request: %s", a.Raw)
	}
	task := a.Result["task"].(map[string]any)
	status := task["status"].(map[string]any)
	text := status["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"].(string)
	if status["state"] != "TASK_STATE_FAILED" || !strings.HasPrefix(task["id"].(string), "task-") || !strings.HasPrefix(task["contextId"].(string), "ctx-") {
		t.Errorf("task = %v", task)
	}
	if !strings.Contains(text, "the order is missing") || !strings.Contains(text, "add the order") {
		t.Errorf("the reason was lost: %q", text)
	}
	for _, leak := range []string{"/home/secret", "bob.ag", "secret line", "other.ag", "line 7"} {
		if strings.Contains(a.Raw, leak) {
			t.Errorf("the answer tells %q:\n%s", leak, a.Raw)
		}
	}
}

func TestAnAgentThatCannotStartLeavesNoConversationBehind(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	bob.startErr = diag.New("the start section failed").Fix("fix it")
	s := newTestServer(t, nil, bob)
	a := send(t, s, message(dataPart("echo", `{"text":"x"}`), ""))
	task := a.Result["task"].(map[string]any)
	if task["status"].(map[string]any)["state"] != "TASK_STATE_FAILED" || task["contextId"] != nil {
		t.Errorf("task = %v", task)
	}
	if s.contexts.Len() != 0 {
		t.Errorf("%d conversations", s.contexts.Len())
	}
}

// req: P4, P2
func TestAPanicInAnAgentDoesNotTakeTheServerDownOrShowItsCause(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	s := newTestServer(t, func(c *Config) { c.Log = applog.New(dir) })
	rec := do(s, http.MethodPost, bobPath, rpcBody("SendMessage", message(dataPart("boom", `{}`), "")), nil)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "kaboom") ||
		!strings.Contains(rec.Body.String(), "something went wrong inside the server") {
		t.Errorf("code %d: %q", rec.Code, rec.Body.String())
	}
	raw, err := os.ReadFile(filepath.Join(dir, "metagente.log"))
	if err != nil || !strings.Contains(string(raw), "PANIC http POST: kaboom") {
		t.Errorf("the cause is not in the log: %v\n%s", err, raw)
	}
	// The server goes on, and the conversation that broke can be used again.
	if text, _ := reply(t, send(t, s, message(dataPart("echo", `{"text":"still here"}`), ""))); text != "echo: still here" {
		t.Errorf("text = %q", text)
	}
}

func TestAnAgentThatTakesTooLongAnswersThatItDid(t *testing.T) {
	s := newTestServer(t, func(c *Config) { c.RequestTimeout = 40 * time.Millisecond })
	a := send(t, s, message(dataPart("slow", `{}`), ""))
	task := a.Result["task"].(map[string]any)
	text := task["status"].(map[string]any)["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)["text"]
	if text != "the agent took too long to answer" {
		t.Errorf("text = %v", text)
	}
}

// req: S6
func TestWhenTheServerIsBusyItAnswers503InsteadOfPilingUp(t *testing.T) {
	s := newTestServer(t, func(c *Config) { c.MaxInFlight = 1 })
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodPost, bobPath, strings.NewReader(rpcBody("SendMessage", message(dataPart("slow", `{}`), ""))))
		req = req.WithContext(ctx)
		req.Host = "127.0.0.1:8080"
		req.Header.Set("Authorization", "Bearer "+goodToken)
		req.Header.Set("Content-Type", "application/json")
		close(started)
		s.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-started
	deadline := time.Now().Add(2 * time.Second)
	var busy *httptest.ResponseRecorder
	for time.Now().Before(deadline) {
		rec := do(s, http.MethodPost, bobPath, rpcBody("SendMessage", message(dataPart("echo", `{"text":"x"}`), "")), nil)
		if rec.Code == http.StatusServiceUnavailable {
			busy = rec
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	if busy == nil {
		t.Fatal("the server never answered 503 although its only place was taken")
	}
	if busy.Header().Get("Retry-After") != "1" || !strings.Contains(busy.Body.String(), "busy") {
		t.Errorf("headers %v, body %q", busy.Header(), busy.Body.String())
	}
	if strings.Contains(busy.Body.String(), "jsonrpc") {
		t.Error("a 503 was dressed as an answer of the protocol")
	}
}

// ---------- the other methods ----------

func TestTheMethodsThatAreNotOfferedSayWhy(t *testing.T) {
	s := newTestServer(t, nil)
	for method, tt := range map[string]struct {
		code int
		text string
	}{
		"GetTask":                          {codeTaskNotFound, "keeps no tasks"},
		"CancelTask":                       {codeTaskNotFound, "keeps no tasks"},
		"SendStreamingMessage":             {codeUnsupported, "does not stream"},
		"SubscribeToTask":                  {codeUnsupported, "does not stream"},
		"CreateTaskPushNotificationConfig": {codePushUnsupported, "push notifications"},
		"GetTaskPushNotificationConfig":    {codePushUnsupported, "push notifications"},
		"ListTaskPushNotificationConfig":   {codePushUnsupported, "push notifications"},
		"DeleteTaskPushNotificationConfig": {codePushUnsupported, "push notifications"},
		"GetExtendedAgentCard":             {codeNoExtendedCard, "no extended"},
		"tasks/get":                        {codeMethodNotFound, "method not found"},
		"anything":                         {codeMethodNotFound, "method not found"},
	} {
		a := decodeAnswer(t, do(s, http.MethodPost, bobPath, rpcBody(method, `{"id":"x"}`), nil))
		wantError(t, a, tt.code, tt.text)
	}
	list := decodeAnswer(t, do(s, http.MethodPost, bobPath, rpcBody("ListTasks", `{}`), nil))
	if list.Error != nil || len(list.Result["tasks"].([]any)) != 0 {
		t.Errorf("ListTasks: %s", list.Raw)
	}
}

func TestARequestThatIsNotJSONRPCIsRefusedWithTheRightCode(t *testing.T) {
	s := newTestServer(t, nil)
	good := message(dataPart("echo", `{"text":"x"}`), "")
	for name, tt := range map[string]struct {
		body string
		code int
	}{
		"a batch":      {`[` + rpcBody("SendMessage", good) + `]`, codeInvalidRequest},
		"not json":     {`{"jsonrpc":`, codeParse},
		"empty":        {``, codeParse},
		"no id":        {`{"jsonrpc":"2.0","method":"SendMessage","params":` + good + `}`, codeInvalidRequest},
		"an id object": {`{"jsonrpc":"2.0","id":{"a":1},"method":"SendMessage","params":` + good + `}`, codeInvalidRequest},
		"an id list":   {`{"jsonrpc":"2.0","id":[1],"method":"SendMessage","params":` + good + `}`, codeInvalidRequest},
		"an id true":   {`{"jsonrpc":"2.0","id":true,"method":"SendMessage","params":` + good + `}`, codeInvalidRequest},
		"a long id":    {`{"jsonrpc":"2.0","id":"` + strings.Repeat("a", 300) + `","method":"SendMessage","params":` + good + `}`, codeInvalidRequest},
		"version 1":    {`{"jsonrpc":"1.0","id":1,"method":"SendMessage","params":` + good + `}`, codeInvalidRequest},
		"no method":    {`{"jsonrpc":"2.0","id":1,"params":` + good + `}`, codeInvalidRequest},
		"no params":    {`{"jsonrpc":"2.0","id":1,"method":"SendMessage"}`, codeInvalidParams},
	} {
		a := decodeAnswer(t, do(s, http.MethodPost, bobPath, tt.body, nil))
		if a.Error == nil || a.Error.Code != tt.code {
			t.Errorf("%s: %s, want code %d", name, a.Raw, tt.code)
		}
	}
	// The id of a good request comes back as it was sent.
	for _, id := range []string{`"abc"`, `7`, `null`, `1.5`} {
		body := `{"jsonrpc":"2.0","id":` + id + `,"method":"GetTask","params":{}}`
		if a := decodeAnswer(t, do(s, http.MethodPost, bobPath, body, nil)); string(a.ID) != id {
			t.Errorf("id %s came back as %s", id, a.ID)
		}
	}
}

// ---------- the door, in front of all of it ----------

// req: S1, S2, S3, S4
func TestNothingReachesAnAgentThroughTheDoorWithoutPassingIt(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	s := newTestServer(t, nil, bob)
	body := rpcBody("SendMessage", message(dataPart("echo", `{"text":"x"}`), ""))
	for name, tt := range map[string]struct {
		mutate func(*http.Request)
		code   int
	}{
		"no token":    {func(r *http.Request) { r.Header.Del("Authorization") }, 401},
		"wrong token": {func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") }, 401},
		"other host":  {func(r *http.Request) { r.Host = "evil.example" }, 421},
		"a browser":   {func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }, 403},
		"a form":      {func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, 415},
		"a query":     {func(r *http.Request) { r.URL.RawQuery = "x=1" }, 400},
		"a preflight": {func(r *http.Request) { r.Method = http.MethodOptions }, 405},
	} {
		rec := do(s, http.MethodPost, bobPath, body, tt.mutate)
		if rec.Code != tt.code {
			t.Errorf("%s: code %d, want %d", name, rec.Code, tt.code)
		}
	}
	if begun, _ := bob.counts(); begun != 0 {
		t.Errorf("%d conversations began for requests that were refused", begun)
	}
}

// req: S6
func TestABodyOverTheLimitIsNotRead(t *testing.T) {
	s := newTestServer(t, func(c *Config) { c.MaxBody = 300 })
	big := rpcBody("SendMessage", message(`{"text":"`+strings.Repeat("x", 1000)+`"}`, ""))
	if rec := do(s, http.MethodPost, bobPath, big, nil); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("code %d", rec.Code)
	}
	lying := do(s, http.MethodPost, bobPath, big, func(r *http.Request) { r.ContentLength = -1 })
	if lying.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a body of unknown length: code %d", lying.Code)
	}
}

// ---------- conversations (S7, S9) ----------

// req: S9
func TestAConversationThatIsNotUsedIsLetGoAndItsIdStopsWorking(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	s := newTestServer(t, nil, bob)
	clock := &fakeClock{now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	s.contexts.now = clock.Now
	_, ctxA := reply(t, send(t, s, message(dataPart("count", `{}`), "")))
	clock.Advance(time.Hour)
	if removed := s.contexts.Sweep(); removed != 1 {
		t.Fatalf("swept %d", removed)
	}
	if _, closed := bob.counts(); closed != 1 {
		t.Errorf("%d conversations were closed, want 1", closed)
	}
	wantError(t, send(t, s, message(dataPart("count", `{}`), `"contextId":"`+ctxA+`"`)), codeInvalidParams, "unknown or expired conversation")
}

// req: S9
func TestThereIsALimitOfConversationsAndItIsSaid(t *testing.T) {
	s := newTestServer(t, func(c *Config) { c.MaxConversations = 2 })
	for i := 0; i < 2; i++ {
		reply(t, send(t, s, message(dataPart("count", `{}`), "")))
	}
	wantError(t, send(t, s, message(dataPart("count", `{}`), "")), codeServer, "as many conversations as it may")
}

func TestStoppingTheServerLetsGoOfEveryConversation(t *testing.T) {
	bob := newFake("Bob", defaultSkills...)
	s, err := New(Config{Token: goodToken, Hosts: LoopbackHosts(8080)}, []Agent{bob})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		reply(t, send(t, s, message(dataPart("count", `{}`), "")))
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	stop()
	<-done
	if _, closed := bob.counts(); closed != 3 {
		t.Errorf("%d conversations were closed, want 3", closed)
	}
}

// ---------- a setup that is not good enough ----------

func TestAServerIsNotMadeWithAWeakSetup(t *testing.T) {
	good := Config{Token: goodToken, Hosts: LoopbackHosts(8080)}
	bob := Agent(newFake("Bob"))
	for name, tt := range map[string]struct {
		cfg    Config
		agents []Agent
		want   string
	}{
		"short token":   {Config{Token: "short", Hosts: good.Hosts}, []Agent{bob}, "at least 32"},
		"no token":      {Config{Hosts: good.Hosts}, []Agent{bob}, "at least 32"},
		"no hosts":      {Config{Token: goodToken}, []Agent{bob}, "hosts"},
		"no agents":     {good, nil, "no agent"},
		"a twin":        {good, []Agent{bob, newFake("Bob")}, "two agents are called Bob"},
		"a bad name":    {good, []Agent{newFake("a/b")}, "cannot be used in an address"},
		"an empty name": {good, []Agent{newFake("")}, "cannot be used in an address"},
	} {
		_, err := New(tt.cfg, tt.agents)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v", name, err)
		}
		if err != nil && strings.Contains(err.Error(), "short") && name == "short token" && strings.Contains(err.Error(), tt.cfg.Token+"x") {
			t.Errorf("%s: the token is in the error", name)
		}
	}
}

// asDiag and renderedText let the tests of the pair read a problem as the person
// would.
func asDiag(err error) (*diag.Diagnostic, bool) { return diag.From(err) }

func renderedText(err error) string {
	if d, ok := diag.From(err); ok {
		return d.Render()
	}
	return err.Error()
}

// req: S8
func TestTheCardDoesNotChangeWithTheHostOfTheRequest(t *testing.T) {
	cardURL := func(s *Server, host string) string {
		rec := do(s, http.MethodGet, bobPath+"/.well-known/agent-card.json", "", func(r *http.Request) { r.Host = host })
		if rec.Code != http.StatusOK {
			t.Fatalf("host %s: code %d", host, rec.Code)
		}
		var card struct {
			Interfaces []struct {
				URL string `json:"url"`
			} `json:"supportedInterfaces"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &card); err != nil || len(card.Interfaces) != 1 {
			t.Fatalf("card: %v %s", err, rec.Body.String())
		}
		return card.Interfaces[0].URL
	}
	s := newTestServer(t, nil)
	for _, host := range LoopbackHosts(8080) { // all three are names the server answers to
		if got := cardURL(s, host); got != "http://127.0.0.1:8080/agents/Bob" {
			t.Errorf("asked as %s, the card says %s", host, got)
		}
	}
	proxied := newTestServer(t, func(c *Config) { c.BaseURL = "https://agents.example.com/" })
	for _, host := range LoopbackHosts(8080) {
		if got := cardURL(proxied, host); got != "https://agents.example.com/agents/Bob" {
			t.Errorf("asked as %s, the card says %s", host, got)
		}
	}
}
