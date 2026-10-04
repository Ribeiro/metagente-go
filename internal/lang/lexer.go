package lang

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"metagente/internal/diag"
)

// TokenKind is the kind of a token.
type TokenKind int

const (
	TokWord TokenKind = iota
	TokNumber
	TokString
	TokSym
)

// TextPart is a piece of a quoted text: literal characters, or a `{name}`
// hole that is filled in when the agent runs.
type TextPart struct {
	Lit   string
	Var   []string
	IsVar bool
}

// Token is one token of a line. Only the field that matches Kind is set.
type Token struct {
	Kind   TokenKind
	Word   string
	Number float64
	Parts  []TextPart
	Sym    rune
	// Col is the 1-based column.
	Col int
}

// Describe says how the token reads in an error message.
func (t Token) Describe() string {
	switch t.Kind {
	case TokWord:
		return "`" + t.Word + "`"
	case TokNumber:
		return "`" + strconv.FormatFloat(t.Number, 'g', -1, 64) + "`"
	case TokString:
		return "a piece of text"
	default:
		return "`" + string(t.Sym) + "`"
	}
}

// Line is one logical line: its indentation level (two spaces per level) and
// its tokens.
type Line struct {
	Number int
	Indent int
	Tokens []Token
	// Comment is the text after `#`, if any.
	Comment string
}

func isDigit(c rune) bool {
	return c >= '0' && c <= '9'
}

func isNameRune(c rune) bool {
	return unicode.IsLetter(c) || unicode.IsNumber(c) || c == '_' || c == '-'
}

