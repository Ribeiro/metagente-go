package serve

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"metagente/internal/config"
	"metagente/internal/lang"
	"metagente/internal/remote"
	"metagente/internal/tools"
	"metagente/internal/value"
)

// The contract of the server with any client of A2A 1.0, written from the text of the protocol and
// not from the client of this project. It is the port of `contract/agent_card.rs` and
// `contract/a2a_wire.rs` of the original project, with the differences of this server, which were
// decided and are in the README: the card is at /agents/NAME and needs the token, an agent answers
// inside the request with a Message, and no task is kept (so there is no GetTask to come back to).

const contractWeather = `agent Weather
  goal "Answer questions about the weather"
  accepts ask city  # weather for a city
  accepts broken
  on ask
    reply "sunny in {city}"
  on broken
    fail "the barometer exploded"
`

// serveSource serves the agents of a source with the real interpreter, on a real port, with the
// limits that `metagente serve` has by default.
func serveSource(t *testing.T, source string) string {
	t.Helper()
	rt := realRuntime(t)
	defs, err := lang.ParseFile("served.ag", "", source)
	if err != nil {
		t.Fatal(err)
	}
	agents := make([]Agent, len(defs))
	for i, def := range defs {
		agents[i] = NewRuntimeAgent(rt, def)
	}
	// The limits of `metagente serve` when nothing is set.
	defaults := config.Default().Serve
	address, _ := realServer(t, func(c *Config) {
		c.MaxInFlight, c.MaxConversations = defaults.MaxRunningTasks, defaults.MaxRetainedTasks
	}, RunOptions{MaxConnections: defaults.MaxConnections}, agents...)
	return address
}

// contractRequest makes a request with the token, and reads the answer as JSON.
func contractRequest(t *testing.T, method, url, body string, headers map[string]string) (int, http.Header, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+goodToken)
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := realClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var doc map[string]any
	_ = json.Unmarshal(raw, &doc)
	return resp.StatusCode, resp.Header, doc
}

func contractCard(t *testing.T, address, agent string) map[string]any {
	t.Helper()
	status, _, card := contractRequest(t, http.MethodGet, "http://"+address+"/agents/"+agent+cardSuffix, "", nil)
	if status != http.StatusOK {
		t.Fatalf("the card of %s: HTTP %d", agent, status)
	}
	return card
}

// contractRPC is a JSON-RPC call written from the text of A2A 1.0, with the header of its version.
func contractRPC(t *testing.T, address, agent, method string, params any) map[string]any {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": method, "params": params})
	_, _, doc := contractRequest(t, http.MethodPost, "http://"+address+"/agents/"+agent, string(body), map[string]string{"A2A-Version": "1.0"})
	return doc
}

func userText(text string, extra map[string]any) map[string]any {
	message := map[string]any{"messageId": "m-1", "role": "ROLE_USER", "parts": []any{map[string]any{"text": text}}}
	for k, v := range extra {
		message[k] = v
	}
	return map[string]any{"message": message}
}

// at walks a document of JSON: keys of objects, and indexes of lists.
func at(doc any, path ...any) any {
	for _, step := range path {
		switch key := step.(type) {
		case string:
			object, _ := doc.(map[string]any)
			doc = object[key]
		case int:
			list, _ := doc.([]any)
			if key >= len(list) {
				return nil
			}
			doc = list[key]
		}
	}
	return doc
}

func nonEmptyText(t *testing.T, doc any, what string) string {
	t.Helper()
	text, _ := doc.(string)
	if text == "" {
		t.Errorf("%s must be a text that is not empty, and is %v", what, doc)
	}
	return text
}

// ---------- the card (contract/agent_card.rs) ----------

func TestTheCardHasEveryFieldThatA2AAsksFor(t *testing.T) {
	address := serveSource(t, contractWeather)
	card := contractCard(t, address, "Weather")
	if card["name"] != "Weather" || card["description"] != "Answer questions about the weather" {
		t.Errorf("name %v, description %v", card["name"], card["description"])
	}
	nonEmptyText(t, card["version"], "version")
	checkInterfaces(t, card)
	if _, ok := card["capabilities"].(map[string]any); !ok || at(card, "capabilities", "streaming") != false {
		t.Errorf("capabilities %v", card["capabilities"])
	}
	for _, key := range []string{"defaultInputModes", "defaultOutputModes"} {
		checkModes(t, card, key)
	}
	raw, _ := json.Marshal(card)
	for _, snake := range []string{"supported_interfaces", "protocol_binding", "default_input_modes"} {
		if strings.Contains(string(raw), snake) {
			t.Errorf("%s in %s: the protocol writes its names in camelCase", snake, raw)
		}
	}
}

