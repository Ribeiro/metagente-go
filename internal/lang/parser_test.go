package lang

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

const weatherSource = `agent Weather
  goal "Answer questions about the weather"
  tool weather from mcp "npx -y weather-mcp"
  accepts ask city
  on ask
    forecast = weather.forecast city: city
    reply "In {city} it will be {forecast.summary}"
`

func mustParse(t *testing.T, text string) []*AgentDef {
	t.Helper()
	agents, err := ParseFile("test.ag", "", text)
	if err != nil {
		t.Fatalf("unexpected problem:\n%s", errorText(t, err))
	}
	return agents
}

func parseError(t *testing.T, text string) string {
	t.Helper()
	_, err := ParseFile("test.ag", "", text)
	if err == nil {
		t.Fatalf("expected a problem for:\n%s", text)
	}
	return errorText(t, err)
}

func TestParsesTheReferenceAgent(t *testing.T) {
	agents := mustParse(t, weatherSource)
	if len(agents) != 1 {
		t.Fatalf("got %d agents, want 1", len(agents))
	}
	a := agents[0]
	if a.Name != "Weather" {
		t.Errorf("name = %q", a.Name)
	}
	if a.Goal == nil || a.Goal.Text != "Answer questions about the weather" {
		t.Errorf("goal = %+v", a.Goal)
	}
	if len(a.Tools) != 1 || a.Tools[0].Kind != ToolMCP || a.Tools[0].Command != "npx -y weather-mcp" {
		t.Errorf("tools = %+v", a.Tools)
	}
	if len(a.Accepts) != 1 || a.Accepts[0].Message != "ask" || !reflect.DeepEqual(a.Accepts[0].Params, []string{"city"}) {
		t.Errorf("accepts = %+v", a.Accepts)
	}
	handler := a.FindHandler("ask")
	if handler == nil || len(handler.Body) != 2 {
		t.Fatalf("handler = %+v", handler)
	}
	assign, ok := handler.Body[0].(*AssignStmt)
	if !ok || assign.Name != "forecast" {
		t.Fatalf("first statement = %#v", handler.Body[0])
	}
	call, ok := assign.Value.(*CallExpr)
	if !ok || call.Target != "weather" || call.Action != "forecast" || len(call.Args) != 1 {
		t.Fatalf("call = %#v", assign.Value)
	}
	if _, ok := handler.Body[1].(*ReplyStmt); !ok {
		t.Errorf("second statement = %#v", handler.Body[1])
	}
}

func TestParsesSeveralAgentsAndAllDeclarations(t *testing.T) {
	text := `agent A
  goal "one"
  tool file "data/"
  tool http
  tool state
  tool clock
  tool env "HOME" "USER"
  link B
  link C from "other/c.ag"
  remote Bob at "https://example.org"
  accepts go a b  # do things
  on start
    reply "hi"
  on go
    if a is b and not a is 3
      reply "same"
    otherwise
      for x in [1, 2, 3]
        file.write path: "x" text: x within 5 seconds
      reply think "what now"
agent B
  goal "two"
`
	agents := mustParse(t, text)
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}
	a := agents[0]
	if len(a.Tools) != 5 || len(a.Links) != 2 || len(a.Remotes) != 1 {
		t.Fatalf("declarations = %d tools, %d links, %d remotes", len(a.Tools), len(a.Links), len(a.Remotes))
	}
	if !a.Links[1].HasPath || a.Links[1].Path != "other/c.ag" {
		t.Errorf("link = %+v", a.Links[1])
	}
	if a.Remotes[0].URL != "https://example.org" {
		t.Errorf("remote = %+v", a.Remotes[0])
	}
	if a.Accepts[0].Description != "do things" {
		t.Errorf("description = %q", a.Accepts[0].Description)
	}
	if a.Start == nil {
		t.Error("the `on start` handler was not kept apart")
	}
	if len(a.Handlers) != 1 {
		t.Errorf("got %d handlers, want 1 (`on start` is not one of them)", len(a.Handlers))
	}
	if !reflect.DeepEqual(a.Tools[4].EnvNames, []string{"HOME", "USER"}) {
		t.Errorf("env names = %v", a.Tools[4].EnvNames)
	}
	if !a.Tools[0].HasScope || a.Tools[0].Scope != "data/" {
		t.Errorf("file scope = %+v", a.Tools[0])
	}
}

