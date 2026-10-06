package lang

import (
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

// checkSource parses and checks a source text. A syntax error comes back as
// the only problem, the way `metagente check` shows it.
func checkSource(t *testing.T, source string) Result {
	t.Helper()
	agents, err := ParseFile("golden.ag", "", source)
	if err != nil {
		d, ok := diag.From(err)
		if !ok {
			t.Fatalf("the error is not a diagnostic: %v", err)
		}
		return Result{Problems: []*diag.Diagnostic{d}}
	}
	return Check(agents)
}

// located asserts what every problem must have (where it is, what is wrong,
// how to fix it) and returns its text.
func located(t *testing.T, d *diag.Diagnostic) string {
	t.Helper()
	text := d.Render()
	if !strings.HasPrefix(text, "Problem on line ") {
		t.Errorf("no line reference:\n%s", text)
	}
	if !strings.Contains(text, "\nFix: ") {
		t.Errorf("no suggested fix:\n%s", text)
	}
	assertPlain(t, text)
	return text
}

func firstProblem(t *testing.T, source string) string {
	t.Helper()
	res := checkSource(t, source)
	if len(res.Problems) == 0 {
		t.Fatalf("expected a problem for:\n%s", source)
	}
	return located(t, res.Problems[0])
}

func assertContains(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func TestTheReferenceAgentHasNoProblems(t *testing.T) {
	res := checkSource(t, weatherSource)
	if len(res.Problems) != 0 {
		t.Errorf("unexpected problems: %v", res.Problems)
	}
}

func TestSyntaxErrorIsLocatedAndSuggestsAFix(t *testing.T) {
	text := firstProblem(t, "agent A\n  gaol \"x\"\n")
	assertContains(t, text, "did you mean `goal`?")
}

func TestUndeclaredName(t *testing.T) {
	text := firstProblem(t, "agent A\n  goal \"x\"\n  accepts go\n  on go\n    reply nme\n")
	assertContains(t, text, "I do not know what `nme` is here")
}

func TestUndeclaredName_SuggestsTheClosestKnownOne(t *testing.T) {
	text := firstProblem(t, "agent A\n  goal \"x\"\n  accepts ask city\n  on ask\n    reply cty\n")
	assertContains(t, text, "did you mean `city`?")
}

func TestUndeclaredBuiltInTool(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  accepts go\n  on go\n    x = file.read path: \"a\"\n    reply x\n"
	res := checkSource(t, source)
	if len(res.Problems) != 1 {
		t.Fatalf("got %d problems, want exactly 1: %v", len(res.Problems), res.Problems)
	}
	text := located(t, res.Problems[0])
	assertContains(t, text, "never declared it", "tool file", "Problem on line 5")
}

func TestBadParameterForABuiltInTool(t *testing.T) {
	text := firstProblem(t, "agent A\n  goal \"x\"\n  tool file\n  accepts go\n  on go\n    x = file.read pth: \"a\"\n    reply x\n")
	assertContains(t, text, "needs a value for `path`")

	all := checkSource(t, "agent A\n  goal \"x\"\n  tool file\n  accepts go\n  on go\n    x = file.read pth: \"a\"\n    reply x\n")
	if len(all.Problems) != 2 {
		t.Fatalf("got %d problems, want 2", len(all.Problems))
	}
	assertContains(t, located(t, all.Problems[1]), "does not take `pth`", "did you mean `path`?")
}

func TestUnknownActionOfABuiltInTool(t *testing.T) {
	text := firstProblem(t, "agent A\n  goal \"x\"\n  tool file\n  accepts go\n  on go\n    x = file.reed path: \"a\"\n    reply x\n")
	assertContains(t, text, "did you mean `file.read`?", "`file` can do: read, write")
}

func TestHandlerWithoutAcceptsAndAcceptsWithoutHandler(t *testing.T) {
	res := checkSource(t, "agent A\n  goal \"x\"\n  accepts one\n  on two\n    reply \"x\"\n")
	if len(res.Problems) != 2 {
		t.Fatalf("got %d problems, want 2: %v", len(res.Problems), res.Problems)
	}
	for _, d := range res.Problems {
		located(t, d)
	}
}

func TestMissingGoal(t *testing.T) {
	text := firstProblem(t, "agent A\n  accepts go\n  on go\n    reply \"x\"\n")
	assertContains(t, text, "has no goal")
}

func TestDuplicateNamesAndMessages(t *testing.T) {
	res := checkSource(t, "agent A\n  goal \"x\"\n  tool clock\n  link clock\n  accepts go\n  accepts go\n  on go\n    reply \"x\"\n")
	var text string
	for _, d := range res.Problems {
		text += located(t, d)
	}
	assertContains(t, text, "`accepts go` appears twice", "the name `clock` is declared more than once")
}

func TestAssigningToAToolNameIsRefused(t *testing.T) {
	text := firstProblem(t, "agent A\n  goal \"x\"\n  tool clock\n  accepts go\n  on go\n    clock = 1\n    reply \"x\"\n")
	assertContains(t, text, "`clock` is already the name of a tool, link, or remote")
}

// ---------- X1: names that were never declared ----------

// req: X1
func TestACallToANameThatWasNeverDeclaredIsRefusedBeforeRunning(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  accepts go\n  on go\n    r = Other.ask city: \"x\"\n    reply r\n"
	res := checkSource(t, source)
	if len(res.Problems) != 1 {
		t.Fatalf("got %d problems, want exactly 1: %v", len(res.Problems), res.Problems)
	}
	text := located(t, res.Problems[0])
	assertContains(t, text, "does not know anything called `Other`", "`link", "`remote", "from mcp")
}

func TestDeclaredLinksRemotesAndMCPToolsCanBeCalled(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  tool weather from mcp \"weather-mcp@1.0.0\"\n  link Helper\n  remote Bob at \"http://127.0.0.1:8080\"\n" +
		"  accepts go\n  on go\n    a = weather.forecast city: \"x\"\n    b = Helper.hi\n    c = Bob.ask city: \"x\"\n    reply c\n"
	res := checkSource(t, source)
	if len(res.Problems) != 0 {
		t.Errorf("unexpected problems: %v", res.Problems)
	}
}

// ---------- X2: the clauses of tools and think ----------

// req: X2, L7
func TestUsingMustNameDeclaredTools(t *testing.T) {
	text := firstProblem(t, "agent A\n  goal \"x\"\n  tool clock\n  accepts go\n  on go\n    reply think \"hi\" using clok\n")
	assertContains(t, text, "`using` names `clok`", "did you mean `clock`?")
}

// req: X2, L7
func TestReadOnlyRemovesTheActionsThatChangeThings(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  tool file \"data/\" readonly\n  accepts go\n  on go\n    file.write path: \"a\" text: \"b\"\n    reply \"x\"\n"
	text := firstProblem(t, source)
	assertContains(t, text, "`file.write` is not available", "readonly", "the actions it can use are: read")

	ok := "agent A\n  goal \"x\"\n  tool file \"data/\" readonly\n  accepts go\n  on go\n    t = file.read path: \"a\"\n    reply t\n"
	if res := checkSource(t, ok); len(res.Problems) != 0 {
		t.Errorf("reading must still work: %v", res.Problems)
	}

	post := "agent A\n  goal \"x\"\n  tool http readonly\n  accepts go\n  on go\n    http.post url: \"https://example.org\"\n    reply \"x\"\n"
	assertContains(t, firstProblem(t, post), "`http.post` is not available")
}

// req: X2, H2
func TestAllowedDomains(t *testing.T) {
	valid := "agent A\n  goal \"x\"\n  tool http allow \"api.example.com\" \"*.corp.example\"\n  accepts go\n  on go\n    reply \"x\"\n"
	if res := checkSource(t, valid); len(res.Problems) != 0 {
		t.Errorf("unexpected problems: %v", res.Problems)
	}
	for _, bad := range []string{"not a domain", "10.0.0.1", "com", "*", "*.com", "a*.example.com", "-bad.example.com", "http://example.com", "{city}.example.com"} {
		source := "agent A\n  goal \"x\"\n  tool http allow \"" + bad + "\"\n  accepts go\n  on go\n    reply \"x\"\n"
		res := checkSource(t, source)
		if len(res.Problems) != 1 {
			t.Errorf("%q: got %d problems, want 1", bad, len(res.Problems))
			continue
		}
		assertContains(t, located(t, res.Problems[0]), "is not a domain name that `allow` can use")
	}
}

// ---------- E1: what tool servers are given ----------

// req: E1
func TestSecretsCanNeverBePassedToAToolServer(t *testing.T) {
	for _, name := range HiddenEnvNames {
		source := "agent A\n  goal \"x\"\n  tool x from mcp \"cmd@1.0.0\" env \"" + name + "\"\n  accepts go\n  on go\n    reply \"x\"\n"
		assertContains(t, firstProblem(t, source), "can never be passed to a tool server")
	}
	source := "agent A\n  goal \"x\"\n  tool x from mcp \"cmd@1.0.0\" env \"1BAD\"\n  accepts go\n  on go\n    reply \"x\"\n"
	assertContains(t, firstProblem(t, source), "is not a valid environment variable name")

	fine := "agent A\n  goal \"x\"\n  tool x from mcp \"cmd@1.0.0\" env \"HTTPS_PROXY\" \"_OK1\"\n  accepts go\n  on go\n    reply \"x\"\n"
	if res := checkSource(t, fine); len(res.Problems) != 0 {
		t.Errorf("unexpected problems: %v", res.Problems)
	}
}

// req: X2
func TestAnEmptyToolServerCommandIsRefused(t *testing.T) {
	source := "agent A\n  goal \"x\"\n  tool x from mcp \"  \"\n  accepts go\n  on go\n    reply \"x\"\n"
	assertContains(t, firstProblem(t, source), "command of `x` is empty")
}

// ---------- warnings: E2 and L7 ----------

// req: E2
func TestUnpinnedPackageRunnersGetAWarning(t *testing.T) {
	res := checkSource(t, weatherSource)
	if len(res.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(res.Warnings), res.Warnings)
	}
	text := res.Warnings[0].Render()
	assertContains(t, text, "Warning on line 3", "does not pin a version of `weather-mcp`", "weather-mcp@1.2.3")

	for _, command := range []string{
		"npx -y weather-mcp@1.4.2",
		"npx -y @scope/server@2.0.0",
		"uvx mcp-server-fetch@1.0.0",
		"uvx mcp-server-fetch==1.0.0",
		"pipx run tool==3.1",
		"./local-server --port 9",
		"https://example.org/mcp",
	} {
		source := "agent A\n  goal \"x\"\n  tool t from mcp \"" + command + "\"\n  accepts go\n  on go\n    reply \"x\"\n"
		if res := checkSource(t, source); len(res.Warnings) != 0 {
			t.Errorf("%q should not warn: %v", command, res.Warnings)
		}
	}
	for _, command := range []string{"npx -y @scope/server", "uvx tool@latest", "bunx tool", "pipx run tool"} {
		source := "agent A\n  goal \"x\"\n  tool t from mcp \"" + command + "\"\n  accepts go\n  on go\n    reply \"x\"\n"
		if res := checkSource(t, source); len(res.Warnings) != 1 {
			t.Errorf("%q should warn once, got %v", command, res.Warnings)
		}
	}
}

// req: L7
func TestThinkWithoutUsingWarnsWhenToolsCanChangeThingsOrSendData(t *testing.T) {
	open := "agent A\n  goal \"x\"\n  tool file\n  accepts go\n  on go\n    reply think \"hi\"\n"
	res := checkSource(t, open)
	if len(res.Warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(res.Warnings), res.Warnings)
	}
	assertContains(t, res.Warnings[0].Render(), "every tool the agent declared", "(file)", "using")

	for name, source := range map[string]string{
		"using limits it":     "agent A\n  goal \"x\"\n  tool file\n  tool clock\n  accepts go\n  on go\n    reply think \"hi\" using clock\n",
		"readonly file":       "agent A\n  goal \"x\"\n  tool file readonly\n  accepts go\n  on go\n    reply think \"hi\"\n",
		"only harmless tools": "agent A\n  goal \"x\"\n  tool clock\n  accepts go\n  on go\n    reply think \"hi\"\n",
	} {
		if res := checkSource(t, source); len(res.Warnings) != 0 {
			t.Errorf("%s: unexpected warnings: %v", name, res.Warnings)
		}
	}

	network := "agent A\n  goal \"x\"\n  tool http readonly\n  accepts go\n  on go\n    reply think \"hi\"\n"
	if res := checkSource(t, network); len(res.Warnings) != 1 {
		t.Errorf("a readonly http tool still reaches the network and should warn, got %v", res.Warnings)
	}
}