// checkInterfaces wants at least one interface, each with an address, the JSON-RPC binding and 1.0.
func checkInterfaces(t *testing.T, card map[string]any) {
	t.Helper()
	interfaces, _ := card["supportedInterfaces"].([]any)
	if len(interfaces) == 0 {
		t.Fatal("no supportedInterfaces")
	}
	for _, i := range interfaces {
		if url := nonEmptyText(t, at(i, "url"), "url"); !strings.HasPrefix(url, "http") {
			t.Errorf("url %q", url)
		}
		if at(i, "protocolBinding") != "JSONRPC" || at(i, "protocolVersion") != "1.0" {
			t.Errorf("interface %v", i)
		}
	}
}

// checkModes wants a list of media types that is not empty, and only texts in it.
func checkModes(t *testing.T, card map[string]any, key string) {
	t.Helper()
	modes, _ := card[key].([]any)
	if len(modes) == 0 {
		t.Errorf("%s is empty", key)
	}
	for _, m := range modes {
		if _, ok := m.(string); !ok {
			t.Errorf("%s holds %v", key, m)
		}
	}
}

func TestThereIsOneSkillForEachMessageWithANameADescriptionAndTags(t *testing.T) {
	card := contractCard(t, serveSource(t, contractWeather), "Weather")
	skills, _ := card["skills"].([]any)
	var ids []string
	for _, skill := range skills {
		ids = append(ids, nonEmptyText(t, at(skill, "id"), "id"))
		nonEmptyText(t, at(skill, "name"), "name")
		nonEmptyText(t, at(skill, "description"), "description")
		if tags, _ := at(skill, "tags").([]any); len(tags) == 0 {
			t.Errorf("the skill %v has no tags", at(skill, "id"))
		}
	}
	if strings.Join(ids, " ") != "ask broken" {
		t.Errorf("skills %v", ids)
	}
	// The comment of `accepts` is the description of the skill.
	if d, _ := at(skills, 0, "description").(string); !strings.HasPrefix(d, "weather for a city") {
		t.Errorf("description %q", d)
	}
}

func TestEveryAgentHasItsOwnCardAndItsInterfacePointsBackAtIt(t *testing.T) {
	address := serveSource(t, contractWeather+"agent Other\n  goal \"Something else\"\n  accepts hi\n  on hi\n    reply \"hi\"\n")
	if name := contractCard(t, address, "Weather")["name"]; name != "Weather" {
		t.Errorf("name %v", name)
	}
	other := contractCard(t, address, "Other")
	if other["name"] != "Other" {
		t.Errorf("name %v", other["name"])
	}
	if url := at(other, "supportedInterfaces", 0, "url"); url != "http://"+address+"/agents/Other" {
		t.Errorf("the interface of Other is %v", url)
	}
	if status, _, _ := contractRequest(t, http.MethodGet, "http://"+address+"/agents/Nobody"+cardSuffix, "", nil); status != http.StatusNotFound {
		t.Errorf("the card of an agent that is not served: HTTP %d", status)
	}
}

// ---------- the messages (contract/a2a_wire.rs) ----------

// The original server answered with a task that had completed, and an artifact. This one answers
// inside the request, so a message that worked is answered with the Message of the agent.
func TestAMessageIsAnsweredWithTheMessageOfTheAgentAndItsConversation(t *testing.T) {
	address := serveSource(t, contractWeather)
	reply := contractRPC(t, address, "Weather", "SendMessage", userText("Faro", map[string]any{"metadata": map[string]any{"skill": "ask"}}))
	if reply["jsonrpc"] != "2.0" || reply["id"] != float64(7) || reply["error"] != nil {
		t.Fatalf("%v", reply)
	}
	message := at(reply, "result", "message")
	if at(message, "role") != "ROLE_AGENT" {
		t.Errorf("role %v", at(message, "role"))
	}
	nonEmptyText(t, at(message, "messageId"), "messageId")
	nonEmptyText(t, at(message, "contextId"), "contextId")
	if text := at(message, "parts", 0, "text"); text != "sunny in Faro" {
		t.Errorf("text %v", text)
	}
}

