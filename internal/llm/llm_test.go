package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"metagente/internal/config"
	"metagente/internal/diag"
)

const testKey = "sk-test-123456"

func noWait(int, time.Duration) time.Duration { return 0 }

// capture is a server that remembers the last request and answers with a fixed
// status and body.
type capture struct {
	server  *httptest.Server
	path    string
	headers http.Header
	body    map[string]any
	calls   atomic.Int32
}

func newCapture(t *testing.T, status int, answer string) *capture {
	t.Helper()
	c := &capture{}
	c.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.calls.Add(1)
		c.path = r.URL.Path
		c.headers = r.Header.Clone()
		raw, _ := io.ReadAll(r.Body)
		c.body = map[string]any{}
		_ = json.Unmarshal(raw, &c.body)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer)
	}))
	t.Cleanup(c.server.Close)
	return c
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

func sampleRequest() *Request {
	return &Request{
		System:    "You are helpful.",
		MaxTokens: 99,
		Cache:     true,
		Tools: []Tool{{Name: "file__read", Description: "Read a file",
			Schema: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}},
		Messages: []Message{
			{Role: RoleUser, Parts: []Part{{Kind: PartText, Text: "read a.txt"}}},
			{Role: RoleAssistant, Parts: []Part{
				{Kind: PartText, Text: "Let me look."},
				{Kind: PartToolUse, ID: "call_1", Name: "file__read", Input: map[string]any{"path": "a.txt"}},
			}},
			{Role: RoleUser, Parts: []Part{{Kind: PartToolResult, ID: "call_1", Text: "no such file", IsError: true}}},
		},
	}
}

const anthropicAnswer = `{"id":"msg_1","type":"message","role":"assistant","model":"m",
 "content":[{"type":"text","text":"Hello "},{"type":"text","text":"there"},
            {"type":"tool_use","id":"toolu_9","name":"file__read","input":{"path":"b.txt"}}],
 "stop_reason":"tool_use",
 "usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":80,"cache_creation_input_tokens":5}}`

// ---------- Anthropic ----------

func TestTheAnthropicRequestHasTheShapeTheAPIExpects(t *testing.T) {
	c := newCapture(t, 200, anthropicAnswer)
	model, err := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatal(rendered(t, err))
	}
	if c.path != "/v1/messages" {
		t.Errorf("path = %s", c.path)
	}
	checkAnthropicHeaders(t, c.headers)
	checkAnthropicSettings(t, c.body)
	checkAnthropicMessages(t, c.body["messages"].([]any))
}

func checkAnthropicHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	if headers.Get("x-api-key") != testKey || headers.Get("anthropic-version") != "2023-06-01" ||
		headers.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", headers)
	}
}

// checkAnthropicSettings looks at the model, the limit, the system prompt and the tool.
func checkAnthropicSettings(t *testing.T, body map[string]any) {
	t.Helper()
	if body["model"] != "m" || body["max_tokens"] != float64(99) {
		t.Errorf("model/max_tokens = %v / %v", body["model"], body["max_tokens"])
	}
	system := body["system"].([]any)[0].(map[string]any)
	if system["text"] != "You are helpful." || system["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Errorf("system = %v", system)
	}
	tool := body["tools"].([]any)[0].(map[string]any)
	if tool["name"] != "file__read" || tool["input_schema"] == nil {
		t.Errorf("tool = %v", tool)
	}
}

// checkAnthropicMessages looks at the request for a tool, at its result, and at the mark for the cache.
func checkAnthropicMessages(t *testing.T, messages []any) {
	t.Helper()
	if len(messages) != 3 {
		t.Fatalf("got %d messages", len(messages))
	}
	assistant := messages[1].(map[string]any)["content"].([]any)
	if use := assistant[1].(map[string]any); use["type"] != "tool_use" || use["id"] != "call_1" ||
		use["input"].(map[string]any)["path"] != "a.txt" {
		t.Errorf("tool_use block = %v", use)
	}
	lastBlock := messages[2].(map[string]any)["content"].([]any)[0].(map[string]any)
	if lastBlock["type"] != "tool_result" || lastBlock["tool_use_id"] != "call_1" ||
		lastBlock["is_error"] != true || lastBlock["content"] != "no such file" {
		t.Errorf("tool_result block = %v", lastBlock)
	}
	if lastBlock["cache_control"] == nil {
		t.Error("the end of the conversation was not marked for the cache")
	}
}

