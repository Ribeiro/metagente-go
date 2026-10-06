package lang

import (
	"reflect"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

// errorText renders an error the way a person would read it.
func errorText(t *testing.T, err error) string {
	t.Helper()
	d, ok := diag.From(err)
	if !ok {
		t.Fatalf("the error is not a diagnostic: %v", err)
	}
	return d.Render()
}

func TestLexSplitsTextIntoLiteralsAndHoles(t *testing.T) {
	lines, err := lex("t.ag", "reply \"hi {name}, {forecast.summary}!\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || len(lines[0].Tokens) != 2 {
		t.Fatalf("unexpected lines: %+v", lines)
	}
	want := []TextPart{
		{Lit: "hi "},
		{Var: []string{"name"}, IsVar: true},
		{Lit: ", "},
		{Var: []string{"forecast", "summary"}, IsVar: true},
		{Lit: "!"},
	}
	if got := lines[0].Tokens[1].Parts; !reflect.DeepEqual(got, want) {
		t.Errorf("parts = %#v, want %#v", got, want)
	}
}

func TestLexEscapes(t *testing.T) {
	lines, err := lex("t.ag", `reply "line\none\t\"quoted\" \{literal}"`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	parts := lines[0].Tokens[1].Parts
	if len(parts) != 1 || parts[0].Lit != "line\none\t\"quoted\" {literal}" {
		t.Errorf("parts = %#v", parts)
	}
}

func TestLexIndentationLevelsAndLineNumbers(t *testing.T) {
	lines, err := lex("t.ag", "agent A\n  goal \"x\"\n    reply \"y\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, want := range []int{0, 1, 2} {
		if lines[i].Indent != want || lines[i].Number != i+1 {
			t.Errorf("line %d: indent %d number %d", i, lines[i].Indent, lines[i].Number)
		}
	}
}

func TestLexDropsBlankAndCommentLinesAndKeepsTrailingComments(t *testing.T) {
	lines, err := lex("t.ag", "agent A  # trailing\n\n# a whole line\n  goal \"x\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Number != 1 || lines[1].Number != 4 {
		t.Fatalf("unexpected lines: %+v", lines)
	}
	if lines[0].Comment != "trailing" {
		t.Errorf("comment = %q, want %q", lines[0].Comment, "trailing")
	}
}

func TestLexAcceptsWindowsLineEndings(t *testing.T) {
	lines, err := lex("t.ag", "agent A\r\n  goal \"x\"\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Errorf("got %d lines, want 2", len(lines))
	}
}

func TestLexAcceptsAccentedNames(t *testing.T) {
	lines, err := lex("t.ag", "reply cidade_São-1\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := lines[0].Tokens[1].Word; got != "cidade_São-1" {
		t.Errorf("word = %q", got)
	}
}

func TestLexNumbers(t *testing.T) {
	lines, err := lex("t.ag", "reply 2.5 7\n")
	if err != nil {
		t.Fatal(err)
	}
	tokens := lines[0].Tokens
	if len(tokens) != 3 || tokens[1].Number != 2.5 || tokens[2].Number != 7 {
		t.Errorf("tokens = %+v", tokens)
	}

	// A dot belongs to the number only when a digit follows it.
	lines, err = lex("t.ag", "reply 5.\n")
	if err != nil {
		t.Fatal(err)
	}
	tokens = lines[0].Tokens
	if len(tokens) != 3 || tokens[1].Kind != TokNumber || tokens[2].Kind != TokSym || tokens[2].Sym != '.' {
		t.Errorf("tokens = %+v", tokens)
	}
}

// req: D1 (the digits case)
func TestLexErrorsSayWhatIsWrongAndHowToFixIt(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{"tab", "\tgoal \"x\"\n", []string{"indented with a tab", "two spaces"}},
		{"odd indentation", "   goal \"x\"\n", []string{"3 spaces", "exactly 2"}},
		{"unterminated text", "goal \"never ends\n", []string{"never ends", "closing quote"}},
		{"unclosed hole", "reply \"hello {name\"\n", []string{"never closed"}},
		{"empty hole", "reply \"hello {}\"\n", []string{"not a name I can fill in"}},
		{"hole with a space", "reply \"hello {a b}\"\n", []string{"not a name I can fill in"}},
		{"trailing backslash", "reply \"x\\\n", []string{"ends with a backslash"}},
		{"strange character", "reply ?\n", []string{"do not understand the character `?`"}},
		{"unreadable number", "reply 1.2.3\n", []string{"`1.2.3` is not a number I can read"}},
		{"number with too many digits", "reply 1234567890123456\n", []string{"too many digits", "at most 15"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertLexProblem(t, tt.source, tt.want)
		})
	}
}

// assertLexProblem lexes a source that is wrong. It wants a problem that is placed on line 1, that
// says how to fix it, and that says each of the words in want.
func assertLexProblem(t *testing.T, source string, want []string) {
	t.Helper()
	_, err := lex("t.ag", source)
	if err == nil {
		t.Fatal("expected a problem")
	}
	text := errorText(t, err)
	if !strings.HasPrefix(text, "Problem on line 1 of t.ag") {
		t.Errorf("no place in:\n%s", text)
	}
	if !strings.Contains(text, "Fix: ") {
		t.Errorf("no fix in:\n%s", text)
	}
	for _, w := range want {
		if !strings.Contains(text, w) {
			t.Errorf("missing %q in:\n%s", w, text)
		}
	}
}

// req: D4
func TestLexLimits(t *testing.T) {
	long := "reply \"" + strings.Repeat("a", MaxLineBytes) + "\"\n"
	if _, err := lex("t.ag", long); err == nil || !strings.Contains(errorText(t, err), "longer than 64 KiB") {
		t.Errorf("a line over the limit should be refused, got %v", err)
	}

	deepest := strings.Repeat("  ", MaxIndentLevels) + "reply \"x\"\n"
	if _, err := lex("t.ag", deepest); err != nil {
		t.Errorf("%d levels should be accepted: %v", MaxIndentLevels, err)
	}
	tooDeep := strings.Repeat("  ", MaxIndentLevels+1) + "reply \"x\"\n"
	if _, err := lex("t.ag", tooDeep); err == nil || !strings.Contains(errorText(t, err), "nested deeper than 32 levels") {
		t.Errorf("%d levels should be refused, got %v", MaxIndentLevels+1, err)
	}
}