// lex turns source text into logical lines of tokens. Indentation is two
// spaces per level; blank lines and comment-only lines are dropped.
func lex(file, source string) ([]Line, error) {
	var lines []Line
	for index, raw := range strings.Split(source, "\n") {
		line, kept, err := lexLine(file, source, index+1, strings.TrimRight(raw, "\r"))
		if err != nil {
			return nil, err
		}
		if kept {
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// lexLine reads one line. It says whether the line is kept: a blank line and a line that is only a
// comment are not.
func lexLine(file, source string, number int, raw string) (Line, bool, error) {
	fail := func(col int, msg, fix string) error {
		return diag.New(msg).At(file, number, col).WithSource(source).Fix(fix)
	}
	if strings.TrimSpace(raw) == "" {
		return Line{}, false, nil
	}
	if len(raw) > MaxLineBytes {
		return Line{}, false, fail(1, "this line is longer than 64 KiB", "split it into shorter lines")
	}
	chars := []rune(raw)
	if err := checkNoTab(chars, fail); err != nil {
		return Line{}, false, err
	}
	spaces := leadingSpaces(chars)
	if spaces < len(chars) && chars[spaces] == '#' {
		return Line{}, false, nil
	}
	if err := checkIndentation(spaces, fail); err != nil {
		return Line{}, false, err
	}
	tokens, comment, err := lexTokens(chars, spaces, fail)
	if err != nil {
		return Line{}, false, err
	}
	return Line{Number: number, Indent: spaces / 2, Tokens: tokens, Comment: comment}, true, nil
}

// checkNoTab refuses a line whose indentation has a tab in it.
func checkNoTab(chars []rune, fail func(col int, msg, fix string) error) error {
	for i, c := range chars {
		if c == ' ' {
			continue
		}
		if c == '\t' {
			return fail(i+1, "this line is indented with a tab",
				"use two spaces for each level of indentation instead of tabs")
		}
		break
	}
	return nil
}

func leadingSpaces(chars []rune) int {
	spaces := 0
	for spaces < len(chars) && chars[spaces] == ' ' {
		spaces++
	}
	return spaces
}

// checkIndentation wants two spaces for each level, and not too many levels.
func checkIndentation(spaces int, fail func(col int, msg, fix string) error) error {
	if spaces%2 != 0 {
		return fail(1,
			fmt.Sprintf("this line is indented by %d spaces, but each level needs exactly 2", spaces),
			"indent with 2, 4, 6 ... spaces")
	}
	if spaces/2 > MaxIndentLevels {
		return fail(1,
			fmt.Sprintf("this line is nested deeper than %d levels", MaxIndentLevels),
			"move some of the lines up, or split the work between agents")
	}
	return nil
}

// lexTokens reads the tokens of a line from the end of its indentation, and the comment that ends it.
func lexTokens(chars []rune, start int, fail func(col int, msg, fix string) error) ([]Token, string, error) {
	var tokens []Token
	comment := ""
	i := start
	for i < len(chars) {
		c := chars[i]
		col := i + 1
		switch {
		case c == ' ':
			i++
		case c == '#':
			comment = strings.TrimSpace(string(chars[i+1:]))
			i = len(chars)
		case c == '"':
			parts, next, err := lexString(chars, i, func(msg, fix string) error {
				return fail(col, msg, fix)
			})
			if err != nil {
				return nil, "", err
			}
			tokens = append(tokens, Token{Kind: TokString, Parts: parts, Col: col})
			i = next
		case isDigit(c):
			token, next, err := lexNumber(chars, i, fail)
			if err != nil {
				return nil, "", err
			}
			tokens = append(tokens, token)
			i = next
		case unicode.IsLetter(c) || c == '_':
			token, next := lexWord(chars, i)
			tokens = append(tokens, token)
			i = next
		case strings.ContainsRune(".:=,[]", c):
			tokens = append(tokens, Token{Kind: TokSym, Sym: c, Col: col})
			i++
		default:
			return nil, "", fail(col, fmt.Sprintf("I do not understand the character `%c`", c),
				"remove it, or put it inside quotes if it is part of a text")
		}
	}
	return tokens, comment, nil
}

// lexNumber reads a number from chars[start]. A dot only belongs to the number if a digit follows it.
func lexNumber(chars []rune, start int, fail func(col int, msg, fix string) error) (Token, int, error) {
	col := start + 1
	i := start
	for i < len(chars) && (isDigit(chars[i]) || chars[i] == '.') {
		if chars[i] == '.' && !(i+1 < len(chars) && isDigit(chars[i+1])) {
			break
		}
		i++
	}
	text := string(chars[start:i])
	n, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return Token{}, 0, fail(col, fmt.Sprintf("`%s` is not a number I can read", text),
			"write it like 5 or 2.5")
	}
	if countDigits(text) > MaxNumberDigits {
		return Token{}, 0, fail(col, fmt.Sprintf("`%s` has too many digits", text),
			fmt.Sprintf("use a number with at most %d digits", MaxNumberDigits))
	}
	return Token{Kind: TokNumber, Number: n, Col: col}, i, nil
}

// lexWord reads a name from chars[start].
func lexWord(chars []rune, start int) (Token, int) {
	i := start
	for i < len(chars) && isNameRune(chars[i]) {
		i++
	}
	return Token{Kind: TokWord, Word: string(chars[start:i]), Col: start + 1}, i
}

func countDigits(text string) int {
	n := 0
	for _, c := range text {
		if isDigit(c) {
			n++
		}
	}
	return n
}

// lexString reads a quoted text that starts at chars[start] == '"'. It
// returns the parts and the index right after the closing quote.
func lexString(chars []rune, start int, fail func(msg, fix string) error) ([]TextPart, int, error) {
	var text textBuilder
	i := start + 1
	for {
		if i >= len(chars) {
			return nil, 0, fail("this text starts with a quote but never ends",
				"add a closing quote (\") at the end of the text")
		}
		switch chars[i] {
		case '"':
			return text.finish(), i + 1, nil
		case '\\':
			escaped, err := lexEscape(chars, i, fail)
			if err != nil {
				return nil, 0, err
			}
			text.lit.WriteRune(escaped)
			i += 2
		case '{':
			hole, next, err := lexHole(chars, i, fail)
			if err != nil {
				return nil, 0, err
			}
			text.addHole(hole)
			i = next
		default:
			text.lit.WriteRune(chars[i])
			i++
		}
	}
}

// textBuilder collects the parts of a text: the characters that are written as they are, and the holes.
type textBuilder struct {
	parts []TextPart
	lit   strings.Builder
}

// addHole closes the literal that came before a hole, if there is one, and adds the hole.
func (b *textBuilder) addHole(hole TextPart) {
	if b.lit.Len() > 0 {
		b.parts = append(b.parts, TextPart{Lit: b.lit.String()})
		b.lit.Reset()
	}
	b.parts = append(b.parts, hole)
}

// finish gives the parts. A text with nothing in it still has one part, an empty literal.
func (b *textBuilder) finish() []TextPart {
	if b.lit.Len() > 0 || len(b.parts) == 0 {
		b.parts = append(b.parts, TextPart{Lit: b.lit.String()})
	}
	return b.parts
}

// lexEscape reads the character after a backslash: \n and \t are a line break and a tab, and any
// other character stands for itself.
func lexEscape(chars []rune, at int, fail func(msg, fix string) error) (rune, error) {
	if at+1 >= len(chars) {
		return 0, fail("this text ends with a backslash",
			"remove the backslash, or write \\\\ for a real one")
	}
	switch next := chars[at+1]; next {
	case 'n':
		return '\n', nil
	case 't':
		return '\t', nil
	default:
		return next, nil
	}
}

// lexHole reads a {name} of a text, from the brace at chars[start]. It returns the hole and the
// index right after the closing brace.
func lexHole(chars []rune, start int, fail func(msg, fix string) error) (TextPart, int, error) {
	end := -1
	for k := start; k < len(chars); k++ {
		if chars[k] == '}' {
			end = k
			break
		}
	}
	if end < 0 {
		return TextPart{}, 0, fail("a { in this text is never closed",
			"close it with }, or write \\{ if you want a real brace")
	}
	inner := string(chars[start+1 : end])
	path, ok := holePath(inner)
	if !ok {
		return TextPart{}, 0, fail(fmt.Sprintf("`{%s}` inside this text is not a name I can fill in", inner),
			"put a name between the braces, like {city} or {forecast.summary}")
	}
	return TextPart{Var: path, IsVar: true}, end + 1, nil
}

// holePath splits what is between the braces into the names of a path, and says whether it is one:
// names separated by dots, none empty, spaces allowed around each.
func holePath(inner string) ([]string, bool) {
	trimmed := strings.TrimSpace(inner)
	segments := strings.Split(trimmed, ".")
	path := make([]string, len(segments))
	valid := trimmed != ""
	for k, segment := range segments {
		segment = strings.TrimSpace(segment)
		path[k] = segment
		if segment == "" {
			valid = false
			continue
		}
		for _, r := range segment {
			if !isNameRune(r) {
				valid = false
			}
		}
	}
	return path, valid
}