func TestWithoutTheCacheNothingIsMarked(t *testing.T) {
	c := newCapture(t, 200, anthropicAnswer)
	model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	req := sampleRequest()
	req.Cache = false
	if _, err := model.Complete(context.Background(), req); err != nil {
		t.Fatal(rendered(t, err))
	}
	if strings.Contains(mustJSON(t, c.body), "cache_control") {
		t.Error("cache_control was sent although the cache is off")
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestAnEmptyTextIsNotSentBecauseTheAPIRefusesIt(t *testing.T) {
	c := newCapture(t, 200, anthropicAnswer)
	model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	req := &Request{MaxTokens: 10, Messages: []Message{{Role: RoleUser, Parts: []Part{
		{Kind: PartText, Text: ""}, {Kind: PartText, Text: "hi"}}}}}
	if _, err := model.Complete(context.Background(), req); err != nil {
		t.Fatal(rendered(t, err))
	}
	blocks := c.body["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(blocks) != 1 {
		t.Errorf("got %d blocks, want 1", len(blocks))
	}
}

func TestTheAnthropicAnswerIsRead(t *testing.T) {
	c := newCapture(t, 200, anthropicAnswer)
	model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	resp, err := model.Complete(context.Background(), sampleRequest())
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if resp.Text() != "Hello there" {
		t.Errorf("text = %q", resp.Text())
	}
	uses := resp.ToolUses()
	if len(uses) != 1 || uses[0].ID != "toolu_9" || uses[0].Name != "file__read" || uses[0].Input["path"] != "b.txt" {
		t.Errorf("tool uses = %+v", uses)
	}
	if resp.Stop != StopToolUse {
		t.Errorf("stop = %v", resp.Stop)
	}
	want := Usage{Input: 100, Output: 20, CacheRead: 80, CacheWrite: 5}
	if resp.Usage != want || resp.Usage.Total() != 120 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestEveryReasonToStopHasAMeaning(t *testing.T) {
	for reason, want := range map[string]Stop{
		"end_turn": StopEnd, "stop_sequence": StopEnd, "tool_use": StopToolUse,
		"max_tokens": StopLength, "model_context_window_exceeded": StopLength,
		"refusal": StopRefusal, "pause_turn": StopOther, "something_new": StopOther,
	} {
		c := newCapture(t, 200, `{"content":[{"type":"text","text":"x"}],"stop_reason":"`+reason+`"}`)
		model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
		resp, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
		if err != nil {
			t.Fatal(rendered(t, err))
		}
		if resp.Stop != want || resp.Reason != reason {
			t.Errorf("%s: stop %v reason %q, want %v", reason, resp.Stop, resp.Reason, want)
		}
	}
}

func TestToolArgumentsThatAreNotAnObjectAreFlagged(t *testing.T) {
	c := newCapture(t, 200, `{"content":[{"type":"tool_use","id":"t","name":"n","input":"nope"}],"stop_reason":"tool_use"}`)
	model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	resp, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if uses := resp.ToolUses(); len(uses) != 1 || !uses[0].BadInput {
		t.Errorf("uses = %+v", uses)
	}
}

// ---------- failures ----------

// req: L8
func TestAnErrorIsExplainedAndTheKeyNeverAppearsInIt(t *testing.T) {
	c := newCapture(t, 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key: `+testKey+`"}}`)
	model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	text := rendered(t, err)
	mustContain(t, text, "answered 401", "invalid x-api-key", "[hidden]")
	if strings.Contains(text, testKey) {
		t.Errorf("the key is in the error:\n%s", text)
	}
	if c.calls.Load() != 1 {
		t.Errorf("a refused key was tried %d times", c.calls.Load())
	}
}

func TestAnErrorInTheOpenAIShapeIsReadToo(t *testing.T) {
	for _, answer := range []string{
		`{"error":{"message":"model not found","type":"invalid_request_error"}}`,
		`{"error":"model not found"}`,
		`{"message":"model not found"}`,
	} {
		c := newCapture(t, 404, answer)
		model, _ := NewOpenAI(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
		_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
		mustContain(t, rendered(t, err), "answered 404: model not found")
	}
}

func TestAFailureThatMayPassIsTriedAgain(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(529)
			_, _ = io.WriteString(w, `{"error":{"message":"overloaded"}}`)
			return
		}
		_, _ = io.WriteString(w, anthropicAnswer)
	}))
	defer server.Close()
	var hint time.Duration
	model, _ := NewAnthropic(Settings{BaseURL: server.URL, Model: "m", APIKey: testKey,
		Wait: func(_ int, h time.Duration) time.Duration { hint = h; return 0 }})
	resp, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if calls.Load() != 2 || resp.Text() != "Hello there" {
		t.Errorf("calls = %d, text = %q", calls.Load(), resp.Text())
	}
	if hint != time.Second {
		t.Errorf("the pause the server asked for was not offered: %v", hint)
	}
}

func TestARequestThatIsWrongIsNotTriedAgain(t *testing.T) {
	c := newCapture(t, 400, `{"error":{"message":"max_tokens is too large"}}`)
	model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey, Wait: noWait})
	_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	mustContain(t, rendered(t, err), "answered 400: max_tokens is too large")
	if c.calls.Load() != 1 {
		t.Errorf("a wrong request was sent %d times", c.calls.Load())
	}
}

