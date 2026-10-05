package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func run(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func writeFile(t *testing.T, dir, name, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertContains(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

const weather = "agent Weather\n  goal \"Answer questions about the weather\"\n  tool weather from mcp \"npx -y weather-mcp\"\n  accepts ask city\n  on ask\n    forecast = weather.forecast city: city\n    reply \"In {city} it will be {forecast.summary}\"\n"

func TestNewAndCheckWorkTogether(t *testing.T) {
	t.Chdir(t.TempDir())

	code, stdout, _ := run(t, "new", "hello")
	if code != 0 {
		t.Fatalf("new failed with %d", code)
	}
	assertContains(t, stdout, "Created hello.ag", "Created metagente.toml")
	for _, name := range []string{"hello.ag", "metagente.toml"} {
		if _, err := os.Stat(name); err != nil {
			t.Errorf("%s was not created", name)
		}
	}

	code, _, stderr := run(t, "new", "hello")
	if code != 1 {
		t.Errorf("a second `new hello` exited with %d, want 1", code)
	}
	assertContains(t, stderr, "already exists")

	code, stdout, stderr = run(t, "check", "hello.ag")
	if code != 0 {
		t.Fatalf("check failed with %d:\n%s", code, stderr)
	}
	assertContains(t, stdout, "No problems found in hello.ag (1 agent).")
}

func TestCheckReportsEveryProblemAndHowManyThereAre(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "bad.ag", "agent Bad\n  accepts go\n  on go\n    x = nme\n    reply q\n")

	code, stdout, stderr := run(t, "check", "bad.ag")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	if stdout != "" {
		t.Errorf("nothing belongs on stdout, got %q", stdout)
	}
	assertContains(t, stderr,
		"Problem on line 1 of bad.ag: agent Bad has no goal",
		"I do not know what `nme` is here",
		"I do not know what `q` is here",
		"Found 3 problems.")
}

func TestCheckSaysProblemInTheSingular(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "bad.ag", "agent Bad\n  accepts go\n  on go\n    reply \"x\"\n")

	code, _, stderr := run(t, "check", "bad.ag")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "Found 1 problem.")
	if strings.Contains(stderr, "problems.") {
		t.Errorf("the plural was used for one problem:\n%s", stderr)
	}
}

func TestCheckShowsASyntaxErrorWithItsLine(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "typo.ag", "agent A\n  gaol \"x\"\n")

	code, _, stderr := run(t, "check", "typo.ag")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "Problem on line 2 of typo.ag", "did you mean `goal`?", "Fix: ")
}

// req: E2
func TestStrictTurnsWarningsIntoProblems(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "weather.ag", weather)

	code, stdout, stderr := run(t, "check", "weather.ag")
	if code != 0 {
		t.Fatalf("a warning must not fail check, exit code = %d:\n%s", code, stderr)
	}
	assertContains(t, stdout, "No problems found in weather.ag")
	assertContains(t, stderr, "Warning on line 3 of weather.ag", "does not pin a version")

	for _, args := range [][]string{{"check", "--strict", "weather.ag"}, {"check", "weather.ag", "--strict"}} {
		code, stdout, stderr = run(t, args...)
		if code != 1 {
			t.Errorf("%v: exit code = %d, want 1", args, code)
		}
		if stdout != "" {
			t.Errorf("%v: nothing belongs on stdout, got %q", args, stdout)
		}
		assertContains(t, stderr, "Found 1 problem.")
	}
}

func TestCheckOfAMissingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, stderr := run(t, "check", "nope.ag")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "I could not open nope.ag: the file does not exist", "Fix: check the file name")
}

func TestMistakesInHowCheckIsWrittenExitWithTwo(t *testing.T) {
	for _, args := range [][]string{
		{"check"},
		{"check", "a.ag", "b.ag"},
		{"check", "--lenient", "a.ag"},
	} {
		code, _, stderr := run(t, args...)
		if code != 2 {
			t.Errorf("%v: exit code = %d, want 2", args, code)
		}
		assertContains(t, stderr, "Problem: ", "Fix: ")
	}
}

