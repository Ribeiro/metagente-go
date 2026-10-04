package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"metagente/internal/diag"
)

// entry is one `key = value` line with the section it belongs to.
type entry struct {
	section string
	key     string
	value   any // string, int64, bool or []string
	line    int
}

// parseTOML reads the small part of TOML that metagente.toml uses: `[section]`
// headers, `key = value` lines, comments, texts in double or single quotes,
// whole numbers, true and false, and lists of texts that fit on one line. It
// refuses everything else with a plain message, instead of guessing.
func parseTOML(file, text string) ([]entry, error) {
	r := &tomlReader{file: file, text: text, seen: map[string]bool{}}
	for index, raw := range strings.Split(text, "\n") {
		if err := r.line(index+1, strings.TrimSpace(strings.TrimRight(raw, "\r"))); err != nil {
			return nil, err
		}
	}
	return r.entries, nil
}

// tomlReader holds what is known while the lines are read: the section that is open, the
// keys already seen, and the entries.
type tomlReader struct {
	file    string
	text    string
	section string
	seen    map[string]bool
	entries []entry
}

// fail is the problem of a line. A line of the credentials section may hold a token, so it is
// shown masked.
func (r *tomlReader) fail(number int, message string) error {
	source := r.text
	if r.section == "credentials" {
		source = maskLine(r.text, number)
	}
	return diag.New(message).At(r.file, number, 1).WithSource(source).
		Fix("check the quotes and the [section] names in metagente.toml")
}

func (r *tomlReader) line(number int, line string) error {
	switch {
	case line == "" || strings.HasPrefix(line, "#"):
		return nil
	case strings.HasPrefix(line, "[["):
		return r.fail(number, "this build does not read lists of tables written as `[[...]]`")
	case strings.HasPrefix(line, "["):
		return r.header(number, line)
	}
	return r.assignment(number, line)
}

// header reads `[section]` and opens that section.
func (r *tomlReader) header(number int, line string) error {
	end := strings.Index(line, "]")
	if end < 0 {
		return r.fail(number, "this section name is not closed with `]`")
	}
	name := strings.TrimSpace(line[1:end])
	if !validKey(name) {
		return r.fail(number, fmt.Sprintf("`%s` is not a section name I can read", name))
	}
	if rest := strings.TrimSpace(line[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
		return r.fail(number, "there is something after the section name")
	}
	r.section = name
	return nil
}

// assignment reads `key = value` and keeps it as an entry of the open section.
func (r *tomlReader) assignment(number int, line string) error {
	eq := strings.Index(line, "=")
	if eq < 0 {
		return r.fail(number, "I expected `key = value` on this line")
	}
	key := strings.TrimSpace(line[:eq])
	if !validKey(key) {
		return r.fail(number, r.unreadableKey(key))
	}
	value, rest, err := parseValue(strings.TrimSpace(line[eq+1:]))
	if err != nil {
		return r.fail(number, r.unreadableValue(err))
	}
	if rest = strings.TrimSpace(rest); rest != "" && !strings.HasPrefix(rest, "#") {
		return r.fail(number, "there is something after the value")
	}
	id := r.section + "\x00" + key
	if r.seen[id] {
		return r.fail(number, fmt.Sprintf("`%s` appears twice", key))
	}
	r.seen[id] = true
	r.entries = append(r.entries, entry{section: r.section, key: key, value: value, line: number})
	return nil
}

// unreadableKey says that a key cannot be read. In the credentials section it does not repeat the
// key, which may be a token.
func (r *tomlReader) unreadableKey(key string) string {
	if r.section == "credentials" {
		return "this is not a name I can read"
	}
	return fmt.Sprintf("`%s` is not a setting name I can read", key)
}

// unreadableValue says that a value cannot be read. In the credentials section the message of the
// reader is not used, because it repeats the word that it could not read.
func (r *tomlReader) unreadableValue(err error) string {
	if r.section == "credentials" {
		return "the value must be the NAME of an environment variable, in quotes, like \"BOB_TOKEN\", not the token itself"
	}
	return err.Error()
}

func validKey(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == '.':
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// parseValue reads one value from the start of s and returns what is left.
func parseValue(s string) (value any, rest string, err error) {
	if s == "" {
		return nil, "", errors.New("a value is missing after `=`")
	}
	switch s[0] {
	case '"':
		return parseBasicString(s)
	case '\'':
		end := strings.IndexByte(s[1:], '\'')
		if end < 0 {
			return nil, "", errors.New("this text starts with a quote but never ends")
		}
		return s[1 : 1+end], s[end+2:], nil
	case '[':
		return parseList(s)
	}
	end := strings.IndexAny(s, " \t#,]")
	if end < 0 {
		end = len(s)
	}
	word, rest := s[:end], s[end:]
	switch word {
	case "true":
		return true, rest, nil
	case "false":
		return false, rest, nil
	}
	n, parseErr := strconv.ParseInt(strings.ReplaceAll(word, "_", ""), 10, 64)
	if parseErr != nil {
		return nil, "", fmt.Errorf("I can read texts in quotes, whole numbers, true, false and lists of texts on one line, but not `%s`", word)
	}
	return n, rest, nil
}

func parseBasicString(s string) (any, string, error) {
	var b strings.Builder
	for i := 1; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			return b.String(), s[i+1:], nil
		case '\\':
			i++
			if i >= len(s) {
				return nil, "", errors.New("this text ends with a backslash")
			}
			switch s[i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			case '"', '\\':
				b.WriteByte(s[i])
			default:
				return nil, "", fmt.Errorf("I do not know the escape `\\%c` in this text", s[i])
			}
		default:
			b.WriteByte(c)
		}
	}
	return nil, "", errors.New("this text starts with a quote but never ends")
}

func parseList(s string) (any, string, error) {
	items := []string{}
	rest := s[1:]
	for {
		rest = strings.TrimLeft(rest, " \t")
		if rest == "" {
			return nil, "", errors.New("this list is not closed with `]` (a list must fit on one line)")
		}
		if rest[0] == ']' {
			return items, rest[1:], nil
		}
		value, after, err := parseValue(rest)
		if err != nil {
			return nil, "", err
		}
		text, ok := value.(string)
		if !ok {
			return nil, "", errors.New("lists can only hold texts in quotes")
		}
		items = append(items, text)
		rest = strings.TrimLeft(after, " \t")
		switch {
		case strings.HasPrefix(rest, ","):
			rest = rest[1:]
		case strings.HasPrefix(rest, "]"):
		case rest == "":
			return nil, "", errors.New("this list is not closed with `]` (a list must fit on one line)")
		default:
			return nil, "", errors.New("I expected `,` or `]` in this list")
		}
	}
}

// maskLine returns the text with the value on one line hidden, so an error can
// still show where the problem is without repeating what may be a secret. A line
// without `=` is hidden whole.
func maskLine(text string, number int) string {
	lines := strings.Split(text, "\n")
	if number < 1 || number > len(lines) {
		return text
	}
	line := lines[number-1]
	if eq := strings.Index(line, "="); eq >= 0 {
		lines[number-1] = strings.TrimRight(line[:eq], " \t") + ` = "(hidden)"`
	} else {
		lines[number-1] = "(hidden)"
	}
	return strings.Join(lines, "\n")
}