func TestTheNumberOfTriesIsLimited(t *testing.T) {
	c := newCapture(t, 503, `{"error":{"message":"down"}}`)
	model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey, Wait: noWait, Retries: 1})
	_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	mustContain(t, rendered(t, err), "answered 503")
	if c.calls.Load() != 2 {
		t.Errorf("sent %d times, want 2 (one try and one more)", c.calls.Load())
	}
	c2 := newCapture(t, 503, `{}`)
	never, _ := NewAnthropic(Settings{BaseURL: c2.server.URL, Model: "m", APIKey: testKey, Wait: noWait, Retries: -1})
	_, _ = never.Complete(context.Background(), &Request{MaxTokens: 1})
	if c2.calls.Load() != 1 {
		t.Errorf("with no retries it was sent %d times", c2.calls.Load())
	}
}

// req: T3
func TestARedirectIsNeverFollowedSoTheKeyCannotBeSentElsewhere(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer other.Close()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer provider.Close()

	model, _ := NewAnthropic(Settings{BaseURL: provider.URL, Model: "m", APIKey: testKey, Wait: noWait})
	_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	mustContain(t, rendered(t, err), "redirected the request", "never follows a redirect")
	if elsewhere.Load() != 0 {
		t.Error("the request, with the key, was sent to the address of the redirect")
	}
}

func TestAnAnswerLargerThanTheLimitIsRefused(t *testing.T) {
	c := newCapture(t, 200, strings.Repeat("x", 500))
	model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey, MaxResponse: 100})
	_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	mustContain(t, rendered(t, err), "is larger than 100 bytes")
}

func TestGivingUpStopsTheWaitForTheModel(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	model, _ := NewAnthropic(Settings{BaseURL: server.URL, Model: "m", APIKey: testKey, Wait: noWait})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	_, err := model.Complete(ctx, &Request{MaxTokens: 1})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
}

func TestAnUnreachableAddressSaysSoWithoutRepeatingTheRequest(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	address := server.URL
	server.Close() // nobody listens there now
	model, _ := NewAnthropic(Settings{BaseURL: address, Model: "m", APIKey: testKey, Wait: noWait, Retries: -1})
	_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	text := rendered(t, err)
	mustContain(t, text, "I could not reach 127.0.0.1")
	if strings.Contains(text, testKey) {
		t.Error("the key is in the error")
	}
}

// ---------- addresses ----------

// req: T3
func TestTheKeyOnlyTravelsToAnAddressThatIsSafe(t *testing.T) {
	for _, tt := range []struct{ address, problem string }{
		{"https://api.example.com", ""},
		{"https://api.example.com/v1/", ""},
		{"http://localhost:11434", ""},
		{"http://127.0.0.1:8080/v1", ""},
		{"http://[::1]:8080", ""},
		{"http://api.example.com", "without encryption"},
		{"http://10.0.0.5:8080", "without encryption"},
		{"https://user:pass@api.example.com", "may not hold a user name"},
		{"https://api.example.com/?token=x", "may not hold a user name, a query or a fragment"},
		{"ftp://api.example.com", "is not valid"},
		{"not an address", "is not valid"},
	} {
		_, err := parseBase(tt.address)
		switch {
		case tt.problem == "" && err != nil:
			t.Errorf("%s was refused: %s", tt.address, rendered(t, err))
		case tt.problem != "" && err == nil:
			t.Errorf("%s was accepted", tt.address)
		case tt.problem != "":
			mustContain(t, rendered(t, err), tt.problem)
		}
	}
}