func TestRunAnswersWithTheReplyOfTheAgent(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if code, _, stderr := run(t, "new", "hello"); code != 0 {
		t.Fatalf("new failed: %s", stderr)
	}

	for _, args := range [][]string{
		{"run", "hello.ag", "greet", "name=Ana"},
		{"run", "hello.ag", "name=Ana"}, // with one accepted message the message can be left out
	} {
		code, stdout, stderr := run(t, args...)
		if code != 0 || stdout != "Hello, Ana!\n" || stderr != "" {
			t.Errorf("%v: code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestRunPicksTheAgentWithTheAgentOption(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "two.ag", "agent First\n  goal \"a\"\n  accepts go\n  on go\n    reply \"first\"\n"+
		"agent Second\n  goal \"b\"\n  accepts go\n  on go\n    reply \"second\"\n")

	for args, want := range map[string]string{
		"two.ag go":                "first\n",
		"two.ag go --agent Second": "second\n",
		"two.ag go --agent=Second": "second\n",
		"--agent Second two.ag go": "second\n",
	} {
		code, stdout, stderr := run(t, append([]string{"run"}, strings.Fields(args)...)...)
		if code != 0 || stdout != want {
			t.Errorf("run %s: code %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
	code, _, stderr := run(t, "run", "two.ag", "go", "--agent", "Third")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "two.ag has no agent called `Third`", "it has: First, Second")
}

func TestRunPrintsRepliesThatAreNotTextAsJSON(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "list.ag", "agent L\n  goal \"x\"\n  accepts go\n  on go\n    reply [1, 2]\n")
	code, stdout, _ := run(t, "run", "list.ag", "go")
	if code != 0 || stdout != "[\n  1,\n  2\n]\n" {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
}

func TestRunStopsOnProblemsAndSaysHowManyMore(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "bad.ag", "agent Bad\n  accepts go\n  on go\n    x = nme\n    reply q\n")
	code, stdout, stderr := run(t, "run", "bad.ag", "go")
	if code != 1 || stdout != "" {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
	assertContains(t, stderr, "agent Bad has no goal", "more problems", "metagente check bad.ag")
}

func TestRunShowsWhereARuntimeProblemHappened(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "reader.ag", "agent R\n  goal \"x\"\n  tool file\n  accepts go\n  on go\n    x = file.read path: \"nope.txt\"\n    reply x\n")
	code, _, stderr := run(t, "run", "reader.ag", "go")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "Problem on line 6 of reader.ag", "does not exist")
	if strings.Contains(stderr, dir) {
		t.Errorf("the message shows where the project lives:\n%s", stderr)
	}
}

func TestRunOfAMissingFile(t *testing.T) {
	t.Chdir(t.TempDir())
	code, _, stderr := run(t, "run", "nope.ag")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "I could not open nope.ag: the file does not exist")
}

func TestRunShowsTheNotesOfTheConfiguration(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "metagente.toml", "[llm]\ncolour = \"red\"\n")
	if code, _, _ := run(t, "new", "hello"); code != 0 {
		t.Fatal("new failed")
	}
	code, _, stderr := run(t, "run", "hello.ag", "name=Ana")
	if code != 0 {
		t.Fatalf("exit code = %d:\n%s", code, stderr)
	}
	assertContains(t, stderr, "Note: metagente.toml has an unknown setting `colour` in [llm]; I ignored it")
}

func TestRunWithABrokenConfigurationExplainsIt(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "metagente.toml", "[runtime]\ntimeout_seconds = soon\n")
	writeFile(t, dir, "a.ag", "agent A\n  goal \"x\"\n  accepts go\n  on go\n    reply \"x\"\n")
	code, _, stderr := run(t, "run", "a.ag", "go")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "Problem on line 2 of metagente.toml", "Fix: ")
}

func TestMistakesInHowRunIsWrittenExitWithTwo(t *testing.T) {
	for _, args := range [][]string{
		{"run"},
		{"run", "--lenient", "a.ag"},
		{"run", "a.ag", "--agent"},
	} {
		code, _, stderr := run(t, args...)
		if code != 2 {
			t.Errorf("%v: exit code = %d, want 2", args, code)
		}
		assertContains(t, stderr, "Problem: ", "Fix: ")
	}
}

func TestVersionHelpAndUnknownCommands(t *testing.T) {
	code, stdout, _ := run(t, "--version")
	if code != 0 || stdout != "metagente "+Version+"\n" {
		t.Errorf("--version: code %d, stdout %q", code, stdout)
	}
	code, stdout, _ = run(t, "--help")
	if code != 0 {
		t.Errorf("--help: exit code = %d", code)
	}
	assertContains(t, stdout, "metagente check", "metagente new")

	code, _, stderr := run(t, "dance")
	if code != 2 {
		t.Errorf("unknown command: exit code = %d, want 2", code)
	}
	assertContains(t, stderr, "I do not know the command `dance`")

	if code, _, _ = run(t); code != 2 {
		t.Errorf("no command: exit code = %d, want 2", code)
	}
}

