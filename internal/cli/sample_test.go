package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Ribeiro/metagente-go/internal/lang"
)

// The City Briefing sample (samples/city-briefing), run offline: a scripted model in place of
// Claude, a fake fetch server in place of `uvx mcp-server-fetch`, and the real files of the sample.
// The Researcher is really served and the Concierge really run, through the commands a person types.
// This is the port of `integration/sample_city_briefing.rs` of the original project.

// sampleDir is the folder of the sample, found from the folder of this package before any test
// moves to a folder of its own.
var sampleDir = func() string {
	wd, _ := os.Getwd()
	return filepath.Join(wd, "..", "..", "samples", "city-briefing")
}()

const (
	sampleKeyVar   = "MG_SAMPLE_TEST_KEY" // the staged copies read their key from here, never the real one
	sampleFetch    = "uvx mcp-server-fetch==2026.8.18"
	sampleAddress  = "http://127.0.0.1:8080/agents/Researcher"
	sampleModel    = "claude-sonnet-5-5"
	sampleTokenVar = "RESEARCHER_TOKEN"
)

func readSample(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(sampleDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// stageSample copies the sample into the folder of the test, changing only what points at real
// services: the command of the fetch server, the address of the Researcher, and where the model is.
func stageSample(t *testing.T, dir, fetch, researcher, modelURL string) {
	t.Helper()
	for _, name := range []string{"README.md", "metagente.toml", "concierge.ag", "researcher.ag"} {
		text := readSample(t, name)
		switch name {
		case "researcher.ag":
			text = strings.Replace(text, sampleFetch, fetch, 1)
		case "concierge.ag":
			text = strings.Replace(text, sampleAddress, researcher, 1)
		case "metagente.toml":
			text = strings.Replace(text, `api_key_env = "ANTHROPIC_API_KEY"`,
				`api_key_env = "`+sampleKeyVar+`"`+"\nbase_url = \""+modelURL+`"`, 1)
		}
		writeFile(t, dir, name, text)
	}
	t.Setenv(sampleKeyVar, "test-key")
}

// ---------- a model that answers from a script ----------

type scriptedModel struct {
	*httptest.Server
	mu      sync.Mutex
	replies []string
	bodies  []map[string]any
	paths   []string
	keys    []string
}

func newScriptedModel(t *testing.T, replies ...string) *scriptedModel {
	t.Helper()
	m := &scriptedModel{replies: replies}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		m.mu.Lock()
		defer m.mu.Unlock()
		m.bodies = append(m.bodies, body)
		m.paths = append(m.paths, r.URL.Path)
		m.keys = append(m.keys, r.Header.Get("x-api-key"))
		if len(m.replies) == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, `{"error":{"type":"api_error","message":"the script has no more answers"}}`)
			return
		}
		reply := m.replies[0]
		m.replies = m.replies[1:]
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(m.Close)
	return m
}

func (m *scriptedModel) seen() (bodies []map[string]any, paths, keys []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]map[string]any(nil), m.bodies...), append([]string(nil), m.paths...), append([]string(nil), m.keys...)
}

func textReply(text string) string {
	raw, _ := json.Marshal(map[string]any{
		"content":     []any{map[string]any{"type": "text", "text": text}},
		"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 10, "output_tokens": 10},
	})
	return string(raw)
}

func toolUse(id, name string, input map[string]any) string {
	raw, _ := json.Marshal(map[string]any{
		"content":     []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}},
		"stop_reason": "tool_use", "usage": map[string]any{"input_tokens": 10, "output_tokens": 10},
	})
	return string(raw)
}

// researcherScript is what the model says when the Researcher asks it about a city: fetch the page,
// then write the facts.
func researcherScript(city string) []string {
	return []string{
		toolUse("call-1", "fetch__fetch", map[string]any{"url": "https://en.wikipedia.org/wiki/" + city}),
		textReply(city + " is a city with a river. It has old streets. People love it."),
	}
}

