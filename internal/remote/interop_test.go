package remote

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"metagente/internal/tools"
	"metagente/internal/value"
)

// echoExecutor answers every message with a text artifact, and reports the
// message it received as the SDK understood it.
type echoExecutor struct{ received chan []byte }

func (e *echoExecutor) Execute(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		raw, _ := json.Marshal(execCtx.Message)
		e.received <- raw
		if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		if !yield(a2a.NewArtifactEvent(execCtx, a2a.NewTextPart("echo: ok")), nil) {
			return
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
	}
}

func (e *echoExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

func hasString(raw []byte, want string) bool {
	var document any
	if json.Unmarshal(raw, &document) != nil {
		return false
	}
	return jsonContainsString(document, want)
}

// jsonContainsString looks for a text, at any depth of a document that was read from JSON.
func jsonContainsString(node any, want string) bool {
	switch node := node.(type) {
	case string:
		return node == want
	case []any:
		for _, item := range node {
			if jsonContainsString(item, want) {
				return true
			}
		}
	case map[string]any:
		for _, item := range node {
			if jsonContainsString(item, want) {
				return true
			}
		}
	}
	return false
}

// This is the test that matters most for the client: it is written by hand, so
// the only proof that it speaks A2A is a server written by someone else, the
// SDK that `serve` will use.
func TestOurClientTalksToTheServerOfTheSDK(t *testing.T) {
	executor := &echoExecutor{received: make(chan []byte, 4)}
	mux := http.NewServeMux()
	server := httptest.NewUnstartedServer(mux)
	base := "http://" + server.Listener.Addr().String()

	// The card is the one the SDK produces; only the skills are added, because
	// building them needs types this test does not depend on.
	sdkCard := a2asrv.NewStaticAgentCardHandler(&a2a.AgentCard{
		Name:    "Bob",
		Version: "1.0.0",
		SupportedInterfaces: []*a2a.AgentInterface{
			a2a.NewAgentInterface(base, a2a.TransportProtocolJSONRPC),
		},
	})
	mux.HandleFunc(a2asrv.WellKnownAgentCardPath, func(w http.ResponseWriter, r *http.Request) {
		recorder := httptest.NewRecorder()
		sdkCard.ServeHTTP(recorder, r)
		var document map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &document); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		document["skills"] = []any{map[string]any{"id": "ask", "name": "Ask", "description": "answer a question"}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(document)
	})
	mux.Handle("/", a2asrv.NewJSONRPCHandler(a2asrv.NewHandler(executor)))
	server.Start()
	defer server.Close()

	pool := NewPool(Options{Allow: func(Spec) error { return nil }, PollEvery: 10 * time.Millisecond})
	tool := pool.Tool("Bob", Spec{Name: "Bob", URL: base})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	actions, err := tool.Actions(ctx)
	if err != nil {
		t.Fatalf("the card of the SDK was not understood: %s", rendered(t, err))
	}
	if len(actions) != 1 || actions[0].Name != "ask" {
		t.Fatalf("actions = %+v", actions)
	}

	got, err := tool.Call(WithTrail(ctx, []string{"Planner"}), "ask", tools.Args{"city": value.Text("Lisbon")})
	if err != nil {
		t.Fatalf("the server of the SDK refused our call: %s", rendered(t, err))
	}
	if got.Text != "echo: ok" {
		t.Errorf("the answer = %q", got.Text)
	}

	select {
	case raw := <-executor.received:
		for _, want := range []string{"ask", "Lisbon", "Planner"} {
			if !hasString(raw, want) {
				t.Errorf("the SDK did not receive %q in:\n%s", want, raw)
			}
		}
		if !strings.Contains(string(raw), "ROLE_USER") {
			t.Errorf("the role was not understood:\n%s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the executor of the SDK received nothing")
	}
}
