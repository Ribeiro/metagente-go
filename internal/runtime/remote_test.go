package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/llm"
	"github.com/Ribeiro/metagente-go/internal/tools"
	"github.com/Ribeiro/metagente-go/internal/trust"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// stubAgent is a small A2A agent: a card with one skill, and an answer to every
// message. It remembers what it was sent.
type stubAgent struct {
	*httptest.Server
	// wantToken, when set, is the bearer token the agent asks for on calls.
	wantToken string
	mu        sync.Mutex
	bodies    []map[string]any
	auth      []string
}

func newStubAgent(t *testing.T, answer string) *stubAgent {
	t.Helper()
	s := &stubAgent{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/agent-card.json" {
			_, _ = fmt.Fprintf(w, `{"supportedInterfaces":[{"url":"http://%s","protocolBinding":"JSONRPC","protocolVersion":"1.0"}],
 "skills":[{"id":"ask","description":"ask about a city"}]}`, r.Host)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		s.mu.Lock()
		s.bodies = append(s.bodies, body)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.mu.Unlock()
		if s.wantToken != "" && r.Header.Get("Authorization") != "Bearer "+s.wantToken {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"unauthorized"}}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":{"message":{"role":"ROLE_AGENT","parts":[{"text":%q}]}}}`, answer)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *stubAgent) sent() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.bodies...)
}

func remoteSource(address string) string {
	return "agent Trip\n  goal \"Ask Bob\"\n  remote Bob at \"" + address + "\"\n  accepts plan city\n" +
		"  on plan\n    forecast = Bob.ask city: city\n    reply \"Bob says: {forecast}\"\n"
}

// req: T1
func TestAnUnapprovedRemoteAgentIsNotReached(t *testing.T) {
	bob := newStubAgent(t, "sunny")
	rt := trustedRT(t)
	_, err := runSource(t, rt, remoteSource(bob.URL), "plan", "city=Lisbon")
	mustContain(t, errText(t, err), "have not approved for this project", "connects to: "+bob.URL)
	if len(bob.sent()) != 0 {
		t.Error("the agent was reached before the person approved it")
	}

	// Even a run that skipped that check cannot reach it.
	_, err = ask(agentFrom(t, rt, remoteSource(bob.URL), "c"), "plan", "city", "Lisbon")
	mustContain(t, errText(t, err), "the remote agent Bob, at "+bob.URL+", has not been approved for this project")
	if len(bob.sent()) != 0 {
		t.Error("the agent was reached without approval")
	}
}

func TestACallToAnApprovedRemoteAgentWorksLikeAnyOtherCall(t *testing.T) {
	bob := newStubAgent(t, "sunny in Lisbon")
	rt := trustedRT(t)
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.RemoteItem(bob.URL)}); err != nil {
		t.Fatal(err)
	}
	got, err := runSource(t, rt, remoteSource(bob.URL), "plan", "city=Lisbon")
	if err != nil || got.Text != "Bob says: sunny in Lisbon" {
		t.Fatalf("got %v %v", got.Display(), err)
	}
	messages := bob.sent()
	if len(messages) != 1 || messages[0]["method"] != "SendMessage" {
		t.Fatalf("what Bob received = %v", messages)
	}
	part := messages[0]["params"].(map[string]any)["message"].(map[string]any)["parts"].([]any)[0].(map[string]any)
	arguments := part["data"].(map[string]any)["arguments"].(map[string]any)
	if arguments["city"] != "Lisbon" {
		t.Errorf("arguments = %v", arguments)
	}
}

func TestARemoteAgentThatIsNotThereFailsWithAPlainSentenceAtTheLineOfTheCall(t *testing.T) {
	// An address nobody listens at: the call fails with a plain sentence.
	bob := newStubAgent(t, "x")
	address := bob.URL
	bob.Close()
	rt := trustedRT(t)
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.RemoteItem(address)}); err != nil {
		t.Fatal(err)
	}
	_, err := runSource(t, rt, remoteSource(address), "plan", "city=Lisbon")
	mustContain(t, errText(t, err), "Problem on line 6", "I could not reach 127.0.0.1", "remote agent Bob")
}

// req: D2
func TestTheOtherSideIsToldWhichAgentsAreRunning(t *testing.T) {
	bob := newStubAgent(t, "ok")
	rt := trustedRT(t)
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.RemoteItem(bob.URL)}); err != nil {
		t.Fatal(err)
	}
	agent := agentFrom(t, rt, remoteSource(bob.URL), "c")
	ctx := withCall(context.Background(), &Call{TaskID: "t", Chain: []string{"Outer", "Trip"}})
	if _, err := agent.Tools["Bob"].Call(ctx, "ask", tools.Args{"city": value.Text("Lisbon")}); err != nil {
		t.Fatal(errText(t, err))
	}
	message := bob.sent()[0]["params"].(map[string]any)["message"].(map[string]any)
	chain := message["metadata"].(map[string]any)["metagente"].(map[string]any)["chain"].([]any)
	if len(chain) != 2 || chain[0] != "Outer" || chain[1] != "Trip" {
		t.Errorf("chain = %v", chain)
	}
}