func TestNewNeedsExactlyOneName(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, args := range [][]string{{"new"}, {"new", "a", "b"}} {
		code, _, stderr := run(t, args...)
		if code != 2 {
			t.Errorf("%v: exit code = %d, want 2", args, code)
		}
		assertContains(t, stderr, "`new` needs one name")
	}
}

func TestCheckReportsAMissingLinkTarget(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "boss.ag", "agent Boss\n  goal \"b\"\n  link Ghost\n  accepts go\n  on go\n    reply Ghost.hi\n")
	code, stdout, stderr := run(t, "check", "boss.ag")
	if code != 1 || stdout != "" {
		t.Errorf("code %d, stdout %q", code, stdout)
	}
	assertContains(t, stderr, "Problem on line 3 of boss.ag", "I could not find an agent called Ghost", "I looked:")
}

func TestCheckAndRunFollowALinkToAnExistingAgent(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "helper.ag", "agent Helper\n  goal \"h\"\n  accepts hi\n  on hi\n    reply \"from helper\"\n")
	writeFile(t, dir, "boss.ag", "agent Boss\n  goal \"b\"\n  link Helper\n  accepts go\n  on go\n    reply Helper.hi\n")

	code, stdout, stderr := run(t, "check", "boss.ag")
	if code != 0 {
		t.Fatalf("check failed with %d:\n%s", code, stderr)
	}
	assertContains(t, stdout, "No problems found in boss.ag")

	code, stdout, stderr = run(t, "run", "boss.ag", "go")
	if code != 0 || stdout != "from helper\n" {
		t.Errorf("run: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestCheckRejectsACallThatTheLinkedAgentDoesNotAccept(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	writeFile(t, dir, "helper.ag", "agent Helper\n  goal \"h\"\n  accepts hi\n  on hi\n    reply \"x\"\n")
	writeFile(t, dir, "boss.ag", "agent Boss\n  goal \"b\"\n  link Helper\n  accepts go\n  on go\n    reply Helper.hii\n")
	code, _, stderr := run(t, "check", "boss.ag")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "Problem on line 6 of boss.ag", "does not accept the message `hii`", "did you mean `hi`?")
}

// ---------- trust ----------

const serverAgent = "agent A\n  goal \"x\"\n  tool weather from mcp \"/nonexistent/weather-server\" env \"PROXY\"\n  accepts go\n  on go\n    r = weather.forecast city: \"x\"\n    reply r\n"

// project makes a project folder to work in, with the approvals kept apart.
func project(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("METAGENTE_CONFIG_DIR", filepath.Join(t.TempDir(), "approvals"))
	return dir
}

// atTheKeyboard pretends a person is there and types the given answer.
func atTheKeyboard(t *testing.T, typed string) {
	t.Helper()
	oldIn, oldTerminal := stdin, isTerminal
	stdin, isTerminal = strings.NewReader(typed), func() bool { return true }
	t.Cleanup(func() { stdin, isTerminal = oldIn, oldTerminal })
}

// nobodyThere pretends there is no terminal, as in a script or a server.
func nobodyThere(t *testing.T) {
	t.Helper()
	oldTerminal := isTerminal
	isTerminal = func() bool { return false }
	t.Cleanup(func() { isTerminal = oldTerminal })
}

// req: T1
func TestTrustApprovesListsAndRevokes(t *testing.T) {
	dir := project(t)
	nobodyThere(t)
	writeFile(t, dir, "agent.ag", serverAgent)

	code, stdout, stderr := run(t, "trust", "--yes", "agent.ag")
	if code != 0 {
		t.Fatalf("trust failed with %d:\n%s", code, stderr)
	}
	assertContains(t, stdout, "[NEW] starts the program: /nonexistent/weather-server (it receives the variables PROXY)", "Approved 1 item")

	code, stdout, _ = run(t, "trust", "agent.ag")
	if code != 0 {
		t.Errorf("trusting again: exit code %d", code)
	}
	assertContains(t, stdout, "[approved]", "Everything is already approved")

	_, stdout, _ = run(t, "trust", "--list")
	assertContains(t, stdout, "starts the program: /nonexistent/weather-server", "set ")

	code, stdout, _ = run(t, "trust", "--revoke")
	if code != 0 {
		t.Errorf("revoking: exit code %d", code)
	}
	assertContains(t, stdout, "Removed 1 approval")
	_, stdout, _ = run(t, "trust", "--list")
	assertContains(t, stdout, "Nothing is approved yet.")
}