// fullScript adds the briefing that the Concierge asks for.
func fullScript(city string) []string {
	return append(researcherScript(city), textReply("Welcome to "+city+"! It has a river and old streets. Enjoy your visit."))
}

// ---------- a fetch server that is not on the internet ----------

type fakeFetch struct {
	*httptest.Server
	mu      sync.Mutex
	fetched []string
}

type fetchIn struct {
	URL string `json:"url" jsonschema:"the address to fetch"`
}

// newFakeFetch is a tool server with the one tool that matters of mcp-server-fetch: `fetch`, which
// answers a page for any city, and fails as the real one does for the city that has none.
func newFakeFetch(t *testing.T) *fakeFetch {
	t.Helper()
	f := &fakeFetch{}
	server := sdk.NewServer(&sdk.Implementation{Name: "fake-fetch", Version: "1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "fetch", Description: "Fetch a URL and return its content"},
		func(_ context.Context, _ *sdk.CallToolRequest, in fetchIn) (*sdk.CallToolResult, any, error) {
			f.mu.Lock()
			f.fetched = append(f.fetched, in.URL)
			f.mu.Unlock()
			if strings.HasSuffix(in.URL, "/Xyzzyplugh") {
				return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: "Failed to fetch " + in.URL + " - status code 404"}}}, nil, nil
			}
			return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: "Contents of " + in.URL + ": a city with a river and old streets."}}}, nil, nil
		})
	f.Server = httptest.NewServer(sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, nil))
	t.Cleanup(f.Close)
	return f
}

func (f *fakeFetch) urls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.fetched...)
}

// ---------- the whole demo ----------