// req: E2, L7
func TestWarningsNeverCountAsProblems(t *testing.T) {
	res := checkSource(t, weatherSource)
	if len(res.Problems) != 0 || len(res.Warnings) == 0 {
		t.Errorf("problems = %v, warnings = %v", res.Problems, res.Warnings)
	}
	if res.Warnings[0].Severity != diag.SeverityWarning {
		t.Error("the warning is not marked as a warning")
	}
}

func TestSplitCommand(t *testing.T) {
	tests := []struct {
		line string
		want []string
	}{
		{"npx -y weather-mcp", []string{"npx", "-y", "weather-mcp"}},
		{`prog "two words" 'and more' plain`, []string{"prog", "two words", "and more", "plain"}},
		{`prog ""`, []string{"prog", ""}},
		{"  spaced   out  ", []string{"spaced", "out"}},
		{"", nil},
	}
	for _, tt := range tests {
		got := SplitCommand(tt.line)
		if strings.Join(got, "\x00") != strings.Join(tt.want, "\x00") || len(got) != len(tt.want) {
			t.Errorf("SplitCommand(%q) = %q, want %q", tt.line, got, tt.want)
		}
	}
}

func TestClosestName(t *testing.T) {
	tests := []struct {
		word    string
		options []string
		want    string
	}{
		{"gaol", agentWords, "goal"},
		{"fille", builtinTools, "file"},
		{"zzzzzz", builtinTools, ""},
		{"a", []string{"b"}, ""}, // the distance must be smaller than the word
		{"reed", []string{"read", "write"}, "read"},
		{"cat", []string{"cut", "cot"}, "cut"}, // the first of equally close options wins
	}
	for _, tt := range tests {
		if got := ClosestName(tt.word, tt.options); got != tt.want {
			t.Errorf("ClosestName(%q) = %q, want %q", tt.word, got, tt.want)
		}
	}
}

func TestUsesThinkSeesAThinkAnywhereInAnAgent(t *testing.T) {
	parse := func(source string) *AgentDef {
		agents, err := ParseFile("a.ag", "", source)
		if err != nil {
			t.Fatalf("does not parse: %v", err)
		}
		return agents[0]
	}
	if UsesThink(parse("agent A\n  goal \"x\"\n  accepts go\n  on go\n    reply \"hi\"\n")) {
		t.Error("an agent that never thinks was said to think")
	}
	nested := "agent A\n  goal \"x\"\n  accepts go items\n  on go\n    for item in items\n      if item\n        answer = think \"about {item}\"\n    reply \"done\"\n"
	if !UsesThink(parse(nested)) {
		t.Error("a think inside a loop and an if was not seen")
	}
	atStart := "agent A\n  goal \"x\"\n  accepts go\n  on start\n    note = think \"warm up\"\n  on go\n    reply \"hi\"\n"
	if !UsesThink(parse(atStart)) {
		t.Error("a think in `on start` was not seen")
	}
}
