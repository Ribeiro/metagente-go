package lang

import (
	"strings"
	"testing"
)

// repeatAgent wraps lines of a handler in an agent that has the tools they use.
func repeatAgent(lines string) string {
	return "agent Pager\n  goal \"Read every page\"\n  tool http\n  tool state\n  accepts go start\n  on go\n" + lines
}

func firstRepeat(t *testing.T, source string) *RepeatStmt {
	t.Helper()
	agents := mustParse(t, source)
	for _, stmt := range agents[0].Handlers[0].Body {
		if r, ok := stmt.(*RepeatStmt); ok {
			return r
		}
	}
	t.Fatalf("no repeat in:\n%s", source)
	return nil
}

func TestRepeatWhileReadsItsConditionAndItsLines(t *testing.T) {
	r := firstRepeat(t, repeatAgent(
		"    url = start\n    repeat while url is not nothing\n      page = http.get url: url\n      url = page.json.next\n    reply \"done\"\n"))
	if r.HasLimit || r.Limit != 0 {
		t.Errorf("a limit that was not written: %+v", r)
	}
	if _, ok := r.Cond.(*CompareExpr); !ok {
		t.Errorf("condition = %T", r.Cond)
	}
	if len(r.Body) != 2 {
		t.Errorf("body has %d lines, want 2", len(r.Body))
	}
	if r.Line != 8 {
		t.Errorf("line = %d, want 8", r.Line)
	}
}

func TestRepeatWhileMayHaveALimitOfTimes(t *testing.T) {
	for text, want := range map[string]int{
		"up to 3 times":          3,
		"up to 1 time":           1,
		"up to 1000000000 times": 1000000000,
	} {
		r := firstRepeat(t, repeatAgent("    repeat while yes "+text+"\n      state.set key: \"k\" value: start\n"))
		if !r.HasLimit || r.Limit != want {
			t.Errorf("%s: limit = %d (written: %v), want %d", text, r.Limit, r.HasLimit, want)
		}
	}
	// The condition may end with a call or a name, and the clause is read after it.
	for _, cond := range []string{"state.get key: \"k\" up to 2 times", "start up to 2 times", "start is more than 1 up to 2 times"} {
		r := firstRepeat(t, repeatAgent("    repeat while "+cond+"\n      state.set key: \"k\" value: start\n"))
		if !r.HasLimit || r.Limit != 2 {
			t.Errorf("%s: limit = %d", cond, r.Limit)
		}
	}
}

func TestRepeatsMayBeNestedAndSitInsideOtherLines(t *testing.T) {
	r := firstRepeat(t, repeatAgent(
		"    repeat while start\n      for x in [1, 2]\n        repeat while x up to 2 times\n          state.set key: \"k\" value: x\n      if start\n        reply start\n"))
	if len(r.Body) != 2 {
		t.Fatalf("outer body has %d lines", len(r.Body))
	}
	inner, ok := r.Body[0].(*ForStmt).Body[0].(*RepeatStmt)
	if !ok || !inner.HasLimit || inner.Limit != 2 {
		t.Errorf("inner = %+v", r.Body[0])
	}
}

func TestRepeatIsAWordOnlyBeforeWhile(t *testing.T) {
	// A value, and a tool, may still be called `repeat`.
	source := "agent R\n  goal \"x\"\n  tool repeat from mcp \"npx -y some-mcp@1.0.0\"\n  accepts go\n  on go\n" +
		"    repeat = 3\n    again = repeat.run n: repeat\n    reply repeat\n"
	res := checkSource(t, source)
	for _, p := range res.Problems {
		if strings.Contains(p.Render(), "`repeat`") && !strings.Contains(p.Render(), "already the name of a tool") {
			t.Errorf("unexpected problem:\n%s", p.Render())
		}
	}
	// ... but `repeat` followed by something else is not understood, and says what a loop looks like.
	text := parseError(t, repeatAgent("    repeat now\n"))
	assertContains(t, text, "I do not understand the line starting with `repeat`", "repeat while condition")
}

func TestRepeatProblemsAreExplained(t *testing.T) {
	for name, c := range map[string]struct {
		lines string
		want  []string
	}{
		"no condition":    {"    repeat while\n      reply 1\n", []string{"the line ends but a value is missing"}},
		"nothing under":   {"    repeat while start\n    reply 1\n", []string{"this `repeat` has nothing under it", "indent the lines to repeat"}},
		"up without to":   {"    repeat while start up 3 times\n      reply 1\n", []string{"after `up` I expected `to`"}},
		"no number":       {"    repeat while start up to times\n      reply 1\n", []string{"`up to` needs a number of times"}},
		"zero":            {"    repeat while start up to 0 times\n      reply 1\n", []string{"a whole number of times, from 1 to 1000000000"}},
		"a fraction":      {"    repeat while start up to 2.5 times\n      reply 1\n", []string{"a whole number of times"}},
		"too many":        {"    repeat while start up to 1000000001 times\n      reply 1\n", []string{"a whole number of times"}},
		"no times":        {"    repeat while start up to 3\n      reply 1\n", []string{"after the number I expected `times`"}},
		"something after": {"    repeat while start up to 3 times now\n      reply 1\n", []string{"the line should have ended"}},
	} {
		text := parseError(t, repeatAgent(c.lines))
		for _, want := range c.want {
			if !strings.Contains(text, want) {
				t.Errorf("%s: missing %q in:\n%s", name, want, text)
			}
		}
	}
}

func TestTheChecksLookInsideARepeat(t *testing.T) {
	// A tool that was not declared, in the condition and in the lines.
	text := firstProblem(t, repeatAgent("    repeat while weather.more\n      reply 1\n"))
	assertContains(t, text, "line 7", "`weather`")
	text = firstProblem(t, repeatAgent("    repeat while start\n      weather.forecast city: start\n"))
	assertContains(t, text, "line 8", "does not know anything called `weather`")
	// A name that nothing gave a value to.
	text = firstProblem(t, repeatAgent("    repeat while nobody\n      reply 1\n"))
	assertContains(t, text, "nobody")
	// A name given a value inside the lines is known after them, as with `for`.
	res := checkSource(t, repeatAgent("    repeat while start up to 2 times\n      seen = start\n    reply seen\n"))
	if len(res.Problems) != 0 {
		t.Errorf("problems: %v", res.Problems)
	}
}

func TestTheCallsInsideARepeatAreCollected(t *testing.T) {
	agents := mustParse(t, repeatAgent("    repeat while state.get key: \"more\"\n      http.get url: start\n"))
	var targets []string
	for _, call := range CollectCalls(agents[0].Handlers[0].Body) {
		targets = append(targets, call.Target+"."+call.Action)
	}
	if strings.Join(targets, " ") != "state.get http.get" {
		t.Errorf("calls = %v", targets)
	}
}