func TestASkillIsNamedInADataPartOrInTheMetadata(t *testing.T) {
	address := serveSource(t, contractWeather)
	byData := map[string]any{"message": map[string]any{"messageId": "m", "role": "ROLE_USER", "parts": []any{
		map[string]any{"data": map[string]any{"skill": "ask", "arguments": map[string]any{"city": "Braga"}}, "mediaType": "application/json"},
	}}}
	byMeta := map[string]any{"message": map[string]any{"messageId": "m", "role": "ROLE_USER", "metadata": map[string]any{"skill": "ask"}, "parts": []any{
		map[string]any{"data": map[string]any{"city": "Viseu"}},
	}}}
	// A client that only sends text: the one value of the skill the metadata names.
	byText := userText("Lagos", map[string]any{"metadata": map[string]any{"skill": "ask"}})
	for want, params := range map[string]any{"sunny in Braga": byData, "sunny in Viseu": byMeta, "sunny in Lagos": byText} {
		if got := at(contractRPC(t, address, "Weather", "SendMessage", params), "result", "message", "parts", 0, "text"); got != want {
			t.Errorf("got %v, want %s", got, want)
		}
	}
}

func TestAnAgentThatFailsAnswersWithAFailedTaskInItsOwnWords(t *testing.T) {
	address := serveSource(t, contractWeather)
	task := at(contractRPC(t, address, "Weather", "SendMessage", userText("x", map[string]any{"metadata": map[string]any{"skill": "broken"}})), "result", "task")
	if at(task, "status", "state") != "TASK_STATE_FAILED" || at(task, "status", "message", "role") != "ROLE_AGENT" {
		t.Errorf("task %v", task)
	}
	if why, _ := at(task, "status", "message", "parts", 0, "text").(string); !strings.Contains(why, "the barometer exploded") {
		t.Errorf("why %q", why)
	}
}

// wantError checks the code of an error and the reason that A2A 1.0 gives it, in its ErrorInfo.
func wantRPCError(t *testing.T, reply map[string]any, code int, reason, contains string) {
	t.Helper()
	if got := at(reply, "error", "code"); got != float64(code) {
		t.Errorf("code %v, want %d: %v", got, code, reply)
		return
	}
	if contains != "" {
		if message, _ := at(reply, "error", "message").(string); !strings.Contains(message, contains) {
			t.Errorf("message %q does not say %q", message, contains)
		}
	}
	info := at(reply, "error", "data", 0)
	if at(info, "@type") != errorInfoType || at(info, "reason") != reason || at(info, "domain") != "a2a-protocol.org" {
		t.Errorf("the ErrorInfo of %d is %v, want the reason %s", code, info, reason)
	}
}

func TestErrorsUseTheCodesOfJSONRPCAndSayTheirReason(t *testing.T) {
	address := serveSource(t, contractWeather)
	wantRPCError(t, contractRPC(t, address, "Weather", "Dance", map[string]any{}), -32601, "METHOD_NOT_FOUND", "")
	wantRPCError(t, contractRPC(t, address, "Weather", "SendMessage", map[string]any{}), -32602, "INVALID_PARAMS", "")
	wantRPCError(t, contractRPC(t, address, "Weather", "SendMessage",
		map[string]any{"message": map[string]any{"messageId": "m", "role": "ROLE_USER", "parts": []any{}}}), -32602, "INVALID_PARAMS", "")
	// A value that the skill needs and is not there.
	wantRPCError(t, contractRPC(t, address, "Weather", "SendMessage",
		map[string]any{"message": map[string]any{"messageId": "m", "role": "ROLE_USER", "parts": []any{map[string]any{"data": map[string]any{"skill": "ask"}}}}}),
		-32602, "INVALID_PARAMS", "needs a value for `city`")
	// A value that the skill does not take.
	wantRPCError(t, contractRPC(t, address, "Weather", "SendMessage",
		map[string]any{"message": map[string]any{"messageId": "m", "role": "ROLE_USER", "parts": []any{map[string]any{"data": map[string]any{"skill": "ask", "arguments": map[string]any{"city": "Faro", "days": 3}}}}}}),
		-32602, "INVALID_PARAMS", "does not take `days`")
	// A skill that the agent does not have: the error lists the ones it has. (The original server
	// called it an unsupported operation, -32004; for this one it is a value that is not valid.)
	wantRPCError(t, contractRPC(t, address, "Weather", "SendMessage",
		map[string]any{"message": map[string]any{"messageId": "m", "role": "ROLE_USER", "parts": []any{map[string]any{"data": map[string]any{"skill": "dance"}}}}}),
		-32602, "INVALID_PARAMS", "it handles: ask, broken")
	// No task is kept, so none is ever found.
	wantRPCError(t, contractRPC(t, address, "Weather", "GetTask", map[string]any{"id": "nope"}), -32001, "TASK_NOT_FOUND", "")

	_, _, notJSON := contractRequest(t, http.MethodPost, "http://"+address+"/agents/Weather", "{oops", nil)
	wantRPCError(t, notJSON, -32700, "PARSE_ERROR", "")
	if id, present := notJSON["id"]; !present || id != nil {
		t.Errorf("the id of an answer to what could not be read must be null: %v", notJSON)
	}
	_, _, notTwo := contractRequest(t, http.MethodPost, "http://"+address+"/agents/Weather", `{"id":1,"method":"GetTask"}`, nil)
	wantRPCError(t, notTwo, -32600, "INVALID_REQUEST", "")
}

