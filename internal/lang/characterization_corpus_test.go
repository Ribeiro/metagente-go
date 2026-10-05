package lang

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The programs of the characterization test. Most are small and wrong in one way, because the
// messages are what a person reads. They are grouped by the part of the code that they were written
// to reach, but nothing here says what the right answer is: the record keeps what the code does.
//
// To add a program, add it at the end of its group (the names come from the programs, so the
// others do not change) and run `make characterize`.

type charAdd func(name, source string)

// charLines joins lines into a source text that ends with a line break.
func charLines(lines ...string) string {
	return strings.Join(lines, "\n") + "\n"
}

// charAgent is a valid agent with the given declarations and, under `on go`, the given statements.
// The statements may carry their own indentation, relative to the first level of statements.
// The message `go` takes a value called x.
func charAgent(decls []string, stmts ...string) string {
	lines := []string{"agent A", `  goal "g"`}
	for _, d := range decls {
		lines = append(lines, "  "+d)
	}
	lines = append(lines, "  accepts go x", "  on go")
	for _, s := range stmts {
		lines = append(lines, "    "+s)
	}
	return charLines(lines...)
}

func charStmts(stmts ...string) string { return charAgent(nil, stmts...) }

func charExpr(e string) string { return charStmts("reply " + e) }

// charDecl is an agent with a goal and one more line, at the level of the declarations.
func charDecl(d string) string { return charLines("agent A", `  goal "g"`, "  "+d) }

// charSlug turns a text into a name that can be read in the record.
func charSlug(text string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(text) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.TrimRight(b.String(), "-")
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
	}
	if s == "" {
		s = "empty"
	}
	return s
}

func charCorpus(t *testing.T) []charCase {
	t.Helper()
	var cases []charCase
	used := map[string]int{}
	add := func(name, source string) {
		used[name]++
		if n := used[name]; n > 1 {
			name = fmt.Sprintf("%s~%d", name, n)
		}
		cases = append(cases, charCase{name: name, source: source})
	}
	charLexCases(add)
	charFileCases(add)
	charDeclCases(add)
	charToolCases(add)
	charStatementCases(add)
	charExprCases(add)
	charCheckCases(add)
	charProgramCases(add)
	for _, file := range exampleFiles(t) {
		add("example/"+filepath.Base(file), readExample(t, file))
	}
	return cases
}

// ---------- the lexer: lines, indentation, numbers, names, symbols, comments, texts ----------