// req: D2
func TestCallsGoingTooDeepAreStoppedBeforeTheyLeave(t *testing.T) {
	bob := newStubAgent(t, "ok")
	rt := trustedRT(t)
	rt.Config.Runtime.MaxCallDepth = 2
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.RemoteItem(bob.URL)}); err != nil {
		t.Fatal(err)
	}
	agent := agentFrom(t, rt, remoteSource(bob.URL), "c")
	ctx := withCall(context.Background(), &Call{TaskID: "t", Chain: []string{"A", "B"}})
	_, err := agent.Tools["Bob"].Call(ctx, "ask", tools.Args{"city": value.Text("Lisbon")})
	mustContain(t, errText(t, err), "calling each other 2 levels deep", "max_call_depth")
	if len(bob.sent()) != 0 {
		t.Error("the call left although it was too deep")
	}
}

func TestWhatARemoteAgentHandlesIsOfferedToTheModel(t *testing.T) {
	bob := newStubAgent(t, "ok")
	model := llm.NewScripted(llm.Say("fine"))
	rt := thinkRT(t, model, func(c *config.Config) {})
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.RemoteItem(bob.URL)}); err != nil {
		t.Fatal(err)
	}
	source := "agent Trip\n  goal \"Ask Bob\"\n  remote Bob at \"" + bob.URL + "\"\n  accepts go\n  on go\n    reply think \"ask Bob about Lisbon\"\n"
	if _, err := askThink(t, rt, source); err != nil {
		t.Fatal(errText(t, err))
	}
	request := model.Requests()[0]
	if names := strings.Join(toolNames(request), " "); names != "Bob__ask" {
		t.Fatalf("the model was offered %q", names)
	}
	mustContain(t, request.Tools[0].Description, "Bob.ask: ask about a city")
}

// ---------- credentials (E5) ----------

// req: E5, T1
func TestApprovingTheAddressDoesNotApproveSendingATokenThere(t *testing.T) {
	const token = "tok-live-0123456789"
	bob := newStubAgent(t, "sunny")
	bob.wantToken = token
	rt := trustedRT(t)
	rt.Config.Credentials = map[string]string{"Bob": "BOB_TOKEN"}
	rt.Getenv = func(name string) string {
		if name == "BOB_TOKEN" {
			return token
		}
		return ""
	}

	// The address alone was approved earlier; the token going there is new.
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.RemoteItem(bob.URL)}); err != nil {
		t.Fatal(err)
	}
	_, err := runSource(t, rt, remoteSource(bob.URL), "plan", "city=Lisbon")
	mustContain(t, errText(t, err), "have not approved for this project",
		"connects to: "+bob.URL+" (and sends it the token held in BOB_TOKEN)")
	if len(bob.sent()) != 0 {
		t.Error("the agent was reached before the token was approved")
	}

	// Once the person approves it with the token, the call goes with it.
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.RemoteItem(bob.URL).WithCredential("BOB_TOKEN")}); err != nil {
		t.Fatal(err)
	}
	got, err := runSource(t, rt, remoteSource(bob.URL), "plan", "city=Lisbon")
	if err != nil || got.Text != "Bob says: sunny" {
		t.Fatalf("got %v %v", got.Display(), err)
	}
	if auth := bob.auth[len(bob.auth)-1]; auth != "Bearer "+token {
		t.Errorf("Authorization = %q", auth)
	}
}

// req: E5, L8
func TestAnAgentRefusedByTheOtherSideNeverShowsTheToken(t *testing.T) {
	const token = "tok-live-0123456789"
	bob := newStubAgent(t, "sunny")
	bob.wantToken = "a-different-token-9999"
	rt := trustedRT(t)
	rt.Config.Credentials = map[string]string{"Bob": "BOB_TOKEN"}
	rt.Getenv = func(name string) string {
		if name == "BOB_TOKEN" {
			return token
		}
		return ""
	}
	if err := rt.Trust.Approve(rt.Config.Root, []trust.Item{trust.RemoteItem(bob.URL).WithCredential("BOB_TOKEN")}); err != nil {
		t.Fatal(err)
	}
	_, err := runSource(t, rt, remoteSource(bob.URL), "plan", "city=Lisbon")
	text := errText(t, err)
	mustContain(t, text, "agent Bob refused the request: unauthorized")
	if strings.Contains(text, token) {
		t.Errorf("the token is in the error:\n%s", text)
	}
}

// req: E5, E6
func TestAnAgentCannotReadTheVariableOfACredential(t *testing.T) {
	const token = "tok-live-0123456789"
	rt := trustedRT(t)
	rt.Config.Credentials = map[string]string{"Bob": "BOB_TOKEN"}
	t.Setenv("BOB_TOKEN", token)
	source := "agent Spy\n  goal \"x\"\n  tool env \"BOB_TOKEN\"\n  accepts go\n  on go\n    reply env.get name: \"BOB_TOKEN\"\n"
	got, err := ask(agentFrom(t, rt, source, "c"), "go")
	mustContain(t, errText(t, err), "`BOB_TOKEN` is a secret", "agents can never read it")
	if strings.Contains(got.Display(), token) {
		t.Error("the token was read")
	}
}

// req: E5
func TestACredentialForAProgramIsIgnoredBecauseOnlyAnAddressGetsAToken(t *testing.T) {
	rt := trustedRT(t)
	rt.Config.Credentials = map[string]string{"weather": "W_TOKEN"}
	agents, err := lang.ParseFile("a.ag", "", "agent A\n  goal \"x\"\n  tool weather from mcp \"npx -y weather-mcp@1.0.0\"\n  accepts go\n  on go\n    reply \"hi\"\n")
	if err != nil {
		t.Fatal(err)
	}
	needs := rt.Needs(agents)
	if len(needs) != 1 || needs[0].Credential != "" {
		t.Errorf("needs = %+v", needs)
	}
}