func TestAVersionOfTheProtocolThatIsNotOneIsRefused(t *testing.T) {
	address := serveSource(t, contractWeather)
	body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m","role":"ROLE_USER","metadata":{"skill":"ask"},"parts":[{"text":"Faro"}]}}}`
	for version, works := range map[string]bool{"0.3": false, "2.0": false, "1.0": true, "1": true, "": true} {
		headers := map[string]string{}
		if version != "" {
			headers["A2A-Version"] = version
		}
		_, _, reply := contractRequest(t, http.MethodPost, "http://"+address+"/agents/Weather", body, headers)
		if works {
			if at(reply, "result", "message", "parts", 0, "text") != "sunny in Faro" {
				t.Errorf("version %q: %v", version, reply)
			}
			continue
		}
		wantRPCError(t, reply, -32009, "VERSION_NOT_SUPPORTED", "")
	}
}

func TestAnswersSayThatTheyAreJSON(t *testing.T) {
	address := serveSource(t, contractWeather)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/agents/Weather" + cardSuffix, ""},
		{http.MethodPost, "/agents/Weather", `{"jsonrpc":"2.0","id":1,"method":"GetTask","params":{"id":"x"}}`},
	} {
		_, header, _ := contractRequest(t, request.method, "http://"+address+request.path, request.body, nil)
		if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Errorf("%s %s: Content-Type %q", request.method, request.path, ct)
		}
	}
}

// ---------- one Metagente calling another (integration/a2a_client.rs) ----------

// What an agent replies as a record travels as data, and comes back to the caller as a record.
func TestARecordRepliedByARemoteAgentComesBackAsARecord(t *testing.T) {
	address := serveSource(t, "agent Data\n  goal \"Give data\"\n  tool clock\n  accepts now\n  on now\n    reply clock.now\n")
	tool := remoteToolFor("http://"+address+"/agents/Data", "TOKEN", map[string]string{"TOKEN": goodToken})
	got, err := callTool(t, tool, nil, "now")
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != value.KindRecord || got.Record["unix"].Number < 1_700_000_000 {
		t.Errorf("the reply came back as %s", got.Describe())
	}
}

// ---------- many requests at once (integration/serve_concurrency.rs) ----------

// Fifty requests that each wait a second are served at the same time: one after another they
// would take fifty seconds.
func TestFiftyRequestsThatEachWaitASecondAreServedAtTheSameTime(t *testing.T) {
	address := serveSource(t, "agent Slowpoke\n  goal \"Wait, then answer\"\n  tool clock\n  accepts echo word\n  on echo\n    clock.wait seconds: 1\n    reply \"echo {word}\"\n")
	started := time.Now()
	var wg sync.WaitGroup
	problems := make(chan string, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("echo w%d", i)
			status, _, body, err := realPost(address, "/agents/Slowpoke", sendTo("echo", fmt.Sprintf(`{"word":"w%d"}`, i)))
			if err != nil || status != http.StatusOK || !strings.Contains(body, `"text":"`+want+`"`) {
				problems <- fmt.Sprintf("request %d: %d %s %v", i, status, body, err)
			}
		}(i)
	}
	wg.Wait()
	close(problems)
	for p := range problems {
		t.Error(p)
	}
	if took := time.Since(started); took > 10*time.Second {
		t.Errorf("50 requests took %s, so they were not served at the same time", took)
	}
}

// remoteToolFor is the client of this project for the agent at an address.
func remoteToolFor(url, credential string, env map[string]string) tools.Tool {
	pool := remote.NewPool(remote.Options{
		Allow:     func(remote.Spec) error { return nil },
		PollEvery: 5 * time.Millisecond,
		Getenv:    func(name string) string { return env[name] },
	})
	return pool.Tool("Remote", remote.Spec{Name: "Remote", URL: url, Credential: credential})
}