func TestHyphenatedNamesAndDottedActionsCanCallRealMCPTools(t *testing.T) {
	text := "agent A\n  goal \"x\"\n  tool gh from mcp \"gh-mcp\"\n  accepts go\n  on go\n" +
		"    a = gh.create-issue title: \"t\"\n" +
		"    b = gh.Weather.ask city: \"x\" within 5 seconds\n" +
		"    reply b\n"
	body := mustParse(t, text)[0].FindHandler("go").Body
	first := body[0].(*AssignStmt).Value.(*CallExpr)
	second := body[1].(*AssignStmt).Value.(*CallExpr)
	if first.Action != "create-issue" {
		t.Errorf("action = %q", first.Action)
	}
	if second.Target != "gh" || second.Action != "Weather.ask" || !second.HasWithin || second.Within != 5 {
		t.Errorf("call = %+v", second)
	}
}

func TestSyntaxErrorsExplainAndSuggest(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{"bare tool env", "agent A\n  goal \"x\"\n  tool env\n",
			[]string{"Problem on line 3 of test.ag", `tool env "HOME"`}},
		{"unknown word suggests the closest keyword", "agent A\n  gaol \"x\"\n",
			[]string{"`gaol`", "did you mean `goal`?"}},
		{"unknown built in tool lists the real ones", "agent A\n  tool fille\n",
			[]string{"did you mean `tool file`?"}},
		{"unknown tool far from any built in one", "agent A\n  tool zzzzzz\n",
			[]string{"built in tools are: file, http, env, state, clock"}},
		{"unterminated text is reported on its line", "agent A\n  goal \"never ends\n",
			[]string{"line 2", "never ends", "closing quote"}},
		{"odd indentation", "agent A\n   goal \"x\"\n", []string{"3 spaces"}},
		{"tab indentation", "agent A\n\tgoal \"x\"\n", []string{"tab"}},
		{"file must start with agent", "goal \"x\"\n", []string{"agent"}},
		{"empty file", "\n\n", []string{"no agent"}},
		{"empty handler", "agent A\n  goal \"x\"\n  on go\n", []string{"nothing under it"}},
		{"stray otherwise", "agent A\n  on go\n    otherwise\n      reply \"x\"\n",
			[]string{"`otherwise` must come right after an `if`"}},
		{"extra tokens", "agent A\n  goal \"x\" \"y\"\n", []string{"did not expect"}},
		{"agent inside an agent", "agent A\n  agent B\n", []string{"cannot be defined inside another agent"}},
		{"missing value", "agent A\n  on go\n    x = \n", []string{"a value is missing"}},
		{"from without mcp", "agent A\n  tool a from b\n", []string{"I expected `mcp`"}},
		{"call without a dot inside an expression", "agent A\n  on go\n    x = weather city: \"x\"\n",
			[]string{"a call has the form target.action"}},
		{"line that starts with an unknown word", "agent A\n  on go\n    weather city: \"x\"\n",
			[]string{"I do not understand the line starting with `weather`"}},
		{"misspelled statement word", "agent A\n  on go\n    repli \"x\"\n",
			[]string{"did you mean `reply`?"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text := parseError(t, tt.source)
			for _, want := range tt.want {
				if !strings.Contains(text, want) {
					t.Errorf("missing %q in:\n%s", want, text)
				}
			}
		})
	}
}

// forbiddenInErrors are words that would show the inner workings of the
// interpreter. Errors speak about the agent only.
var forbiddenInErrors = []string{
	"goroutine", "panic:", ".go:", "runtime.", "nil pointer", "interface {", "*lang.", "diag.",
}

func assertPlain(t *testing.T, text string) {
	t.Helper()
	for _, word := range forbiddenInErrors {
		if strings.Contains(text, word) {
			t.Errorf("found `%s` in:\n%s", word, text)
		}
	}
}

func TestErrorsNeverShowTheInnerWorkings(t *testing.T) {
	for _, bad := range []string{
		"agent\n",
		"agent A\n  on go\n    x = \n",
		"agent A\n  tool a from b\n",
		"agent A\n  on go\n    ???\n",
	} {
		assertPlain(t, parseError(t, bad))
	}
}

func TestErrorsShowTheOffendingLineWithAMarker(t *testing.T) {
	text := parseError(t, "agent A\n  gaol \"x\"\n")
	if !strings.Contains(text, "2 |   gaol \"x\"") {
		t.Errorf("the offending line is missing in:\n%s", text)
	}
	if !strings.Contains(text, "^") {
		t.Errorf("the marker is missing in:\n%s", text)
	}
	if strings.Contains(text, "╭") {
		t.Errorf("box drawing characters in:\n%s", text)
	}
}