func TestTrustAsksWhenAPersonIsThere(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "agent.ag", serverAgent)

	atTheKeyboard(t, "n\n")
	code, stdout, stderr := run(t, "trust", "agent.ag")
	if code != 1 {
		t.Errorf("a no must exit with 1, got %d", code)
	}
	assertContains(t, stdout, "Nothing was approved.")
	assertContains(t, stderr, "Approve the NEW items for the project at")
	if _, stdout, _ := run(t, "trust", "--list"); !strings.Contains(stdout, "Nothing is approved yet.") {
		t.Errorf("a no approved something:\n%s", stdout)
	}

	atTheKeyboard(t, "yes\n")
	if code, _, stderr := run(t, "trust", "agent.ag"); code != 0 {
		t.Errorf("a yes must work, got %d:\n%s", code, stderr)
	}
}

func TestTrustCannotAskWithoutATerminal(t *testing.T) {
	dir := project(t)
	nobodyThere(t)
	writeFile(t, dir, "agent.ag", serverAgent)
	code, _, stderr := run(t, "trust", "agent.ag")
	if code != 1 {
		t.Errorf("exit code = %d, want 1", code)
	}
	assertContains(t, stderr, "I cannot ask you here, and nothing was approved", "--yes")
}

func TestTrustOfAFileThatNeedsNothing(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "plain.ag", "agent A\n  goal \"x\"\n  accepts go\n  on go\n    reply \"hi\"\n")
	code, stdout, _ := run(t, "trust", "plain.ag")
	if code != 0 {
		t.Errorf("exit code = %d", code)
	}
	assertContains(t, stdout, "Nothing in plain.ag needs approval")
}

func TestTrustIsWrittenCorrectly(t *testing.T) {
	project(t)
	for _, args := range [][]string{
		{"trust"},
		{"trust", "--list", "--revoke"},
		{"trust", "a.ag", "b.ag"},
		{"trust", "--nope", "a.ag"},
		{"trust", "--config"},
	} {
		if code, _, stderr := run(t, args...); code != 2 {
			t.Errorf("%v: exit code %d, want 2 (%s)", args, code, stderr)
		}
	}
}

// req: T1
func TestRunAsksBeforeApprovingAndRemembersTheAnswer(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "agent.ag", serverAgent)

	atTheKeyboard(t, "y\n")
	code, _, stderr := run(t, "run", "agent.ag", "go")
	assertContains(t, stderr, "This agent wants to:", "starts the program: /nonexistent/weather-server", "Approve this for the project at")
	// What fails after the yes is the program that is not installed.
	assertContains(t, stderr, "I could not start the tool server")
	if code != 1 {
		t.Errorf("exit code = %d", code)
	}

	// The second run asks nothing.
	atTheKeyboard(t, "")
	_, _, stderr = run(t, "run", "agent.ag", "go")
	if strings.Contains(stderr, "Approve this for the project") {
		t.Errorf("the person was asked again:\n%s", stderr)
	}
}

func TestRunRefusesWhenNobodyCanBeAsked(t *testing.T) {
	dir := project(t)
	nobodyThere(t)
	writeFile(t, dir, "agent.ag", serverAgent)
	code, _, stderr := run(t, "run", "agent.ag", "go")
	if code != 1 {
		t.Errorf("exit code = %d", code)
	}
	assertContains(t, stderr, "have not approved for this project", "metagente trust agent.ag")
	if strings.Contains(stderr, "I could not start") {
		t.Error("the program was tried although it was not approved")
	}
}

// ---------- think ----------

const weatherThinker = "agent W\n  goal \"Weather\"\n  accepts ask city\n  on ask\n    reply think \"weather in {city}?\"\n"

// modelServer pretends to be the Anthropic API and remembers who knocked.
type modelServer struct {
	*httptest.Server
	mu        sync.Mutex
	key, path string
	workspace string
	calls     int
}

// seen is what the server saw, read safely while it may still be answering.
func (m *modelServer) seen() (key, path string, calls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.key, m.path, m.calls
}

func newModelServer(t *testing.T, status int, body string) *modelServer {
	t.Helper()
	m := &modelServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.calls++
		m.key, m.path = r.Header.Get("x-api-key"), r.URL.Path
		m.workspace = r.Header.Get("anthropic-workspace-id")
		m.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(m.Close)
	return m
}