func charLexCases(add charAdd) {
	add("lex/empty", "")
	add("lex/only-blank-lines", "   \n\n  \n")
	add("lex/only-comments", "# one\n  # two\n")
	add("lex/minimal-agent", charLines("agent A", `  goal "g"`))
	add("lex/windows-line-endings", "agent A\r\n  goal \"g\"\r\n  accepts go\r\n  on go\r\n    reply \"hi\"\r\n")
	add("lex/no-final-line-break", "agent A\n  goal \"g\"")
	add("lex/blank-lines-between", "agent A\n\n  goal \"g\"\n   \n  accepts go\n\n  on go\n\n    reply \"x\"\n")

	// indentation
	add("lex/tab-at-the-start", "\tagent A\n")
	add("lex/tab-after-spaces", "agent A\n  \tgoal \"g\"\n")
	add("lex/tab-inside-the-line", "agent\tA\n")
	add("lex/line-with-only-a-tab", "agent A\n\t\n  goal \"g\"\n")
	add("lex/one-space-of-indent", "agent A\n goal \"g\"\n")
	add("lex/three-spaces-of-indent", "agent A\n   goal \"g\"\n")
	add("lex/odd-indent-in-a-comment", "agent A\n   # not checked\n  goal \"g\"\n")
	add("lex/32-levels", "agent A\n"+strings.Repeat(" ", 64)+"reply \"x\"\n")
	add("lex/33-levels", "agent A\n"+strings.Repeat(" ", 66)+"reply \"x\"\n")

	// the size of a line
	add("lex/line-of-exactly-64KiB", "#"+strings.Repeat("x", MaxLineBytes-1)+"\n")
	add("lex/line-of-64KiB-plus-one", "#"+strings.Repeat("x", MaxLineBytes)+"\n")
	add("lex/long-line-after-code", "agent A\n  goal \""+strings.Repeat("x", MaxLineBytes)+"\"\n")
	add("lex/word-of-60000-letters", "agent "+strings.Repeat("a", 60000)+"\n")

	// numbers
	for _, n := range []string{
		"5", "2.5", "0.5", "007", "10.", "1.2.3", "5.x", "0", "00.0", ".5", "5..5", "1.5.",
		"123456789012345", "1234567890123456", "1234567.89012345", "12345678.90123456", strings.Repeat("9", 400),
	} {
		add("lex/number-"+charSlug(n), charExpr(n))
	}

	// names
	for _, w := range []string{"_a", "9a", "a-b", "a-", "a_b-c9", "a1", "x.y", "agent", "yes", "reply"} {
		add("lex/name-"+charSlug(w), charLines("agent "+w))
	}
	add("lex/name-accented", charLines("agent Ação"))
	add("lex/name-uppercase-accented", charLines("agent ÉCOLE"))
	add("lex/name-japanese", charLines("agent 日本語"))
	add("lex/name-with-a-leading-hyphen", charLines("agent -a"))

	// characters that are not part of the language
	for i, c := range []string{
		"@", "(", ")", "+", "!", "$", "%", "{", "}", "'", "€", "😀", "\r", ">", "<", ";", "/", "\\", "|", "~", "^", "&", "*", "?",
		"`", "\x00", "\u00a0",
	} {
		add(fmt.Sprintf("lex/unknown-char-%02d", i), "agent A\n  goal \"g\" "+c+" x\n")
	}

	// symbols and comments
	add("lex/all-the-symbols", charLines("agent A", "  accepts a b", "  on a", "    x = [1, 2]", "    y = w.f k: x"))
	add("lex/comment-after-tokens", charLines("agent A  # the agent", `  goal "g" # why`))
	add("lex/comment-without-space", "agent A #x\n")
	add("lex/comment-empty", "agent A #\n")
	add("lex/comment-with-quotes", "agent A # it's \"quoted\"\n")
	add("lex/hash-inside-a-text", charLines("agent A", `  goal "a # not a comment"`))
	add("lex/comment-describes-accepts", charLines("agent A", `  goal "g"`, "  accepts ask city # the city to look up", "  on ask", `    reply "x"`))
	add("lex/comment-with-accents", charLines("agent A # olá, ação", `  goal "g"`))

	// texts: escapes, holes and the ways they go wrong
	for _, s := range []string{
		`""`, `"abc"`, `"a\nb"`, `"a\tb"`, `"a\"b"`, `"a\\b"`, `"a\{b}"`, `"a\qb"`, `"café ção"`, `"a # b"`, `"a]b"`,
		`"{city}"`, `"{ city }"`, `"{forecast.summary}"`, `"{a.b.c}"`, `"{a-b}"`, `"{1a}"`, `"{a} and {b}"`, `"x {a}"`, `"{a} x"`,
		`"{a}{b}"`, `"{ spaced . path }"`,
		`"{}"`, `"{ }"`, `"{.a}"`, `"{a.}"`, `"{a..b}"`, `"{a b}"`, `"{a{b}"`, `"{a,b}"`, `"{a:b}"`, `"{\"a\"}"`, `"{ação}"`,
		`"unclosed`, `"ends\`, `"quote\"`, `"{open`, `"x{"`, `"}"`, `"{a}}"`, `"\\"`, `"\"`, `"a"b`, `"a""b"`, `"a" "b"`,
		"\"tab\tinside\"",
	} {
		add("lex/text-"+charSlug(s), charExpr(s))
	}
}

// ---------- the file: where an agent starts and ends ----------

func charFileCases(add charAdd) {
	add("file/no-agent-word", charLines(`goal "x"`))
	add("file/indented-first-line", charLines("  agent A"))
	add("file/a-text-first", "\"x\"\n")
	add("file/a-number-first", "5\n")
	add("file/agent-without-a-name", "agent\n")
	add("file/agent-with-a-text-as-name", "agent \"A\"\n")
	add("file/agent-with-an-extra-word", "agent A B\n")
	add("file/two-agents", charLines("agent A", `  goal "a"`, "agent B", `  goal "b"`))
	add("file/two-agents-with-the-same-name", charLines("agent A", `  goal "a"`, "agent A", `  goal "b"`))
	add("file/agent-alone", "agent A\n")
	add("file/agent-called-agent", "agent agent\n")
	add("file/larger-than-1MiB", strings.Repeat("#"+strings.Repeat("x", 1000)+"\n", 1100))
	add("file/a-line-too-deep-under-the-agent", charLines("agent A", `    goal "x"`))
	add("file/a-line-much-too-deep-under-the-agent", charLines("agent A", `      goal "x"`))
	add("file/something-else-after-the-agent", charLines("agent A", `  goal "g"`, "foo"))
	add("file/agent-inside-an-agent", charLines("agent A", "  agent B"))
	var many []string
	for i := 0; i < 40; i++ {
		many = append(many, fmt.Sprintf("agent Agent%d", i), `  goal "g"`)
	}
	add("file/forty-agents", charLines(many...))
}

// ---------- the lines under `agent`: goal, link, remote, accepts, on ----------

func charDeclCases(add charAdd) {
	for _, d := range []string{
		// a line that does not begin with a word
		`"x"`, `5`, `.`, `[`, `:`,
		// goal
		`goal "Answer questions"`, `goal`, `goal 5`, `goal "a {b}"`, `goal "a" "b"`, `goal x`, `goal "a\nb"`, `goal ""`,
		// link
		`link B`, `link B from "b.ag"`, `link`, `link "x"`, `link B from`, `link B from b.ag`, `link B from "a{x}.ag"`,
		`link B C`, `link B from "x" extra`, `link B from ""`,
		// remote
		`remote Bob at "https://host:8080"`, `remote`, `remote Bob`, `remote Bob at`, `remote Bob at https`,
		`remote Bob at "u" extra`, `remote Bob on "u"`, `remote Bob at "https://{host}/x"`, `remote "Bob" at "u"`,
		// accepts
		`accepts ask city`, `accepts ask`, `accepts`, `accepts "x"`, `accepts ask city "x"`, `accepts ask 5`,
		`accepts ask a, b`, `accepts ask a b c`, `accepts ask # says hello`, `accepts ask city # the city`, `accepts ask a a`,
		// on, with nothing under it
		`on ask`, `on`, `on ask extra`, `on "x"`, `on start`,
		// an agent in an agent
		`agent B`,
		// words that are not declarations
		`gaol "x"`, `foo "x"`, `tools file`, `accept x`, `Goal "x"`, `link2 B`, `remot Bob at "u"`, `onn ask`, `goall "x"`,
	} {
		add("decl/"+charSlug(d), charDecl(d))
	}

	add("decl/goal-twice", charLines("agent A", `  goal "one"`, `  goal "two"`))
	add("decl/several-links-and-remotes", charLines("agent A", `  goal "g"`, "  link B", `  link C from "c.ag"`, `  remote R at "u1"`, `  remote S at "u2"`))
	add("decl/accepts-in-several-forms", charLines("agent A", `  goal "g"`, "  accepts a", "  accepts b x", "  accepts c x y # two values", "  on a", `    reply "a"`))
	add("handler/start-and-messages", charLines("agent A", `  goal "g"`, "  accepts ask", "  on start", "    n = 1", "  on ask", "    reply n"))
	add("handler/start-twice", charLines("agent A", `  goal "g"`, "  on start", "    n = 1", "  on start", "    n = 2"))
	add("handler/start-is-also-accepted", charLines("agent A", `  goal "g"`, "  accepts start", "  on start", "    n = 1"))
	add("handler/nothing-under-it-then-another", charLines("agent A", `  goal "g"`, "  accepts a b", "  on a", "  on b", `    reply "x"`))
	add("handler/nothing-under-it-at-the-end", charLines("agent A", `  goal "g"`, "  accepts a", "  on a"))
	add("handler/body-too-deep", charLines("agent A", `  goal "g"`, "  accepts a", "  on a", `        reply "x"`))
	add("handler/two-handlers-and-more-lines", charLines("agent A", `  goal "g"`, "  accepts a b", "  on a", `    reply "a"`, "  on b", `    reply "b"`, `  goal "again"`))
}

// ---------- `tool`: every kind and every clause ----------

func charToolCases(add charAdd) {
	for _, d := range []string{
		`tool`, `tool "x"`, `tool 5`,
		// file
		`tool file`, `tool file "docs/"`, `tool file readonly`, `tool file "d" readonly`, `tool file readonly readonly`,
		`tool file ""`, `tool file "{x}"`, `tool file "a" "b"`, `tool file allow`, `tool file readonly "d"`,
		// http
		`tool http`, `tool http readonly`, `tool http allow "api.example.com"`, `tool http allow "a.com" "b.com"`,
		`tool http allow private`, `tool http allow private "x.com"`, `tool http allow`, `tool http allow readonly`,
		`tool http allow "a.com" allow "b.com"`, `tool http readonly allow private`, `tool http allow "a.com" readonly`,
		`tool http allow "*.example.com"`, `tool http allow "com"`, `tool http allow "10.0.0.1"`, `tool http allow "a_b.com"`,
		`tool http allow "-a.com"`, `tool http allow "a-.com"`, `tool http allow "a..com"`, `tool http allow ""`,
		`tool http allow "*.com"`, `tool http allow "1.2"`, `tool http allow "a.1"`, `tool http allow "EXAMPLE.com"`,
		`tool http allow "ex ample.com"`, `tool http allow private allow private`, `tool http extra`,
		`tool http allow "` + strings.Repeat("a", 63) + `.com"`, `tool http allow "` + strings.Repeat("a", 64) + `.com"`,
		// state, clock, env
		`tool state`, `tool clock`, `tool state x`, `tool clock "x"`, `tool state readonly`,
		`tool env "HOME" "USER"`, `tool env`, `tool env HOME`, `tool env "A" extra`, `tool env "A"`,
		// tool servers
		`tool w from mcp "node server.js"`, `tool w from`, `tool w from http "x"`, `tool w from mcp`, `tool w from mcp x`,
		`tool w from mcp ""`, `tool w from mcp "   "`, `tool w from mcp "cmd" env "A" "B"`, `tool w from mcp "cmd" env`,
		`tool w from mcp "cmd" env x`, `tool w from mcp "cmd" extra`, `tool w from mcp "cmd" env "1A"`,
		`tool w from mcp "cmd" env "A-B"`, `tool w from mcp "cmd" env "ANTHROPIC_API_KEY"`,
		`tool w from mcp "cmd" env "OPENAI_API_KEY" "METAGENTE_TOKEN"`, `tool w from mcp "cmd" env ""`,
		`tool w from mcp "cmd" env "OK" "bad name"`, `tool w from mcp "cmd {x}"`,
		`tool w from mcp "https://x.example/mcp"`, `tool w from mcp "http://x.example/mcp"`,
		// versions that are not pinned
		`tool w from mcp "npx -y pkg"`, `tool w from mcp "npx pkg@latest"`, `tool w from mcp "npx pkg@1.2.3"`,
		`tool w from mcp "npx @scope/pkg"`, `tool w from mcp "npx @scope/pkg@1.0.0"`, `tool w from mcp "npx @scope/pkg@latest"`,
		`tool w from mcp "uvx pkg"`, `tool w from mcp "uvx pkg==1.0"`, `tool w from mcp "uvx pkg=="`, `tool w from mcp "bunx pkg"`,
		`tool w from mcp "pipx run pkg"`, `tool w from mcp "pipx run pkg==2"`, `tool w from mcp "pipx install pkg"`,
		`tool w from mcp "pipx"`, `tool w from mcp "pipx run"`, `tool w from mcp "python server.py"`,
		`tool w from mcp "/usr/bin/npx pkg"`, `tool w from mcp "NPX pkg"`, `tool w from mcp "npx"`, `tool w from mcp "npx -y"`,
		`tool w from mcp "npx 'pkg with space'"`, `tool w from mcp "npx -y --foo pkg"`, `tool w from mcp "npx.cmd pkg"`,
		`tool w from mcp "npx pkg@"`, `tool w from mcp "npx a@b@c"`, `tool w from mcp "uvx --from pkg cmd"`,
		// names that are not tools
		`tool fil`, `tool htp`, `tool weather`, `tool clok`, `tool x y`, `tool File`, `tool statee`, `tool envv "A"`,
		// names of variables that a tool server may be given
		`tool w from mcp "cmd" env "A1"`, `tool w from mcp "cmd" env "_X" "a_b9" "Z"`, `tool w from mcp "cmd" env "9A"`,
		`tool w from mcp "cmd" env "A.B"`, `tool w from mcp "cmd" env "A B"`, `tool w from mcp "cmd" env "É"`,
		// readonly on a tool server, and its clauses in any order
		`tool w from mcp "cmd" readonly`, `tool w from mcp "cmd" readonly env "A"`, `tool w from mcp "cmd" env "A" readonly`,
		`tool w from mcp "cmd" env "A" readonly env "B"`, `tool w from mcp "cmd" readonly readonly`,
		`tool w from mcp "cmd" readonly extra`, `tool w from mcp "cmd" readonly env`, `tool w from mcp "https://x.example/mcp" readonly`,
	} {
		add("tool/"+charSlug(d), charDecl(d))
	}

	add("tool/the-same-tool-twice", charAgent([]string{"tool file", "tool file"}))
	add("tool/three-times", charAgent([]string{"tool clock", "tool clock", "tool clock"}))
	add("tool/same-name-as-a-link", charAgent([]string{"tool file", "link file"}))
	add("tool/same-name-as-a-remote", charAgent([]string{"tool file", `remote file at "u"`}))
	add("tool/all-kinds-together", charAgent([]string{
		`tool file "docs/" readonly`, `tool http allow "a.com" private`, "tool state", "tool clock", `tool env "HOME"`,
		`tool w from mcp "npx -y w@1.0.0" env "HTTPS_PROXY"`,
	}))
}

// ---------- the statements: reply, fail, if, for, assignment, calls ----------

func charStatementCases(add charAdd) {
	for _, lines := range [][]string{
		// an expression where a statement should be
		{`"x"`}, {`5`}, {`[1, 2]`}, {`.`}, {`]`}, {`"a" "b"`}, {`:`}, {`= 1`},
		// reply and fail
		{`reply "x"`}, {`reply`}, {`fail "boom"`}, {`fail`}, {`reply "a" "b"`}, {`reply x`}, {`fail x.y`}, {`reply yes`}, {`reply 1 2`},
		// if
		{`if yes`, `  reply "a"`},
		{`if yes`, `  reply "a"`, `otherwise`, `  reply "b"`},
		{`if yes`},
		{`if`},
		{`if yes yes`, `  reply "a"`},
		{`if yes`, `      reply "deep"`},
		{`if yes`, `  reply "a"`, `otherwise x`, `  reply "b"`},
		{`if yes`, `  reply "a"`, `otherwise`},
		{`if yes`, `  reply "a"`, `otherwise`, `reply "after"`},
		{`otherwise`, `  reply "x"`},
		{`reply "a"`, `otherwise`, `  reply "b"`},
		{`if yes`, `  if no`, `    reply "a"`, `  otherwise`, `    reply "b"`, `otherwise`, `  reply "c"`},
		{`if yes`, `  reply "a"`, `  otherwise`, `  reply "b"`},
		{`if yes`, `  reply "a"`, `x = 1`, `otherwise`, `  reply "b"`},
		{`if x is "x"`, `  reply "a"`},
		{`if think "ok?" is "yes"`, `  reply "a"`},
		{`if yes`, `  reply "a"`, `otherwise`, `  if no`, `    reply "b"`, `  otherwise`, `    reply "c"`},
		{`if yes`, `  x = 1`, `  reply x`, `reply "b"`},
		// for
		{`for c in [1, 2]`, `  reply c`},
		{`for`, `  reply 1`},
		{`for c`, `  reply c`},
		{`for c in`, `  reply c`},
		{`for c of x`, `  reply c`},
		{`for "c" in x`, `  reply 1`},
		{`for c in x extra`, `  reply c`},
		{`for c in x`},
		{`for c in x.y`, `  for d in c`, `    reply d`},
		{`for c in w.f k: 1`, `  reply c`},
		{`for c in [1]`, `  reply c`, `  fail "x"`},
		// assignment
		{`a = 1`}, {`a = think "x"`}, {`a = w.f k: 1`}, {`a =`}, {`a = 1 2`}, {`a == 1`}, {`a.b = 1`}, {`a, b = 1`}, {`a : 1`},
		{`nothing = 1`}, {`reply = 1`}, {`if = 1`}, {`for = 1`}, {`a = [1, 2]`}, {`a = b`}, {`a = "x {x}"`}, {`think = 1`},
		// calls and think as statements
		{`think "x"`}, {`think "x" using file`}, {`think`}, {`thinking = 1`}, {`w.f`}, {`w.f k: 1`}, {`w.f k: 1 within 5 seconds`},
		{`a.b.c d: 1`}, {`a.`}, {`a.b.`}, {`think "x" "y"`},
		// a line that is not a statement
		{`replay "x"`}, {`iff yes`}, {`fial "x"`}, {`foo bar`}, {`foo`}, {`Reply "x"`}, {`yes`}, {`no`}, {`not yes`}, {`and`},
		{`x y`}, {`x "y"`}, {`x 5`}, {`x [1]`}, {`otherwis`}, {`fo x in y`}, {`reply2 "x"`}, {`nothing`}, {`within 5 seconds`},
		// indentation
		{`reply "a"`, `  reply "deeper"`},
		{`reply "a"`, `    reply "much deeper"`},
		// several statements
		{`a = 1`, `b = 2`, `reply a`},
		{`fail "x"`, `reply "unreachable"`},
		// a block with something wrong in it, in each place that a block can be
		{`if yes`, `  reply "a"`, `otherwise`, `  foo bar`},
		{`if yes`, `  reply "a"`, `otherwise`, `      reply "deep"`},
		{`if yes`, `  foo bar`},
		{`if yes`, `  reply "a"`, `otherwise`, `  reply`},
		{`for c in x`, `  foo bar`},
		{`for c in x`, `      reply "deep"`},
		{`for c in x`, `  reply`},
		{`if yes`, `  for c in x`, `    foo bar`},
		{`if yes`, `  reply "a"`, `otherwise`, `  for c in x`, `    reply c`, `  reply "b"`},
	} {
		add("stmt/"+charSlug(strings.Join(lines, " ")), charStmts(lines...))
	}

	// nesting
	var deep []string
	for i := 0; i < 28; i++ {
		deep = append(deep, strings.Repeat("  ", i)+"if yes")
	}
	add("stmt/nested-ifs-28-deep", charStmts(append(deep, strings.Repeat("  ", 28)+`reply "deep"`)...))
	deep = nil
	for i := 0; i < 31; i++ {
		deep = append(deep, strings.Repeat("  ", i)+"if yes")
	}
	add("stmt/nested-ifs-31-deep", charStmts(append(deep, strings.Repeat("  ", 31)+`reply "deep"`)...))
	var fors []string
	for i := 0; i < 6; i++ {
		fors = append(fors, strings.Repeat("  ", i)+fmt.Sprintf("for v%d in x", i))
	}
	add("stmt/nested-fors", charStmts(append(fors, strings.Repeat("  ", 6)+"reply v5")...))

	// many statements
	var many []string
	for i := 0; i < 300; i++ {
		many = append(many, fmt.Sprintf("v%d = %d", i, i))
	}
	add("stmt/three-hundred-assignments", charStmts(many...))
}

// ---------- the expressions: values, lists, paths, calls, operators, think ----------

func charExprCases(add charAdd) {
	for _, e := range []string{
		// values
		`"x"`, `"x {x}"`, `5`, `2.5`, `yes`, `no`, `nothing`,
		// lists
		`[]`, `[1]`, `[1, 2, 3]`, `[[1], [2]]`, `[1,]`, `[,]`, `[1 2]`, `[1,`, `[`, `[1, 2`, `[yes, no, nothing, "x", a.b]`,
		`[w.f city: 1]`, `[think "x"]`, `[ ]`, `[1, [2, [3]]]`, `]`, `[1]]`, `[[]`, `[1, , 2]`,
		// paths
		`a`, `a.b`, `a.b.c`, `a.`, `a..b`, `a.5`, `a."x"`, `a.b.`, `x.y.z`, `yes.b`, `nothing.b`, `no.x`, `think.x`, `a-b`, `a-b.c-d`,
		// calls
		`w.f`, `w.f k: 1`, `w.f k: 1 j: "x"`, `w.f k: a.b`, `w.f k: [1]`, `w.f k: think "x"`, `w.f k:`, `w.f k 1`,
		`w.f within 5 seconds`, `w.f within 1 second`, `w.f within 2.5 seconds`, `w.f within`, `w.f within x`, `w.f within 5`,
		`w.f within 5 minutes`, `w.f k: 1 within 5 seconds`, `a within 5 seconds`, `a k: 1`, `w.f.g k: 1`, `w.f k: 1 k: 2`,
		`w.f within 5 seconds extra`, `w.f within 0 seconds`, `w.f k: w.g`, `w.f k: x j: y`, `w.f : 1`, `w.f k: : 1`,
		`w.f k: nothing`, `w.f k: yes`, `w.f k: "a {x}"`, `w.f k: 5`, `w.f k: 1 j:`, `w.f within 5 seconds k: 1`,
		// operators
		`a and b`, `a or b`, `not a`, `not not a`, `a and b or c`, `a or b and c`, `not a and b`, `a is b`, `a is not b`,
		`a is more than 3`, `a is less than 3`, `a is more 3`, `a is less`, `a contains "x"`, `a is`, `a contains`, `a is not`,
		`a is b is c`, `a is more than`, `a and`, `a or`, `not`, `a > b`, `(a)`, `x is "a" and y is "b"`, `a is w.f k: 1`,
		`w.f k: 1 is "x"`, `a is not not b`, `a contains [1]`, `[1] contains 1`, `a and not b`, `not a is b`,
		`a is b and c is d or e`, `a or b or c`, `a and b and c`, `a is yes`, `a is nothing`, `a is more than w.f`,
		`a is more than less than 3`, `a is less than`, `a contains contains b`,
		// think
		`think "x"`, `think "x" using a b`, `think "x" using`, `think "x" using and`, `think "x" using a and yes`,
		`think "x" using is`, `think`, `think 5`, `think a.b`, `think [1]`, `think think "x"`, `think "x" using file clock`,
		`think "x" using within`, `think "x" using a contains "b"`, `think "x" using a, b`, `think "x" using "a"`,
		`think "x" using or`, `think "x {x}"`, `think x`, `think "x" using file within 5 seconds`, `think "p" is "q"`,
		`think "x" or yes`, `not think "x"`, `think "a" using a or think "b"`, `think "x" using contains`,
	} {
		add("expr/"+charSlug(e), charExpr(e))
	}

	// the limits
	add("expr/list-of-10000-items", charExpr("["+strings.Repeat("1, ", 9999)+"1]"))
	add("expr/list-of-10001-items", charExpr("["+strings.Repeat("1, ", 10000)+"1]"))
	add("expr/lists-100-deep", charExpr(strings.Repeat("[", 100)+strings.Repeat("]", 100)))
	add("expr/not-200-times", charExpr(strings.Repeat("not ", 200)+"a"))
	add("expr/or-of-500-terms", charExpr(strings.Repeat("a or ", 500)+"a"))
	add("expr/call-with-100-arguments", charExpr("w.f "+strings.Repeat("k: 1 ", 100)))
	add("expr/path-of-100-segments", charExpr("a"+strings.Repeat(".b", 100)))
}

// ---------- the checks: names, permissions, actions, parameters, warnings ----------

type charCheck struct {
	label string
	decls []string
	stmts []string
}

func charCheckCases(add charAdd) {
	// the structure of an agent
	add("check/no-goal", charLines("agent A", "  accepts go", "  on go", `    reply "x"`))
	add("check/accepts-twice", charLines("agent A", `  goal "g"`, "  accepts go", "  accepts go", "  on go", `    reply "x"`))
	add("check/accepts-without-a-handler", charLines("agent A", `  goal "g"`, "  accepts go"))
	add("check/handler-without-accepts", charLines("agent A", `  goal "g"`, "  on go", `    reply "x"`))
	add("check/handler-close-to-an-accept", charLines("agent A", `  goal "g"`, "  accepts search", "  on serch", `    reply "x"`))
	add("check/handler-far-from-every-accept", charLines("agent A", `  goal "g"`, "  accepts search", "  on zzzzzz", `    reply "x"`))
	add("check/handler-twice", charLines("agent A", `  goal "g"`, "  accepts go", "  on go", `    reply "a"`, "  on go", `    reply "b"`))
	add("check/two-agents-with-the-same-name", charLines("agent A", `  goal "g"`, "agent A", `  goal "g"`))
	add("check/the-name-of-a-tool-a-link-and-a-remote", charAgent([]string{"tool file", "link file", `remote file at "u"`}))
	add("check/problems-in-several-agents-come-in-order", charLines(
		"agent B", "  accepts x", "  on y", `    reply "{nope}"`,
		"agent A", "  accepts go", "  on go", `    reply "{missing}"`, `    zzz.go`,
		"agent C", `  goal "g"`, "  tool file", "  tool file"))

	// the variables that a handler knows
	add("check/values-of-accepts-are-known", charLines("agent A", `  goal "g"`, "  accepts go city days", "  on go", `    reply "{city} {days}"`))
	add("check/unknown-variable-in-a-text", charStmts(`reply "{nope}"`))
	add("check/unknown-variable-close-to-a-known-one", charStmts(`reply "{xx}"`))
	add("check/unknown-variable-in-a-path", charStmts(`reply nope.deeper`))
	add("check/unknown-variable-close-to-a-tool", charAgent([]string{"tool clock"}, `reply clok`))
	add("check/variable-from-an-assignment", charStmts("y = 1", "reply y"))
	add("check/variable-used-before-it-is-assigned", charStmts("reply y", "y = 1"))
	add("check/for-variable", charStmts("for c in [1]", "  reply c"))
	add("check/for-variable-stays-after-the-loop", charStmts("for c in [1]", "  reply c", "reply c"))
	add("check/variable-from-an-if-branch", charStmts("if yes", "  y = 1", "otherwise", "  z = 2", "reply y", "reply z"))
	add("check/start-does-not-see-the-values-of-accepts", charLines("agent A", `  goal "g"`, "  accepts go x", "  on start", "    reply x", "  on go", "    reply x"))
	add("check/start-only", charLines("agent A", `  goal "g"`, "  on start", `    reply "x"`))
	add("check/names-inside-conditions", charStmts("if nope is yes and nada", "  reply 1"))
	add("check/names-inside-lists", charStmts("y = [a, b, x]"))
	add("check/names-inside-call-arguments", charAgent([]string{"tool clock"}, `clock.wait seconds: nope`))

	// assigning to the name of something declared
	add("check/assign-to-the-name-of-a-tool", charAgent([]string{"tool file"}, "file = 1"))
	add("check/assign-to-the-name-of-a-link", charAgent([]string{"link B"}, "B = 1"))
	add("check/assign-to-the-name-of-a-remote", charAgent([]string{`remote R at "u"`}, "R = 1"))
	add("check/assign-to-a-built-in-name-that-was-not-declared", charStmts("file = 1"))

	// permissions: what is used without being declared
	add("check/call-to-a-name-that-was-never-declared", charStmts(`nope.go k: 1`))
	add("check/call-to-a-name-close-to-a-link", charAgent([]string{"link Weather"}, `Wether.go`))
	add("check/call-to-a-built-in-tool-that-was-not-declared", charStmts(`file.read path: "x"`))
	add("check/built-in-tool-in-a-text", charStmts(`reply "{clock.now}"`))
	add("check/built-in-tool-as-a-path", charStmts("y = clock.now"))
	add("check/bare-built-in-name", charStmts("y = file"))
	add("check/bare-declared-tool-name", charAgent([]string{"tool file"}, "y = file"))
	add("check/permission-reported-once-for-a-tool-in-a-line", charStmts(`reply "{file.a} {file.b}"`))
	add("check/permission-reported-in-each-line", charStmts(`reply "{file.a}"`, `reply "{file.b}"`))
	add("check/permission-for-three-tools-in-a-line", charStmts(`reply "{file.a} {clock.now} {http.get}"`))
	add("check/permission-in-the-start-handler", charLines("agent A", `  goal "g"`, "  on start", `    y = file.read path: "a"`))
	add("check/permission-in-a-condition", charStmts("if clock.now is 1", "  reply 1"))
	add("check/permission-in-a-for", charStmts("for c in file.read path: \"a\"", "  reply c"))
	add("check/permission-in-a-list-of-calls", charStmts(`y = [file.a, clock.b]`))

	// actions, required values and values that are not taken
	for _, c := range []charCheck{
		{"file-read", []string{"tool file"}, []string{`file.read path: "a"`}},
		{"file-write", []string{"tool file"}, []string{`file.write path: "a" text: "b"`}},
		{"file-write-without-text", []string{"tool file"}, []string{`file.write path: "a"`}},
		{"file-read-without-path", []string{"tool file"}, []string{`file.read`}},
		{"file-read-with-an-extra-value", []string{"tool file"}, []string{`file.read path: "a" extra: 1`}},
		{"file-with-an-action-close-to-read", []string{"tool file"}, []string{`file.reed path: "a"`}},
		{"file-with-an-action-that-does-not-exist", []string{"tool file"}, []string{`file.delete path: "a"`}},
		{"file-with-an-action-that-does-not-exist-and-no-values", []string{"tool file"}, []string{`file.delete`}},
		{"file-with-a-value-close-to-path", []string{"tool file"}, []string{`file.read pth: "a"`}},
		{"file-with-a-value-close-to-path-plural", []string{"tool file"}, []string{`file.read paths: "a"`}},
		{"file-with-a-value-from-another-action", []string{"tool file"}, []string{`file.read path: "a" text: "b"`}},
		{"readonly-file-write", []string{"tool file readonly"}, []string{`file.write path: "a" text: "b"`}},
		{"readonly-file-read", []string{"tool file readonly"}, []string{`file.read path: "a"`}},
		{"readonly-file-with-an-action-that-does-not-exist", []string{"tool file readonly"}, []string{`file.delete`}},
		{"readonly-file-write-in-a-text", []string{"tool file readonly"}, []string{`reply "{file.write}"`}},
		{"http-get", []string{"tool http"}, []string{`http.get url: "u"`}},
		{"http-post-with-a-body", []string{"tool http"}, []string{`http.post url: "u" body: "b"`}},
		{"http-post-without-a-body", []string{"tool http"}, []string{`http.post url: "u"`}},
		{"http-post-without-values", []string{"tool http"}, []string{`http.post`}},
		{"readonly-http-post", []string{"tool http readonly"}, []string{`http.post url: "u"`}},
		{"http-put", []string{"tool http"}, []string{`http.put url: "u"`}},
		{"http-get-with-a-value-close-to-url", []string{"tool http"}, []string{`http.get urll: "u"`}},
		{"env-get", []string{`tool env "HOME"`}, []string{`env.get name: "HOME"`}},
		{"env-get-without-a-name", []string{`tool env "HOME"`}, []string{`env.get`}},
		{"env-set", []string{`tool env "HOME"`}, []string{`env.set name: "X"`}},
		{"state-set", []string{"tool state"}, []string{`state.set key: "k" value: 1`}},
		{"state-get", []string{"tool state"}, []string{`state.get key: "k"`}},
		{"state-set-without-a-value", []string{"tool state"}, []string{`state.set key: "k"`}},
		{"state-put", []string{"tool state"}, []string{`state.put key: "k"`}},
		{"clock-now", []string{"tool clock"}, []string{`clock.now`}},
		{"clock-wait", []string{"tool clock"}, []string{`clock.wait seconds: 1`}},
		{"clock-wait-without-seconds", []string{"tool clock"}, []string{`clock.wait`}},
		{"clock-now-with-a-typo", []string{"tool clock"}, []string{`clock.nowx`}},
		{"clock-now-with-a-value", []string{"tool clock"}, []string{`clock.now x: 1`}},
		{"clock-with-a-dotted-action", []string{"tool clock"}, []string{`clock.now.later`}},
		{"two-problems-in-a-call", []string{"tool file"}, []string{`file.write extra: 1`}},
		{"a-path-to-a-tool-is-a-call", []string{"tool clock"}, []string{`y = clock.now`}},
		{"a-path-to-a-tool-with-a-wrong-action", []string{"tool clock"}, []string{`y = clock.nope`}},
		{"a-path-with-a-field-of-the-result", []string{"tool clock"}, []string{`y = clock.now.hour`}},
		// tool servers, links and remotes are not looked into
		{"server-any-action", []string{`tool w from mcp "cmd"`}, []string{`w.anything k: 1`}},
		{"server-dotted-action", []string{`tool w from mcp "cmd"`}, []string{`w.a.b`}},
		{"server-as-a-bare-name", []string{`tool w from mcp "cmd"`}, []string{"y = w"}},
		{"server-as-a-path", []string{`tool w from mcp "cmd"`}, []string{"y = w.f"}},
		{"server-in-a-text", []string{`tool w from mcp "cmd"`}, []string{`reply "{w.f}"`}},
		{"link-call", []string{"link B"}, []string{`B.go k: 1`}},
		{"link-as-a-bare-name", []string{"link B"}, []string{"y = B"}},
		{"remote-call", []string{`remote R at "u"`}, []string{`R.go`}},
		// text holes that name a tool
		{"hole-with-a-tool", []string{"tool clock"}, []string{`reply "{clock.now}"`}},
		{"hole-with-a-wrong-action", []string{"tool clock"}, []string{`reply "{clock.nope}"`}},
		{"hole-with-an-action-that-needs-values", []string{"tool file"}, []string{`reply "{file.read}"`}},
		// using
		{"using-a-declared-tool", []string{"tool clock"}, []string{`y = think "p" using clock`}},
		{"using-a-name-that-was-not-declared", []string{"tool clock"}, []string{`y = think "p" using nope`}},
		{"using-a-name-close-to-a-tool", []string{"tool clock"}, []string{`y = think "p" using cloc`}},
		{"using-a-declared-and-an-undeclared", []string{"tool clock"}, []string{`y = think "p" using clock nope`}},
		{"using-a-link", []string{"link B"}, []string{`y = think "p" using B`}},
		{"using-a-built-in-tool-that-was-not-declared", nil, []string{`y = think "p" using file`}},
		{"using-in-the-prompt-names", []string{"tool clock"}, []string{`y = think "{nope}" using clock`}},
		// warnings: think can use tools that change things
		{"think-with-file", []string{"tool file"}, []string{`y = think "p"`}},
		{"think-with-readonly-file", []string{"tool file readonly"}, []string{`y = think "p"`}},
		{"think-with-http", []string{"tool http"}, []string{`y = think "p"`}},
		{"think-with-file-and-http", []string{"tool file", "tool http"}, []string{`y = think "p"`}},
		{"think-with-state-and-clock", []string{"tool state", "tool clock"}, []string{`y = think "p"`}},
		{"think-with-using", []string{"tool file"}, []string{`y = think "p" using file`}},
		{"think-with-no-tools", nil, []string{`y = think "p"`}},
		{"think-in-a-condition", []string{"tool file"}, []string{`if think "p" is "y"`, `  reply "a"`}},
		{"think-in-a-condition-with-and", []string{"tool file"}, []string{`if think "p" is "y" and yes`, `  reply "a"`}},
		{"think-in-a-for", []string{"tool http"}, []string{"for c in [1]", `  y = think "p"`}},
		{"think-twice", []string{"tool file"}, []string{`y = think "a"`, `z = think "b"`}},
		{"think-with-and-without-using", []string{"tool file", "tool clock"}, []string{`y = think "a" using clock`, `z = think "b"`}},
		{"think-in-reply-and-fail", []string{"tool file"}, []string{`reply think "a"`, `fail think "b"`}},
		{"think-with-a-server", []string{`tool w from mcp "cmd"`}, []string{`y = think "p"`}},
		{"think-with-a-link", []string{"link B"}, []string{`y = think "p"`}},
	} {
		add("check/"+c.label, charAgent(c.decls, c.stmts...))
	}
	add("check/think-in-the-start-handler", charLines("agent A", `  goal "g"`, "  tool file", "  on start", `    y = think "p"`))
	add("check/think-in-several-handlers", charLines("agent A", `  goal "g"`, "  tool http", "  accepts a b", "  on a", `    y = think "p"`, "  on b", `    z = think "q"`))
	add("check/everything-wrong-at-once", charLines(
		"agent A",
		"  accepts go x",
		"  accepts go",
		"  accepts lonely",
		"  on go",
		`    reply "{nope}"`,
		`    file.read path: "a"`,
		`    nobody.go`,
		"  on stray",
		`    y = think "p" using ghost`,
		"  on go",
		`    reply 1`,
		`  tool http allow "com" "10.0.0.1"`,
		`  tool w from mcp "npx -y w" env "1A" "ANTHROPIC_API_KEY"`,
		`  tool w from mcp ""`,
		"  link w"))
}

// ---------- whole programs ----------

func charProgramCases(add charAdd) {
	add("program/kitchen-sink", charLines(
		"agent Kitchen",
		`  goal "Do a bit of everything"`,
		`  tool file "docs/"`,
		`  tool http allow "api.example.com" "*.example.org"`,
		`  tool env "HOME" "USER"`,
		"  tool state",
		"  tool clock",
		`  tool weather from mcp "npx -y weather-mcp@1.2.3" env "HTTPS_PROXY"`,
		"  link Helper",
		`  link Other from "other.ag"`,
		`  remote Bob at "https://bob.example:8080"`,
		"  accepts ask city days  # asks about a city",
		"  accepts other",
		"  on start",
		`    state.set key: "n" value: 0`,
		"  on ask",
		"    now = clock.now",
		`    n = state.get key: "n"`,
		"    data = weather.forecast city: city days: days within 30 seconds",
		"    if data.ok is yes and n is more than 3",
		`      reply "{city}: {data.summary} at {now}"`,
		"    otherwise",
		`      for c in [city, "Lisbon"]`,
		`        note = think "Describe {c}" using weather clock`,
		`        file.write path: "out.txt" text: note`,
		`      fail "no data for {city}"`,
		"  on other",
		"    reply Helper.go k: 1"))
	add("program/two-agents-that-call-each-other", charLines(
		"agent Boss",
		`  goal "Ask the worker"`,
		"  link Worker",
		"  accepts run job",
		"  on run",
		"    r = Worker.do job: job",
		`    reply "{r}"`,
		"agent Worker",
		`  goal "Do the job"`,
		"  accepts do job",
		"  on do",
		`    reply "done {job}"`))
	add("program/text-with-every-escape", charLines(
		"agent A", `  goal "g"`, "  accepts go", "  on go",
		`    reply "tab\there, newline\nthere, quote\" and backslash\\ and brace\{ and {x}"`))
}