func TestTheAddressOfTheRequestIsBuiltFromTheBase(t *testing.T) {
	for _, tt := range []struct{ base, anthropic, openai string }{
		{"https://x.example", "https://x.example/v1/messages", "https://x.example/v1/chat/completions"},
		{"https://x.example/", "https://x.example/v1/messages", "https://x.example/v1/chat/completions"},
		{"https://x.example/v1", "https://x.example/v1/messages", "https://x.example/v1/chat/completions"},
		{"https://x.example/api", "https://x.example/api/v1/messages", "https://x.example/api/v1/chat/completions"},
	} {
		a, err := NewAnthropic(Settings{BaseURL: tt.base})
		if err != nil {
			t.Fatal(err)
		}
		o, err := NewOpenAI(Settings{BaseURL: tt.base})
		if err != nil {
			t.Fatal(err)
		}
		if got := a.(*anthropic).address; got != tt.anthropic {
			t.Errorf("anthropic %s -> %s, want %s", tt.base, got, tt.anthropic)
		}
		if got := o.(*openAI).address; got != tt.openai {
			t.Errorf("openai %s -> %s, want %s", tt.base, got, tt.openai)
		}
	}
}

// ---------- OpenAI chat format ----------

const openAIToolAnswer = `{"choices":[{"message":{"role":"assistant","content":null,
  "tool_calls":[{"id":"call_7","type":"function","function":{"name":"file__read","arguments":"{\"path\":\"c.txt\"}"}}]},
  "finish_reason":"tool_calls"}],
  "usage":{"prompt_tokens":50,"completion_tokens":7,"prompt_tokens_details":{"cached_tokens":30}}}`

