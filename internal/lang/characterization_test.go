package lang

import (
	"crypto/sha256"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"metagente/internal/diag"
)

// The characterization test records what the lexer, the parser and the checks do with a large set of
// programs, good and bad, and compares it with what they did when the record was made.
//
// It exists to make it safe to change how those parts are written. It does not say that the record
// is right, only that it did not change: for every program it keeps the tokens, the tree, and every
// message with its line, its column and its advice. A change that is meant to alter one of them
// shows up as a difference, and the record is then rewritten on purpose.
//
//	make characterize          rewrites the record from what the code does now
//	make characterize-cover    lists the blocks of code that the programs never reach
//
// The programs are in characterization_corpus_test.go, and the record is
// testdata/characterization.golden.

var updateCharacterization = flag.Bool("update-characterization", false,
	"rewrite testdata/characterization.golden from what the code does now")

const characterizationFile = "testdata/characterization.golden"

// charCase is a program and the name that it has in the record.
type charCase struct {
	name   string
	source string
}

func TestCharacterization(t *testing.T) {
	cases := charCorpus(t)
	var out strings.Builder
	for _, c := range cases {
		out.WriteString(charDescribe(c))
	}
	got := out.String()

	if *updateCharacterization {
		if err := os.MkdirAll(filepath.Dir(characterizationFile), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(characterizationFile, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s: %d cases, %d bytes", characterizationFile, len(cases), len(got))
		return
	}

	want, err := os.ReadFile(characterizationFile)
	if err != nil {
		t.Fatalf("there is no record of what the code does (%v).\nMake it, with the code as it is now:\n  make characterize", err)
	}
	if string(want) != got {
		charReport(t, string(want), got)
	}
}

// charDescribe is everything the code does with one program, in the order that it does it.
func charDescribe(c charCase) string {
	const file = "case.ag"
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s ===\n", c.name)

	b.WriteString("-- source --\n")
	b.WriteString(charShort(charSource(c.source)))

	b.WriteString("-- lex --\n")
	b.WriteString(charShort(charSafely(func() string { return charLex(file, c.source) })))

	b.WriteString("-- parse --\n")
	var agents []*AgentDef
	parsed := charSafely(func() string {
		var err error
		agents, err = ParseFile(file, file, c.source)
		if err != nil {
			agents = nil
			return "error:\n" + charError(err)
		}
		return charAgents(agents)
	})
	b.WriteString(charShort(parsed))

	if agents != nil {
		b.WriteString("-- check --\n")
		b.WriteString(charShort(charSafely(func() string { return charResult(Check(agents)) })))
	}
	return b.String()
}

// charSafely runs one stage. A panic is part of what the code does, so it is recorded and not raised.
func charSafely(stage func() string) (text string) {
	defer func() {
		if r := recover(); r != nil {
			text = fmt.Sprintf("PANIC: %v\n", r)
		}
	}()
	return stage()
}

// charSource shows the program one line at a time, quoted, so a tab, a carriage return or a
// character that is not printable can be seen, and so the record does not depend on how git
// ends the lines of a file.
func charSource(source string) string {
	lines := strings.Split(source, "\n")
	if n := len(lines); n > 1 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%d %q\n", i+1, line)
	}
	return b.String()
}

// charLex is the tokens of every line, or the error.
func charLex(file, source string) string {
	lines, err := lex(file, source)
	if err != nil {
		return "error:\n" + charError(err)
	}
	if len(lines) == 0 {
		return "no lines\n"
	}
	var b strings.Builder
	for _, line := range lines {
		fmt.Fprintf(&b, "L%d indent=%d", line.Number, line.Indent)
		for _, tok := range line.Tokens {
			b.WriteString(" " + charToken(tok))
		}
		if line.Comment != "" {
			fmt.Fprintf(&b, " comment=%q", line.Comment)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func charToken(t Token) string {
	switch t.Kind {
	case TokWord:
		return fmt.Sprintf("word(%s)@%d", t.Word, t.Col)
	case TokNumber:
		return fmt.Sprintf("number(%s)@%d", strconv.FormatFloat(t.Number, 'g', -1, 64), t.Col)
	case TokString:
		var parts []string
		for _, p := range t.Parts {
			if p.IsVar {
				parts = append(parts, "{"+strings.Join(p.Var, ".")+"}")
			} else {
				parts = append(parts, strconv.Quote(p.Lit))
			}
		}
		return fmt.Sprintf("string[%s]@%d", strings.Join(parts, " "), t.Col)
	case TokSym:
		return fmt.Sprintf("sym(%c)@%d", t.Sym, t.Col)
	}
	return fmt.Sprintf("unknown-kind(%d)@%d", t.Kind, t.Col)
}

// charError is an error the way a person would read it.
func charError(err error) string {
	if d, ok := diag.From(err); ok {
		return d.Render()
	}
	return "not a diagnostic: " + err.Error()
}

// charAgents is the tree of every agent.
func charAgents(agents []*AgentDef) string {
	if len(agents) == 0 {
		return "no agents\n"
	}
	var b strings.Builder
	for i, agent := range agents {
		fmt.Fprintf(&b, "agent #%d ", i)
		charDump(&b, reflect.ValueOf(agent), 0)
		b.WriteString("\n")
	}
	return b.String()
}

// charResult is what Check found: each problem and each warning, as a person would read it.
func charResult(r Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "problems: %d\n", len(r.Problems))
	for _, d := range r.Problems {
		b.WriteString(strings.TrimRight(d.Render(), "\n") + "\n")
	}
	fmt.Fprintf(&b, "warnings: %d\n", len(r.Warnings))
	for _, d := range r.Warnings {
		b.WriteString(strings.TrimRight(d.Render(), "\n") + "\n")
	}
	return b.String()
}

// charShort keeps the record readable when a program makes a very long text: the start, the end,
// and the hash of everything, so a change in the part that was left out is still seen.
func charShort(text string) string {
	text = strings.TrimRight(text, "\n") + "\n"
	const head, tail, slack = 2000, 800, 400
	if len(text) <= head+tail+slack {
		return text
	}
	sum := sha256.Sum256([]byte(text))
	from := head
	for from > 0 && !utf8.RuneStart(text[from]) {
		from--
	}
	to := len(text) - tail
	for to < len(text) && !utf8.RuneStart(text[to]) {
		to++
	}
	return text[:from] + fmt.Sprintf("...(%d bytes left out; sha256 %x)...\n", to-from, sum) + text[to:]
}

// ---------- the tree, written down ----------

// charDump writes a value of the tree. What fits in a line is written in one line; the rest is
// written one field, or one item, to a line. The type of every node is written, the nil lists are
// told from the empty ones, and the places are written as @line:column.
func charDump(b *strings.Builder, v reflect.Value, depth int) {
	switch v.Kind() {
	case reflect.Pointer:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		if v.Type() == reflect.TypeOf((*SourceFile)(nil)) {
			charFormat(b, v)
			return
		}
		b.WriteString("&")
		charDump(b, v.Elem(), depth)
		return
	case reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		charDump(b, v.Elem(), depth)
		return
	}

	var one strings.Builder
	charFormat(&one, v)
	if one.Len() <= 100 {
		b.WriteString(one.String())
		return
	}
	switch v.Kind() {
	case reflect.Struct:
		t := v.Type()
		b.WriteString(t.Name() + "{\n")
		for i := 0; i < v.NumField(); i++ {
			charIndent(b, depth+1)
			b.WriteString(t.Field(i).Name + ": ")
			charDump(b, v.Field(i), depth+1)
			b.WriteString("\n")
		}
		charIndent(b, depth)
		b.WriteString("}")
	case reflect.Slice:
		b.WriteString("[\n")
		for i := 0; i < v.Len(); i++ {
			charIndent(b, depth+1)
			charDump(b, v.Index(i), depth+1)
			b.WriteString("\n")
		}
		charIndent(b, depth)
		b.WriteString("]")
	default:
		b.WriteString(one.String())
	}
}

func charIndent(b *strings.Builder, depth int) {
	for i := 0; i < depth; i++ {
		b.WriteString("  ")
	}
}

// charFormat writes a value in one line.
func charFormat(b *strings.Builder, v reflect.Value) {
	switch v.Kind() {
	case reflect.Pointer:
		charFormatPointer(b, v)
	case reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
			return
		}
		charFormat(b, v.Elem())
	case reflect.Struct:
		charFormatStruct(b, v)
	case reflect.Slice:
		charFormatSlice(b, v)
	case reflect.String:
		b.WriteString(strconv.Quote(v.String()))
	case reflect.Bool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Float32, reflect.Float64:
		b.WriteString(strconv.FormatFloat(v.Float(), 'g', -1, 64))
	default:
		fmt.Fprintf(b, "<%s>", v.Kind())
	}
}

func charFormatPointer(b *strings.Builder, v reflect.Value) {
	switch {
	case v.IsNil():
		b.WriteString("nil")
	case v.Type() == reflect.TypeOf((*SourceFile)(nil)):
		f := v.Elem()
		fmt.Fprintf(b, "source(%q, %q)", f.FieldByName("Name").String(), f.FieldByName("Path").String())
	default:
		b.WriteString("&")
		charFormat(b, v.Elem())
	}
}

func charFormatStruct(b *strings.Builder, v reflect.Value) {
	t := v.Type()
	if t == reflect.TypeOf(Span{}) {
		fmt.Fprintf(b, "@%d:%d", v.Field(0).Int(), v.Field(1).Int())
		return
	}
	b.WriteString(t.Name() + "{")
	for i := 0; i < v.NumField(); i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(t.Field(i).Name + ": ")
		charFormat(b, v.Field(i))
	}
	b.WriteString("}")
}

func charFormatSlice(b *strings.Builder, v reflect.Value) {
	if v.IsNil() {
		b.WriteString("nil")
		return
	}
	b.WriteString("[")
	for i := 0; i < v.Len(); i++ {
		if i > 0 {
			b.WriteString(", ")
		}
		charFormat(b, v.Index(i))
	}
	b.WriteString("]")
}

// ---------- what is said when the record and the code disagree ----------

// charSplit cuts the record into its cases.
func charSplit(all string) ([]string, map[string]string) {
	var names []string
	texts := map[string]string{}
	current := ""
	var body strings.Builder
	flush := func() {
		if current != "" {
			texts[current] = body.String()
		}
		body.Reset()
	}
	for _, line := range strings.SplitAfter(all, "\n") {
		bare := strings.TrimRight(line, "\n")
		if strings.HasPrefix(bare, "=== ") && strings.HasSuffix(bare, " ===") {
			flush()
			current = strings.TrimSuffix(strings.TrimPrefix(bare, "=== "), " ===")
			names = append(names, current)
		}
		body.WriteString(line)
	}
	flush()
	return names, texts
}

func charReport(t *testing.T, want, got string) {
	t.Helper()
	wantNames, wantText := charSplit(want)
	gotNames, gotText := charSplit(got)
	var problems []string
	for _, name := range gotNames {
		old, known := wantText[name]
		switch {
		case !known:
			problems = append(problems, "not in the record: "+name)
		case old != gotText[name]:
			problems = append(problems, "different: "+name+"\n"+charFirstDifference(old, gotText[name]))
		}
	}
	for _, name := range wantNames {
		if _, still := gotText[name]; !still {
			problems = append(problems, "in the record but not run any more: "+name)
		}
	}
	if len(problems) == 0 {
		t.Fatal("the record and what the code does differ, but no single case is different (were cases moved?)")
	}
	const most = 6
	shown := problems
	if len(shown) > most {
		shown = shown[:most]
	}
	t.Errorf("what the code does is not what the record says: %d of %d cases differ.\n\n%s\n\nIf this change of behavior is intended, rewrite the record with: make characterize",
		len(problems), len(gotNames), strings.Join(shown, "\n\n"))
}

// charFirstDifference shows where two texts first differ, with the two lines before.
func charFirstDifference(old, now string) string {
	a, b := strings.Split(old, "\n"), strings.Split(now, "\n")
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	var out strings.Builder
	for k := max(0, i-2); k < i; k++ {
		fmt.Fprintf(&out, "      %s\n", charCut(a[k]))
	}
	if i < len(a) {
		fmt.Fprintf(&out, "  record: %s\n", charCut(a[i]))
	} else {
		out.WriteString("  record: (ends here)\n")
	}
	if i < len(b) {
		fmt.Fprintf(&out, "  now:    %s", charCut(b[i]))
	} else {
		out.WriteString("  now:    (ends here)")
	}
	return out.String()
}

func charCut(line string) string {
	r := []rune(line)
	if len(r) > 160 {
		return string(r[:160]) + "..."
	}
	return line
}