func useModel(t *testing.T, dir, address, key string) {
	t.Helper()
	writeFile(t, dir, "metagente.toml", "[llm]\nprovider = \"anthropic\"\nmodel = \"test-model\"\napi_key_env = \"TEST_MODEL_KEY\"\nbase_url = \""+address+"\"\n")
	t.Setenv("TEST_MODEL_KEY", key)
	writeFile(t, dir, "agent.ag", weatherThinker)
}

// req: T1, T3
func TestThinkEndToEndTheKeyGoesOnlyToAnApprovedAddress(t *testing.T) {
	dir := project(t)
	nobodyThere(t)
	model := newModelServer(t, 200, `{"content":[{"type":"text","text":"It will be sunny"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":4}}`)
	const key = "sk-secret-value-123"
	useModel(t, dir, model.URL, key)

	// The address is not the one the provider uses, so nothing is sent yet.
	code, _, stderr := run(t, "run", "agent.ag", "ask", "city=Lisbon")
	if code != 1 {
		t.Errorf("exit code = %d", code)
	}
	assertContains(t, stderr, "have not approved for this project",
		"sends your key and what the agent asks the language model to: "+model.URL)
	if _, _, calls := model.seen(); calls != 0 {
		t.Errorf("the model was reached before the address was approved (%d calls)", calls)
	}

	code, stdout, stderr := run(t, "trust", "--yes", "agent.ag")
	if code != 0 {
		t.Fatalf("trust failed:\n%s", stderr)
	}
	assertContains(t, stdout, "[NEW] sends your key", "Approved 1 item")

	code, stdout, stderr = run(t, "run", "agent.ag", "ask", "city=Lisbon")
	if code != 0 || stdout != "It will be sunny\n" {
		t.Fatalf("run: code %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	if seenKey, seenPath, _ := model.seen(); seenKey != key || seenPath != "/v1/messages" {
		t.Errorf("the model saw key %q at %s", seenKey, seenPath)
	}
	if strings.Contains(stdout+stderr, key) {
		t.Error("the key was shown")
	}
}

// req: T1, T3
func TestTheApprovalOfAModelOnThisComputerWithNoKeyDoesNotSayThatAKeyIsSent(t *testing.T) {
	dir := project(t)
	nobodyThere(t)
	t.Setenv("OPENAI_API_KEY", "")
	model := newModelServer(t, 200, `{}`)
	writeFile(t, dir, "metagente.toml", "[llm]\nprovider = \"openai-compatible\"\nmodel = \"m\"\nbase_url = \""+model.URL+"\"\n")
	writeFile(t, dir, "agent.ag", weatherThinker)
	const sentence = "sends what the agent asks the language model to, with no key (none is set): "

	code, _, stderr := run(t, "run", "agent.ag", "ask", "city=Lisbon")
	if code != 1 {
		t.Errorf("exit code = %d", code)
	}
	assertContains(t, stderr, "have not approved for this project", sentence+model.URL)

	code, stdout, stderr := run(t, "trust", "--yes", "agent.ag")
	if code != 0 {
		t.Fatalf("trust failed:\n%s", stderr)
	}
	assertContains(t, stdout, "[NEW] "+sentence+model.URL, "Approved 1 item")
	if strings.Contains(stdout+stderr, "sends your key") {
		t.Error("it said that a key is sent, and there is none")
	}
}

// req: L8
func TestAKeyTheProviderRefusesIsNeverShown(t *testing.T) {
	dir := project(t)
	nobodyThere(t)
	const key = "sk-refused-key-456"
	model := newModelServer(t, 401, `{"error":{"type":"authentication_error","message":"invalid x-api-key: `+key+`"}}`)
	useModel(t, dir, model.URL, key)
	if code, _, stderr := run(t, "trust", "--yes", "agent.ag"); code != 0 {
		t.Fatalf("trust failed:\n%s", stderr)
	}
	code, stdout, stderr := run(t, "run", "agent.ag", "ask", "city=Lisbon")
	if code != 1 {
		t.Errorf("exit code = %d", code)
	}
	assertContains(t, stderr, "the language model could not answer", "answered 401", "invalid x-api-key", "[hidden]",
		"check the [llm] settings in metagente.toml")
	if strings.Contains(stdout+stderr, key) {
		t.Errorf("the key is in the output:\n%s", stderr)
	}
}

func TestThinkWithoutTheKeyVariableSaysWhichOneToSet(t *testing.T) {
	dir := project(t)
	nobodyThere(t)
	model := newModelServer(t, 200, `{}`)
	useModel(t, dir, model.URL, "")
	if code, _, stderr := run(t, "trust", "--yes", "agent.ag"); code != 0 {
		t.Fatalf("trust failed:\n%s", stderr)
	}
	_, _, stderr := run(t, "run", "agent.ag", "ask", "city=Lisbon")
	assertContains(t, stderr, "the language model could not answer: the variable TEST_MODEL_KEY is not set", "export TEST_MODEL_KEY")
	if _, _, calls := model.seen(); calls != 0 {
		t.Error("the model was reached without a key")
	}
}

func TestThinkSendsTheWorkspaceOfTheKeyWhenTheConfigurationNamesOne(t *testing.T) {
	dir := project(t)
	nobodyThere(t)
	model := newModelServer(t, 200, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	useModel(t, dir, model.URL, "sk-secret-value-123")
	toml, err := os.ReadFile(filepath.Join(dir, "metagente.toml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "metagente.toml", string(toml)+"workspace_id = \"wrkspc_01Abc\"\n")
	if code, _, stderr := run(t, "trust", "--yes", "agent.ag"); code != 0 {
		t.Fatalf("trust failed:\n%s", stderr)
	}
	if code, _, stderr := run(t, "run", "agent.ag", "ask", "city=Lisbon"); code != 0 {
		t.Fatalf("run failed:\n%s", stderr)
	}
	model.mu.Lock()
	got := model.workspace
	model.mu.Unlock()
	if got != "wrkspc_01Abc" {
		t.Errorf("the workspace the model saw = %q", got)
	}
}

// ---------- internal failures ----------

// req: P2, P3, P4
func TestAnInternalFailureShowsOneSentenceAndKeepsTheDetailsInTheLog(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("METAGENTE_STATE_DIR", state)
	const key = "sk-ant-api03-abcdef0123456789"
	t.Setenv("ANTHROPIC_API_KEY", key)

	var out, errOut bytes.Buffer
	code := mainWith(nil, &out, &errOut, func([]string, io.Writer, io.Writer) int {
		panic("deliberate failure with " + key)
	})
	if code != 1 || out.Len() != 0 {
		t.Errorf("code %d, stdout %q", code, out.String())
	}
	shown := errOut.String()
	assertContains(t, shown, "Something went wrong inside Metagente. This is not a mistake in your agent.",
		"The details were saved in "+filepath.Join(state, "metagente.log"))
	for _, leak := range []string{"deliberate", "goroutine", "panic", key, ".go:"} {
		if strings.Contains(shown, leak) {
			t.Errorf("the person was shown %q:\n%s", leak, shown)
		}
	}

	raw, err := os.ReadFile(filepath.Join(state, "metagente.log"))
	if err != nil {
		t.Fatal(err)
	}
	logged := string(raw)
	assertContains(t, logged, "PANIC main: deliberate failure with [hidden]", "goroutine")
	if strings.Contains(logged, key) {
		t.Errorf("the key is in the log:\n%s", logged)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(filepath.Join(state, "metagente.log")); info.Mode().Perm() != 0o600 {
			t.Errorf("the log has permissions %v", info.Mode().Perm())
		}
	}
}

func TestACommandThatWorksLeavesNoLog(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("METAGENTE_STATE_DIR", state)
	var out, errOut bytes.Buffer
	if code := mainWith([]string{"--version"}, &out, &errOut, Run); code != 0 {
		t.Fatalf("code %d: %s", code, errOut.String())
	}
	if _, err := os.Stat(state); err == nil {
		t.Error("a log folder was made although nothing failed")
	}
}

// req: S1
func TestTheTokenCommandPrintsOnlyAGoodToken(t *testing.T) {
	code, stdout, stderr := run(t, "token")
	if code != 0 || stderr != "" {
		t.Fatalf("code %d, stderr %q", code, stderr)
	}
	token := strings.TrimSuffix(stdout, "\n")
	if strings.ContainsAny(token, " \n") || len(token) < 43 {
		t.Errorf("output %q", stdout)
	}
	_, other, _ := run(t, "token")
	if other == stdout {
		t.Error("two calls gave the same token")
	}
	if code, _, stderr := run(t, "token", "extra"); code != 2 || !strings.Contains(stderr, "takes no arguments") {
		t.Errorf("code %d, stderr %q", code, stderr)
	}
}