func TestTheOpenAIRequestHasTheShapeOfTheChatFormat(t *testing.T) {
	c := newCapture(t, 200, openAIToolAnswer)
	model, _ := NewOpenAI(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	if _, err := model.Complete(context.Background(), sampleRequest()); err != nil {
		t.Fatal(rendered(t, err))
	}
	if c.path != "/v1/chat/completions" || c.headers.Get("Authorization") != "Bearer "+testKey {
		t.Errorf("path %s, authorization %q", c.path, c.headers.Get("Authorization"))
	}
	body := c.body
	if body["max_tokens"] != float64(99) || body["max_completion_tokens"] != nil {
		t.Errorf("a server on this machine expects max_tokens: %v", body)
	}
	messages := body["messages"].([]any)
	roles := make([]string, len(messages))
	for i, m := range messages {
		roles[i] = m.(map[string]any)["role"].(string)
	}
	if strings.Join(roles, " ") != "system user assistant tool" {
		t.Fatalf("roles = %v", roles)
	}
	assistant := messages[2].(map[string]any)
	call := assistant["tool_calls"].([]any)[0].(map[string]any)
	function := call["function"].(map[string]any)
	if call["id"] != "call_1" || function["name"] != "file__read" || function["arguments"] != `{"path":"a.txt"}` {
		t.Errorf("tool call = %v", call)
	}
	tool := messages[3].(map[string]any)
	if tool["tool_call_id"] != "call_1" || tool["content"] != "error: no such file" {
		t.Errorf("tool message = %v", tool)
	}
	function2 := body["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if function2["name"] != "file__read" || function2["parameters"] == nil {
		t.Errorf("tool = %v", body["tools"])
	}
}

func TestTheNameOfTheLimitFollowsTheServer(t *testing.T) {
	for _, tt := range []struct{ configured, address, want string }{
		{"", "https://api.openai.com", "max_completion_tokens"},
		{"", "https://my-resource.openai.azure.com/openai/v1", "max_completion_tokens"},
		{"", "https://openrouter.ai/api", "max_tokens"},
		{"", "http://localhost:11434", "max_tokens"},
		{"max_completion_tokens", "http://localhost:11434", "max_completion_tokens"},
		{"max_tokens", "https://api.openai.com", "max_tokens"},
	} {
		u, _ := url.Parse(tt.address)
		if got := maxTokensField(tt.configured, u); got != tt.want {
			t.Errorf("%q at %s -> %s, want %s", tt.configured, tt.address, got, tt.want)
		}
	}
}

func TestTheOpenAIAnswerIsRead(t *testing.T) {
	c := newCapture(t, 200, openAIToolAnswer)
	model, _ := NewOpenAI(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	resp, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	uses := resp.ToolUses()
	if len(uses) != 1 || uses[0].ID != "call_7" || uses[0].Name != "file__read" || uses[0].Input["path"] != "c.txt" {
		t.Errorf("uses = %+v", uses)
	}
	if resp.Stop != StopToolUse || resp.Usage != (Usage{Input: 50, Output: 7, CacheRead: 30}) {
		t.Errorf("stop %v usage %+v", resp.Stop, resp.Usage)
	}
}

func TestOddButCommonAnswersOfCompatibleServersAreAccepted(t *testing.T) {
	for name, tt := range map[string]struct {
		answer string
		check  oddAnswerCheck
	}{
		"arguments as an object": {
			`{"choices":[{"message":{"content":"","tool_calls":[{"id":"a","function":{"name":"n","arguments":{"path":"d.txt"}}}]},"finish_reason":"tool_calls"}]}`,
			wantOneToolUse(false, "d.txt"),
		},
		"arguments that are not json": {
			`{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"n","arguments":"{oops"}}]},"finish_reason":"tool_calls"}]}`,
			wantOneToolUse(true, ""),
		},
		"no arguments at all": {
			`{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"n","arguments":""}}]},"finish_reason":"tool_calls"}]}`,
			wantOneToolUse(false, ""),
		},
		"stop with tool calls": {
			`{"choices":[{"message":{"tool_calls":[{"id":"a","function":{"name":"n","arguments":"{}"}}]},"finish_reason":"stop"}]}`,
			wantStop(StopToolUse),
		},
		"content as parts": {
			`{"choices":[{"message":{"content":[{"type":"text","text":"he"},{"type":"text","text":"llo"}]},"finish_reason":"stop"}]}`,
			wantTextAndStop("hello", StopEnd),
		},
		"cut": {
			`{"choices":[{"message":{"content":"half"},"finish_reason":"length"}]}`,
			wantStop(StopLength),
		},
		"refusal": {
			`{"choices":[{"message":{"content":null,"refusal":"I cannot help with that"},"finish_reason":"stop"}]}`,
			wantStop(StopRefusal),
		},
		"content filter": {
			`{"choices":[{"message":{"content":""},"finish_reason":"content_filter"}]}`,
			wantStop(StopRefusal),
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := newCapture(t, 200, tt.answer)
			model, _ := NewOpenAI(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
			resp, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
			if err != nil {
				t.Fatal(rendered(t, err))
			}
			tt.check(t, resp)
		})
	}
}

// oddAnswerCheck says what a test wants to find in the response to an answer.
type oddAnswerCheck func(t *testing.T, r *Response)

func wantStop(want Stop) oddAnswerCheck {
	return func(t *testing.T, r *Response) {
		t.Helper()
		if r.Stop != want {
			t.Errorf("stop = %v", r.Stop)
		}
	}
}

// wantOneToolUse wants a single request for a tool, whose arguments were readable or were not,
// and, when wantPath is not empty, that has that path in them.
func wantOneToolUse(wantBad bool, wantPath string) oddAnswerCheck {
	return func(t *testing.T, r *Response) {
		t.Helper()
		u := r.ToolUses()
		if len(u) != 1 || u[0].BadInput != wantBad || (wantPath != "" && u[0].Input["path"] != wantPath) {
			t.Errorf("uses = %+v", u)
		}
	}
}

func wantTextAndStop(text string, stop Stop) oddAnswerCheck {
	return func(t *testing.T, r *Response) {
		t.Helper()
		if r.Text() != text || r.Stop != stop {
			t.Errorf("text %q stop %v", r.Text(), r.Stop)
		}
	}
}

func TestAnAnswerWithoutChoicesIsAProblem(t *testing.T) {
	c := newCapture(t, 200, `{"choices":[]}`)
	model, _ := NewOpenAI(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey})
	_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
	mustContain(t, rendered(t, err), "answered without a message")
}

func TestAServerOnThisMachineNeedsNoKey(t *testing.T) {
	c := newCapture(t, 200, `{"choices":[{"message":{"content":"hi"},"finish_reason":"stop"}]}`)
	model, _ := NewOpenAI(Settings{BaseURL: c.server.URL, Model: "m"})
	if _, err := model.Complete(context.Background(), &Request{MaxTokens: 1}); err != nil {
		t.Fatal(rendered(t, err))
	}
	if got := c.headers.Get("Authorization"); got != "" {
		t.Errorf("an empty key was sent as %q", got)
	}
}

// ---------- configuration ----------

func TestTheConfigurationIsCheckedAndFilledIn(t *testing.T) {
	r, err := Resolve(config.LLM{Provider: "anthropic"})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if r.BaseURL != AnthropicBase || r.KeyEnv != "ANTHROPIC_API_KEY" || r.Model != "claude-sonnet-5-5" {
		t.Errorf("anthropic defaults = %+v", r)
	}
	r, err = Resolve(config.LLM{Provider: "OpenAI-Compatible", Model: "gpt", BaseURL: "https://x.example/v1/"})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if r.Provider != "openai-compatible" || r.BaseURL != "https://x.example/v1" || r.KeyEnv != "OPENAI_API_KEY" {
		t.Errorf("openai = %+v", r)
	}

	for _, tt := range []struct {
		cfg  config.LLM
		want string
	}{
		{config.LLM{}, "needs a language model to think, and none is set up"},
		{config.LLM{Provider: "gemini"}, "I do not know the language model provider `gemini`"},
		{config.LLM{Provider: "openai"}, "needs a model name"},
		{config.LLM{Provider: "anthropic", BaseURL: "http://api.example.com"}, "without encryption"},
		{config.LLM{Provider: "anthropic", MaxTokensField: "max"}, "is not a name the limit of tokens can have"},
	} {
		_, err := Resolve(tt.cfg)
		mustContain(t, rendered(t, err), tt.want)
	}
}

// req: T1, T3
func TestOnlyANonDefaultAddressNeedsApproval(t *testing.T) {
	for _, tt := range []struct {
		cfg  config.LLM
		want string
		ok   bool
	}{
		{config.LLM{Provider: "anthropic"}, "", false},
		{config.LLM{Provider: "anthropic", BaseURL: "https://api.anthropic.com/"}, "", false},
		{config.LLM{Provider: "openai", Model: "m", BaseURL: "https://api.openai.com"}, "", false},
		{config.LLM{Provider: "anthropic", BaseURL: "https://proxy.example"}, "https://proxy.example", true},
		{config.LLM{Provider: "openai-compatible", Model: "m", BaseURL: "http://localhost:11434"}, "http://localhost:11434", true},
		{config.LLM{}, "", false},
	} {
		got, ok := NonDefaultBase(tt.cfg)
		if got != tt.want || ok != tt.ok {
			t.Errorf("%+v -> %q, %v; want %q, %v", tt.cfg, got, ok, tt.want, tt.ok)
		}
	}
}

func TestTheKeyIsReadFromTheVariableTheConfigurationNames(t *testing.T) {
	env := map[string]string{"MY_KEY": testKey}
	getenv := func(name string) string { return env[name] }

	if _, err := FromConfig(config.LLM{Provider: "anthropic", APIKeyEnv: "MY_KEY"}, getenv); err != nil {
		t.Fatal(rendered(t, err))
	}
	_, err := FromConfig(config.LLM{Provider: "anthropic"}, getenv)
	mustContain(t, rendered(t, err),
		"the language model could not answer: the variable ANTHROPIC_API_KEY is not set", "export ANTHROPIC_API_KEY")

	// Only a server on this same machine may go without a key.
	if _, err := FromConfig(config.LLM{Provider: "openai-compatible", Model: "m", BaseURL: "http://localhost:11434"}, getenv); err != nil {
		t.Errorf("a local server was refused: %s", rendered(t, err))
	}
	if _, err := FromConfig(config.LLM{Provider: "openai-compatible", Model: "m", BaseURL: "https://x.example"}, getenv); err == nil {
		t.Error("a remote server without a key was accepted")
	}
	if _, err := FromConfig(config.LLM{Provider: "anthropic", BaseURL: "http://localhost:9"}, getenv); err == nil {
		t.Error("the Anthropic provider needs a key even on this machine")
	}
}

// req: L8
func TestRedactingHidesTheKeyAndLeavesShortWordsAlone(t *testing.T) {
	if got := Redact("bad key sk-test-123456 here", testKey); got != "bad key [hidden] here" {
		t.Errorf("got %q", got)
	}
	if got := Redact("a key, ok", "key"); got != "a key, ok" {
		t.Errorf("a short word was hidden: %q", got)
	}
	if got := Redact("nothing here", ""); got != "nothing here" {
		t.Errorf("an empty secret changed the text: %q", got)
	}
}

func TestTheScriptedModelAnswersInOrderAndRemembersTheQuestions(t *testing.T) {
	model := NewScripted(Say("one"), Ask(Use("a", "n", map[string]any{"k": "v"})), Cut("half"), Fail(errors.New("down")))
	var stops []Stop
	for i := 0; i < 3; i++ {
		resp, err := model.Complete(context.Background(), &Request{System: "s", Messages: []Message{{Role: RoleUser}}})
		if err != nil {
			t.Fatal(err)
		}
		stops = append(stops, resp.Stop)
	}
	if stops[0] != StopEnd || stops[1] != StopToolUse || stops[2] != StopLength {
		t.Errorf("stops = %v", stops)
	}
	if _, err := model.Complete(context.Background(), &Request{}); err == nil || err.Error() != "down" {
		t.Errorf("err = %v", err)
	}
	if _, err := model.Complete(context.Background(), &Request{}); err == nil {
		t.Error("a model without answers left must say so")
	}
	if got := len(model.Requests()); got != 5 {
		t.Errorf("remembered %d questions, want 5", got)
	}
}

// req: L8
func TestAKeyWrittenWhereTheNameOfTheVariableGoesIsNeverShown(t *testing.T) {
	const key = "sk-ant-usr-abcdefghijklmnopqrstuvwxyz-0123456789"
	getenv := func(string) string { return "" }
	for name, call := range map[string]func() error{
		"Resolve": func() error { _, err := Resolve(config.LLM{Provider: "anthropic", APIKeyEnv: key}); return err },
		"FromConfig": func() error {
			_, err := FromConfig(config.LLM{Provider: "anthropic", APIKeyEnv: key}, getenv)
			return err
		},
	} {
		text := rendered(t, call())
		mustContain(t, text, "api_key_env must be the NAME of an environment variable", "not the key itself")
		if strings.Contains(text, key) || strings.Contains(text, "sk-ant") {
			t.Errorf("%s repeated the key:\n%s", name, text)
		}
	}
	for _, good := range []string{"ANTHROPIC_API_KEY", "my_key2", "_x"} {
		if !validEnvName(good) {
			t.Errorf("%s was refused as a name", good)
		}
	}
	for _, bad := range []string{"", "1KEY", "MY-KEY", "MY KEY", "key=value", "sk-ant-usr-abc"} {
		if validEnvName(bad) {
			t.Errorf("%q was accepted as a name", bad)
		}
	}
}

// req: L8
func TestAnAddressWithASecretInItIsNeverRepeated(t *testing.T) {
	for _, address := range []string{
		"https://user:hunter2secret@api.example.com",
		"https://api.example.com/?token=abc123secret",
		"not an address?token=abc123secret",
		"https://api.example.com/#frag-secret99",
	} {
		_, err := parseBase(address)
		if err == nil {
			t.Errorf("%s was accepted", address)
			continue
		}
		text := rendered(t, err)
		for _, secret := range []string{"hunter2secret", "abc123secret", "frag-secret99"} {
			if strings.Contains(text, secret) {
				t.Errorf("the message for %s repeated the secret:\n%s", address, text)
			}
		}
	}
	// An address with nothing secret in it is still shown, to help find the mistake.
	_, err := parseBase("ftp://api.example.com")
	mustContain(t, rendered(t, err), "`ftp://api.example.com`")
}

// ---------- workspaces and hints ----------

func TestTheWorkspaceIsSentOnlyWhenOneIsConfigured(t *testing.T) {
	c := newCapture(t, 200, anthropicAnswer)
	with, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey, WorkspaceID: "wrkspc_01Abc"})
	if _, err := with.Complete(context.Background(), &Request{MaxTokens: 1}); err != nil {
		t.Fatal(rendered(t, err))
	}
	if got := c.headers.Get("anthropic-workspace-id"); got != "wrkspc_01Abc" {
		t.Errorf("workspace header = %q", got)
	}

	c2 := newCapture(t, 200, anthropicAnswer)
	without, _ := NewAnthropic(Settings{BaseURL: c2.server.URL, Model: "m", APIKey: testKey})
	if _, err := without.Complete(context.Background(), &Request{MaxTokens: 1}); err != nil {
		t.Fatal(rendered(t, err))
	}
	if _, sent := c2.headers["Anthropic-Workspace-Id"]; sent {
		t.Error("an empty workspace was sent as a header")
	}
}

