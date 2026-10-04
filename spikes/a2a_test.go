package spikes

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

type observation struct {
	contextID   string
	messageJSON []byte
}

// spikeExecutor reports what the SDK handed it, then answers "ok".
type spikeExecutor struct {
	observed chan observation
}

func (e *spikeExecutor) Execute(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		raw, _ := json.Marshal(execCtx.Message)
		e.observed <- observation{contextID: execCtx.ContextID, messageJSON: raw}
		yield(a2a.NewMessage(a2a.MessageRoleAgent, a2a.NewTextPart("ok")), nil)
	}
}

func (e *spikeExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

type panickingExecutor struct{}

func (panickingExecutor) Execute(context.Context, *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) { panic("boom") }
}

func (panickingExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

// jsonHasString reports whether some string inside the JSON document is exactly want.
// It needs no knowledge of how the SDK names its fields.
func jsonHasString(raw []byte, want string) bool {
	var document any
	if err := json.Unmarshal(raw, &document); err != nil {
		return false
	}
	var walk func(any) bool
	walk = func(node any) bool {
		switch node := node.(type) {
		case string:
			return node == want
		case []any:
			for _, item := range node {
				if walk(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range node {
				if walk(item) {
					return true
				}
			}
		}
		return false
	}
	return walk(document)
}

// V6: a large text with newlines, quotes, tabs and unicode survives the SDK's
// message type, both in memory and through JSON.
func TestV6LongMultilineTextReachesTheExecutorIntact(t *testing.T) {
	body := strings.Repeat("line one \"quoted\" \\ back\nline two ünïcode ✓\ttab\r\n", 2000)
	t.Logf("text of %d bytes", len(body))
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(body))

	// Through JSON, and back.
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	if !jsonHasString(raw, body) {
		t.Error("the text is not in the JSON of the message exactly as it was written")
	}
	var back a2a.Message
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("the message cannot be read back from its own JSON: %v", err)
	}
	again, _ := json.Marshal(&back)
	if !jsonHasString(again, body) {
		t.Error("the text changed after a round trip through JSON")
	}

	// Through the request handler, to the executor.
	executor := &spikeExecutor{observed: make(chan observation, 1)}
	handler := a2asrv.NewHandler(executor)
	if _, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: message}); err != nil {
		t.Fatal(err)
	}
	seen := <-executor.observed
	t.Logf("context id given to the executor: %q", seen.contextID)
	if seen.contextID == "" {
		t.Error("the executor got no context id, so state could not be kept per context (S7)")
	}
	if !jsonHasString(seen.messageJSON, body) {
		t.Error("the executor did not receive the text exactly as it was sent")
	}
}

// V3: our own middleware can sit in front of the JSON-RPC handler, while the
// Agent Card stays public. NewJSONRPCHandler returns an ordinary http.Handler.
func TestV3OurMiddlewareSitsInFrontOfTheJSONRPCHandler(t *testing.T) {
	executor := &spikeExecutor{observed: make(chan observation, 8)}
	rpc := a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor))
	card := a2asrv.NewStaticAgentCardHandler(&a2a.AgentCard{
		Name:    "spike",
		Version: "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface("http://localhost:0", a2a.TransportProtocolJSONRPC),
		},
	})

	var reachedSDK atomic.Int32
	probe := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reachedSDK.Add(1)
		rpc.ServeHTTP(w, r)
	})

	const token = "s3cret-token-value"
	protect := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, card)
	mux.Handle("/", protect(probe))
	server := httptest.NewServer(mux)
	defer server.Close()

	do := func(method, path, authorization string) (int, string) {
		var body io.Reader
		if method == http.MethodPost {
			body = strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"GetTask","params":{"id":"does-not-exist"}}`)
		}
		req, err := http.NewRequest(method, server.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}

	if code, _ := do(http.MethodGet, a2asrv.WellKnownAgentCardPath, ""); code != http.StatusOK {
		t.Errorf("the Agent Card should be public, got %d", code)
	}
	if code, _ := do(http.MethodPost, "/", ""); code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", code)
	}
	if code, _ := do(http.MethodPost, "/", "Bearer wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", code)
	}
	if reachedSDK.Load() != 0 {
		t.Errorf("the SDK saw %d request(s) that had no valid token", reachedSDK.Load())
	}
	code, body := do(http.MethodPost, "/", "Bearer "+token)
	t.Logf("valid token: %d %s", code, body)
	if code == http.StatusUnauthorized {
		t.Error("a valid token was refused")
	}
	if reachedSDK.Load() != 1 {
		t.Errorf("the SDK saw %d request(s) with a valid token, want 1", reachedSDK.Load())
	}
}

// P4: a panic inside the agent must not take the server down.
func TestP4APanicInTheAgentDoesNotTakeTheServerDown(t *testing.T) {
	handler := a2asrv.NewHandler(panickingExecutor{},
		a2asrv.WithExecutionPanicHandler(func(r any) error { return fmt.Errorf("recovered: %v", r) }))
	result, err := handler.SendMessage(context.Background(), &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("hello")),
	})
	t.Logf("the call came back with result %T and error %v", result, err)
}