// req: H2, L7, E1
func TestToolClauses(t *testing.T) {
	text := `agent A
  goal "x"
  tool file "data/" readonly
  tool http allow "api.example.com" "*.corp.example" readonly
  tool fetch from mcp "npx -y pkg@1.0.0" env "HTTPS_PROXY" "NO_PROXY"
agent B
  goal "y"
  tool http allow private
`
	agents := mustParse(t, text)
	tools := agents[0].Tools
	if !tools[0].ReadOnly || tools[0].Scope != "data/" {
		t.Errorf("file tool = %+v", tools[0])
	}
	if !tools[1].ReadOnly || !reflect.DeepEqual(tools[1].Allow, []string{"api.example.com", "*.corp.example"}) {
		t.Errorf("http tool = %+v", tools[1])
	}
	if !reflect.DeepEqual(tools[2].MCPEnv, []string{"HTTPS_PROXY", "NO_PROXY"}) || tools[2].Command != "npx -y pkg@1.0.0" {
		t.Errorf("mcp tool = %+v", tools[2])
	}
	if !agents[1].Tools[0].AllowPrivate {
		t.Errorf("allow private was not read: %+v", agents[1].Tools[0])
	}
}

// req: H2, L7, E1
func TestToolClauseErrors(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"allow without values", "agent A\n  tool http allow\n", "`allow` needs `private`"},
		{"env without values", "agent A\n  tool x from mcp \"cmd\" env\n", "`env` needs the names"},
		{"readonly on state", "agent A\n  tool state readonly\n", "did not expect `readonly`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if text := parseError(t, tt.source); !strings.Contains(text, tt.want) {
				t.Errorf("missing %q in:\n%s", tt.want, text)
			}
		})
	}
}

// req: L7
func TestThinkUsing(t *testing.T) {
	text := "agent A\n  goal \"x\"\n  tool clock\n  accepts go\n  on go\n    reply think \"hi\" using clock fetch\n"
	body := mustParse(t, text)[0].FindHandler("go").Body
	think, ok := body[0].(*ReplyStmt).Value.(*ThinkExpr)
	if !ok {
		t.Fatalf("reply value = %#v", body[0].(*ReplyStmt).Value)
	}
	if !reflect.DeepEqual(think.Using, []string{"clock", "fetch"}) {
		t.Errorf("using = %v", think.Using)
	}

	// `using` stops at the words that continue an expression.
	text = "agent A\n  goal \"x\"\n  accepts go\n  on go\n    reply think \"hi\" using clock and yes\n"
	think = mustParse(t, text)[0].FindHandler("go").Body[0].(*ReplyStmt).Value.(*AndExpr).Left.(*ThinkExpr)
	if !reflect.DeepEqual(think.Using, []string{"clock"}) {
		t.Errorf("using = %v", think.Using)
	}

	text = parseError(t, "agent A\n  accepts go\n  on go\n    reply think \"hi\" using\n")
	if !strings.Contains(text, "`using` needs the names of the tools") {
		t.Errorf("unexpected problem:\n%s", text)
	}
}

// req: D4
func TestParserLimits(t *testing.T) {
	// A list over the limit is refused; one at the limit is accepted.
	atLimit := strings.TrimSuffix(strings.Repeat("1, ", MaxListItems), ", ")
	if _, err := ParseFile("t.ag", "", "agent A\n  accepts go\n  on go\n    reply ["+atLimit+"]\n"); err != nil {
		t.Errorf("a list of %d items should be accepted: %v", MaxListItems, err)
	}
	over := strings.Repeat("1, ", MaxListItems+1)
	_, err := ParseFile("t.ag", "", "agent A\n  accepts go\n  on go\n    reply ["+over+"1]\n")
	if err == nil || !strings.Contains(errorText(t, err), "more than "+strconv.Itoa(MaxListItems)+" items") {
		t.Errorf("a list over the limit should be refused, got %v", err)
	}

	big := "agent A\n  goal \"" + strings.Repeat("x", MaxSourceBytes) + "\"\n"
	_, err = ParseFile("t.ag", "", big)
	if err == nil || !strings.Contains(errorText(t, err), "larger than 1 MiB") {
		t.Errorf("a file over the limit should be refused, got %v", err)
	}
}