func TestTheWorkspaceInTheConfigurationIsCheckedAndPassedOn(t *testing.T) {
	r, err := Resolve(config.LLM{Provider: "anthropic", WorkspaceID: " wrkspc_01JwQvzr7rXLA5AGx3HKfFUJ "})
	if err != nil || r.WorkspaceID != "wrkspc_01JwQvzr7rXLA5AGx3HKfFUJ" {
		t.Fatalf("got %+v, %v", r, err)
	}
	for _, tt := range []struct {
		cfg  config.LLM
		want string
	}{
		{config.LLM{Provider: "openai", Model: "m", WorkspaceID: "wrkspc_1"}, "only used with the provider \"anthropic\""},
		{config.LLM{Provider: "anthropic", WorkspaceID: "wrkspc 1"}, "is not a workspace identifier"},
		{config.LLM{Provider: "anthropic", WorkspaceID: "wrkspc_1\r\nX-Evil: 1"}, "is not a workspace identifier"},
		{config.LLM{Provider: "anthropic", WorkspaceID: strings.Repeat("a", 200)}, "is not a workspace identifier"},
	} {
		_, err := Resolve(tt.cfg)
		mustContain(t, rendered(t, err), tt.want)
	}
}

func TestTheAnswersPeopleMeetWhenTheySetUpComeWithWhatToDo(t *testing.T) {
	for _, tt := range []struct {
		status int
		answer string
		hint   string
	}{
		{401, `{"error":{"message":"API key is invalid."}}`, "the provider does not accept this key"},
		{400, `{"error":{"message":"This API key is not scoped to a workspace, so this request must include the anthropic-workspace-id header"}}`, `workspace_id = "wrkspc_..."`},
		{403, `{"error":{"message":"forbidden"}}`, "not allowed to do this"},
		{404, `{"error":{"message":"model: nope"}}`, "model name"},
		{400, `{"error":{"message":"max_tokens is too large"}}`, ""},
		{500, `{"error":{"message":"oops"}}`, ""},
	} {
		c := newCapture(t, tt.status, tt.answer)
		model, _ := NewAnthropic(Settings{BaseURL: c.server.URL, Model: "m", APIKey: testKey, Wait: noWait, Retries: -1})
		_, err := model.Complete(context.Background(), &Request{MaxTokens: 1})
		var failure *Error
		if !errors.As(err, &failure) {
			t.Fatalf("status %d: err = %v", tt.status, err)
		}
		if tt.hint == "" && failure.Hint != "" || !strings.Contains(failure.Hint, tt.hint) {
			t.Errorf("status %d: hint = %q, want it to contain %q", tt.status, failure.Hint, tt.hint)
		}
	}
}