// wholeFlow runs the demo as the README says: approve, serve the Researcher, ask the Concierge.
func wholeFlow(t *testing.T, model *scriptedModel, fetch *fakeFetch, modelName string) (briefing string) {
	t.Helper()
	dir := project(t)
	nobodyThere(t)
	stageSample(t, dir, fetch.URL, sampleAddress, model.URL)
	if modelName != sampleModel {
		toml := readSample(t, "metagente.toml")
		writeFile(t, dir, "metagente.toml", strings.Replace(
			strings.Replace(toml, `model = "`+sampleModel+`"`, `model = "`+modelName+`"`, 1),
			`api_key_env = "ANTHROPIC_API_KEY"`, `api_key_env = "`+sampleKeyVar+`"`+"\nbase_url = \""+model.URL+`"`, 1))
	}
	if code, _, stderr := run(t, "trust", "--yes", "researcher.ag"); code != 0 {
		t.Fatalf("trust researcher.ag:\n%s", stderr)
	}
	live := startServe(t, []string{"researcher.ag", "--port", "0"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)
	t.Cleanup(func() { live.stop(t) })

	// The Concierge points at the Researcher that now listens, and carries its token.
	concierge := strings.Replace(readSample(t, "concierge.ag"), sampleAddress, "http://"+live.address+"/agents/Researcher", 1)
	writeFile(t, dir, "concierge.ag", concierge)
	t.Setenv(sampleTokenVar, testToken)
	if code, _, stderr := run(t, "trust", "--yes", "concierge.ag"); code != 0 {
		t.Fatalf("trust concierge.ag:\n%s", stderr)
	}
	code, stdout, stderr := run(t, "run", "concierge.ag", "city=Lisbon")
	if code != 0 {
		t.Fatalf("run concierge.ag: %d\n%s", code, stderr)
	}
	return strings.TrimSpace(stdout)
}

func TestTheConciergeAsksTheResearcherOverA2AAndWritesTheBriefing(t *testing.T) {
	model, fetch := newScriptedModel(t, fullScript("Lisbon")...), newFakeFetch(t)
	briefing := wholeFlow(t, model, fetch, sampleModel)
	if briefing != "Welcome to Lisbon! It has a river and old streets. Enjoy your visit." {
		t.Errorf("briefing %q", briefing)
	}
	bodies, paths, keys := model.seen()
	if len(bodies) != 3 {
		t.Fatalf("%d requests to the model; want 3: the fetch and the facts of the Researcher, the briefing of the Concierge", len(bodies))
	}
	for i := range bodies {
		if bodies[i]["model"] != sampleModel || paths[i] != "/v1/messages" || keys[i] != "test-key" {
			t.Errorf("request %d: model %v, path %s, key %q", i, bodies[i]["model"], paths[i], keys[i])
		}
	}
	checkOfferedTools(t, bodies[0])
	// The page that was fetched is the one of the city, and the facts and the time travelled over A2A.
	if urls := fetch.urls(); len(urls) != 1 || !strings.HasSuffix(urls[0], "/Lisbon") {
		t.Errorf("fetched %v", urls)
	}
	last, _ := json.Marshal(bodies[2])
	if !strings.Contains(string(last), "Lisbon is a city with a river.") || !strings.Contains(string(last), "(checked 20") {
		t.Errorf("the Concierge did not write from what the Researcher sent:\n%s", last)
	}
}

// checkOfferedTools wants the Researcher to offer the model only what it declared: fetch and the clock.
func checkOfferedTools(t *testing.T, body map[string]any) {
	t.Helper()
	var offered []string
	tools, _ := body["tools"].([]any)
	for _, tool := range tools {
		name, _ := tool.(map[string]any)["name"].(string)
		offered = append(offered, name)
		if !strings.HasPrefix(name, "fetch__") && !strings.HasPrefix(name, "clock__") {
			t.Errorf("the model was offered %s, which the Researcher did not declare", name)
		}
	}
	if joined := strings.Join(offered, " "); !strings.Contains(joined, "fetch__fetch") || !strings.Contains(joined, "clock__now") {
		t.Errorf("offered %v", offered)
	}
}

func TestChangingOnlyTheModelLineChangesTheModelOfBothAgents(t *testing.T) {
	fetch := newFakeFetch(t)
	for _, name := range []string{sampleModel, "claude-haiku-4-5"} {
		t.Run(name, func(t *testing.T) {
			model := newScriptedModel(t, fullScript("Lisbon")...)
			wholeFlow(t, model, fetch, name)
			bodies, _, _ := model.seen()
			if len(bodies) != 3 {
				t.Fatalf("%d requests", len(bodies))
			}
			for i, body := range bodies {
				if body["model"] != name {
					t.Errorf("request %d used %v", i, body["model"])
				}
			}
		})
	}
}

// The messages of the troubleshooting table of the README are the ones the program prints.
func TestTheTroubleshootingOfTheReadmeSaysWhatTheProgramPrints(t *testing.T) {
	readme := readSample(t, "README.md")
	model, fetch := newScriptedModel(t, fullScript("Lisbon")...), newFakeFetch(t)
	dir := project(t)
	nobodyThere(t)
	stageSample(t, dir, fetch.URL, sampleAddress, model.URL)
	if code, _, stderr := run(t, "trust", "--yes", "researcher.ag"); code != 0 {
		t.Fatalf("trust:\n%s", stderr)
	}
	live := startServe(t, []string{"researcher.ag", "--port", "0"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)
	t.Cleanup(func() { live.stop(t) })
	writeFile(t, dir, "concierge.ag", strings.Replace(readSample(t, "concierge.ag"), sampleAddress, "http://"+live.address+"/agents/Researcher", 1))
	if code, _, stderr := run(t, "trust", "--yes", "concierge.ag"); code != 0 {
		t.Fatalf("trust:\n%s", stderr)
	}

	// A token that is not the one of the Researcher.
	t.Setenv(sampleTokenVar, "wrong-0123456789-abcdefghij-ABCDEFGHIJ")
	_, _, stderr := run(t, "run", "concierge.ag", "city=Lisbon")
	assertContains(t, stderr, "answered 401 when I asked for the agent card of Researcher", sampleTokenVar)
	assertContains(t, readme, "`answered 401 when I asked for the agent card of Researcher`")

	// The Researcher is not running.
	live.stop(t)
	t.Setenv(sampleTokenVar, testToken)
	_, _, stderr = run(t, "run", "concierge.ag", "city=Lisbon")
	assertContains(t, stderr, "I could not reach 127.0.0.1:", "(remote agent Researcher)")
	assertContains(t, readme, "`I could not reach 127.0.0.1:8080 (remote agent Researcher)`")

	// No key.
	t.Setenv(sampleKeyVar, "")
	_, _, stderr = run(t, "run", "researcher.ag", "city=Lisbon")
	assertContains(t, stderr, "the language model could not answer: the variable "+sampleKeyVar+" is not set")
	assertContains(t, readme, "`the language model could not answer: the variable ANTHROPIC_API_KEY is not set`")

	// More than the key copied into the variable.
	t.Setenv(sampleKeyVar, "sk-ant-abc\nwc -c < ~/.key")
	_, _, stderr = run(t, "run", "researcher.ag", "city=Lisbon")
	assertContains(t, stderr, "the variable "+sampleKeyVar+" holds a line break, which cannot be part of a key")
	assertContains(t, readme, "`the variable ANTHROPIC_API_KEY holds a line break, which cannot be part of a key`")
}

// stageResearcher stages the sample and approves the Researcher, to run it alone.
func stageResearcher(t *testing.T, fetch string, model *scriptedModel) {
	t.Helper()
	dir := project(t)
	nobodyThere(t)
	stageSample(t, dir, fetch, sampleAddress, model.URL)
	if code, _, stderr := run(t, "trust", "--yes", "researcher.ag"); code != 0 {
		t.Fatalf("trust researcher.ag:\n%s", stderr)
	}
}

func TestAnEmptyCityStopsEarlyAndAPageThatIsNotThereBecomesNoFactsFound(t *testing.T) {
	model := newScriptedModel(t,
		toolUse("call-1", "fetch__fetch", map[string]any{"url": "https://en.wikipedia.org/wiki/Xyzzyplugh"}),
		textReply("No facts were found for Xyzzyplugh."))
	stageResearcher(t, newFakeFetch(t).URL, model)

	code, _, stderr := run(t, "run", "researcher.ag", "city=")
	if code == 0 {
		t.Error("an empty city was answered")
	}
	assertContains(t, stderr, "I need a city", "researcher.ag", "line ")
	if bodies, _, _ := model.seen(); len(bodies) != 0 {
		t.Error("the model was asked about an empty city")
	}

	code, stdout, stderr := run(t, "run", "researcher.ag", "city=Xyzzyplugh")
	if code != 0 || !strings.HasPrefix(stdout, "No facts were found for Xyzzyplugh.") {
		t.Fatalf("code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// The fetch that failed was given to the model to read, not shown to the person as a failure.
	bodies, _, _ := model.seen()
	second, _ := json.Marshal(bodies[1])
	if !strings.Contains(string(second), "Failed to fetch") || !strings.Contains(string(second), "404") {
		t.Errorf("the model was not told the fetch failed:\n%s", second)
	}
}

func TestAFetchServerThatCannotStartIsExplainedAndTheModelIsNotAsked(t *testing.T) {
	model := newScriptedModel(t, researcherScript("Lisbon")...)
	stageResearcher(t, "definitely-not-a-real-program --serve", model)
	code, _, stderr := run(t, "run", "researcher.ag", "city=Lisbon")
	if code == 0 {
		t.Fatal("it worked without its tool server")
	}
	assertContains(t, stderr, "definitely-not-a-real-program")
	if bodies, _, _ := model.seen(); len(bodies) != 0 {
		t.Errorf("the model was asked %d times without its tools", len(bodies))
	}
}

func TestWithoutTheKeyTheResearcherSaysWhichVariableIsNotSet(t *testing.T) {
	model := newScriptedModel(t, researcherScript("Lisbon")...)
	dir := project(t)
	nobodyThere(t)
	stageSample(t, dir, newFakeFetch(t).URL, sampleAddress, model.URL)
	toml := strings.Replace(readSample(t, "metagente.toml"), `api_key_env = "ANTHROPIC_API_KEY"`,
		`api_key_env = "ANTHROPIC_API_KEY"`+"\nbase_url = \""+model.URL+`"`, 1)
	writeFile(t, dir, "metagente.toml", toml)
	t.Setenv("ANTHROPIC_API_KEY", "")
	if code, _, stderr := run(t, "trust", "--yes", "researcher.ag"); code != 0 {
		t.Fatalf("trust:\n%s", stderr)
	}
	code, _, stderr := run(t, "run", "researcher.ag", "city=Lisbon")
	if code == 0 {
		t.Fatal("it ran without a key")
	}
	assertContains(t, stderr, "ANTHROPIC_API_KEY", "not set", "Fix: ")
	for _, word := range []string{"panic", "goroutine", ".go:"} {
		if strings.Contains(stderr, word) {
			t.Errorf("%q in:\n%s", word, stderr)
		}
	}
	if bodies, _, _ := model.seen(); len(bodies) != 0 {
		t.Error("the model was asked without a key")
	}
}

// ---------- the files of the sample ----------

func TestTheAgentsOfTheSampleDeclareOnlyWhatTheyNeed(t *testing.T) {
	researcher, err := lang.ParseFile("researcher.ag", "", readSample(t, "researcher.ag"))
	if err != nil {
		t.Fatal(err)
	}
	var declared []string
	for _, tool := range researcher[0].Tools {
		declared = append(declared, tool.Name)
	}
	if strings.Join(declared, " ") != "fetch clock" || researcher[0].Tools[0].Kind != lang.ToolMCP ||
		len(researcher[0].Links) != 0 || len(researcher[0].Remotes) != 0 {
		t.Errorf("the Researcher declares %v, %d links, %d remotes", declared, len(researcher[0].Links), len(researcher[0].Remotes))
	}
	concierge, err := lang.ParseFile("concierge.ag", "", readSample(t, "concierge.ag"))
	if err != nil {
		t.Fatal(err)
	}
	c := concierge[0]
	if len(c.Tools) != 0 || len(c.Links) != 0 || len(c.Remotes) != 1 || c.Remotes[0].Name != "Researcher" || c.Remotes[0].URL != sampleAddress {
		t.Errorf("the Concierge declares %d tools, %d links, remotes %+v", len(c.Tools), len(c.Links), c.Remotes)
	}
	if len(c.Accepts) != 1 || c.Accepts[0].Message != "brief" || strings.Join(c.Accepts[0].Params, " ") != "city" {
		t.Errorf("the Concierge accepts %+v", c.Accepts)
	}
	for _, file := range []string{"researcher.ag", "concierge.ag"} {
		checkNamesNoModel(t, file)
	}
	toml := readSample(t, "metagente.toml")
	assertContains(t, toml, `provider = "anthropic"`, `model = "`+sampleModel+`"`, sampleTokenVar)
}

func TestTheReadmeOfTheSampleCoversEverythingAndShowsTheAgentsAsTheyAre(t *testing.T) {
	index, err := os.ReadFile(filepath.Join(sampleDir, "..", "README.md"))
	if err != nil || !strings.Contains(string(index), "city-briefing/") {
		t.Errorf("samples/README.md does not list the sample: %v", err)
	}
	readme := readSample(t, "README.md")
	var headings []string
	for _, line := range strings.Split(readme, "\n") {
		if strings.HasPrefix(line, "## ") {
			headings = append(headings, strings.ToLower(line))
		}
	}
	for _, needed := range []string{"prerequisites", "configur", "running", "question", "a2a", "troubleshoot"} {
		if !strings.Contains(strings.Join(headings, "\n"), needed) {
			t.Errorf("the README has no section about %q: %v", needed, headings)
		}
	}
	assertContains(t, readme, "You should see something like", "metagente serve researcher.ag", "metagente run concierge.ag",
		"metagente trust researcher.ag", "metagente trust concierge.ag", "metagente token", "uv", "ANTHROPIC_API_KEY", sampleTokenVar,
		readSample(t, "metagente.toml")[strings.Index(readSample(t, "metagente.toml"), "[llm]"):len(strings.TrimRight(readSample(t, "metagente.toml"), "\n"))])
	for _, file := range []string{"researcher.ag", "concierge.ag"} {
		if !strings.Contains(readme, strings.TrimRight(readSample(t, file), "\n")) {
			t.Errorf("the README does not show %s as it is", file)
		}
	}
	for _, line := range codeLines(readme) {
		if strings.Contains(line, "--public") {
			t.Errorf("a command of the README uses --public, which the demo never needs: %s", line)
		}
	}
}

// checkNamesNoModel wants a file of the sample to pass check --strict and to name no provider or
// model: which model is used is written only in metagente.toml.
func checkNamesNoModel(t *testing.T, file string) {
	t.Helper()
	if code, _, stderr := run(t, "check", "--strict", filepath.Join(sampleDir, file)); code != 0 {
		t.Errorf("check --strict %s:\n%s", file, stderr)
	}
	text := strings.ToLower(readSample(t, file))
	for _, word := range []string{"anthropic", "claude", "sonnet", "model", "provider"} {
		if strings.Contains(text, word) {
			t.Errorf("%s names `%s`: that belongs in metagente.toml", file, word)
		}
	}
}

// codeLines are the lines of a Markdown text that are inside blocks of code.
func codeLines(text string) []string {
	var lines []string
	inCode := false
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inCode = !inCode
		} else if inCode {
			lines = append(lines, line)
		}
	}
	return lines
}

// The guide for two computers changes the address of the Researcher with sed, and names the pinned fetch
// server: if the sample changes either, the guide has to change with it.
func TestTheGuideForTwoComputersFollowsTheSample(t *testing.T) {
	guide := readSample(t, "TWO-COMPUTERS.md")
	assertContains(t, guide, "s|"+sampleAddress+"|", sampleFetch+" --help", "--host $LAB_IP:8443")
	assertContains(t, readSample(t, "README.md"), "(TWO-COMPUTERS.md)")

	// The script does the same steps, so it names the same address and fetch server, and it is
	// valid shell.
	script := readSample(t, "two-computers.sh")
	assertContains(t, script, `SAMPLE_ADDRESS="`+sampleAddress+`"`, `FETCH="`+sampleFetch+`"`, "--host $ip:$PORT")
	// On Windows `bash` may be the one of WSL, which fails without a distribution; the script is for
	// macOS and Linux.
	if runtime.GOOS == "windows" {
		return
	}
	if out, err := exec.Command("bash", "-n", filepath.Join(sampleDir, "two-computers.sh")).CombinedOutput(); err != nil {
		t.Errorf("bash -n two-computers.sh: %v\n%s", err, out)
	}
}

func TestTheSampleStaysSmallAndHoldsNoKey(t *testing.T) {
	if n := strings.Count(readSample(t, "researcher.ag"), "\n"); n >= 20 {
		t.Errorf("researcher.ag has %d lines", n)
	}
	if n := strings.Count(readSample(t, "concierge.ag"), "\n"); n >= 10 {
		t.Errorf("concierge.ag has %d lines", n)
	}
	looksLikeAKey := regexp.MustCompile(`sk-[A-Za-z0-9_-]{10,}`)
	checked := 0
	err := filepath.Walk(filepath.Join(sampleDir, ".."), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		checked++
		if looksLikeAKey.Match(raw) {
			t.Errorf("%s looks like it holds a key", path)
		}
		return nil
	})
	if err != nil || checked < 5 {
		t.Errorf("checked %d files: %v", checked, err)
	}
}