// req: T3
func TestAModelGetsNoKeyOnlyWhenNoneIsSetAndTheAddressIsOfThisComputer(t *testing.T) {
	none := func(string) string { return "" }
	set := func(string) string { return "a-key" }
	compatible := func(address string) config.LLM {
		return config.LLM{Provider: "openai-compatible", Model: "m", BaseURL: address}
	}
	for _, tt := range []struct {
		name   string
		cfg    config.LLM
		getenv func(string) string
		want   bool
	}{
		{"a server on localhost", compatible("http://localhost:11434/v1"), none, true},
		{"a server on 127.0.0.1", compatible("http://127.0.0.1:8080"), none, true},
		{"a server on ::1", compatible("http://[::1]:8080"), none, true},
		{"with a key set", compatible("http://localhost:11434/v1"), set, false},
		{"a server that is not on this computer", compatible("https://models.example/v1"), none, false},
		{"Anthropic is never used with no key", config.LLM{Provider: "anthropic", BaseURL: "http://localhost:9"}, none, false},
		{"settings that are not valid", config.LLM{Provider: "nobody"}, none, false},
	} {
		if got := SendsNoKey(tt.cfg, tt.getenv); got != tt.want {
			t.Errorf("%s: SendsNoKey = %v, want %v", tt.name, got, tt.want)
		}
	}
}
