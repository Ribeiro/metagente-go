package config

import (
	"errors"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"

	"metagente/internal/diag"
)

// entry is one setting with the section it belongs to and the line it is on.
type entry struct {
	section string
	key     string
	value   any // what the TOML library read: string, int64, bool, float64, []any, a table...
	line    int
}

// parseTOML reads metagente.toml with a TOML library, so that anything TOML allows is
// read as TOML reads it (lists over several lines, inline tables, every kind of
// text). What comes out is a list of settings with their lines, in the order of the
// file; what each setting must be is checked by its setter, not here.
//
// A line of the credentials section may hold a token, so a problem there is told
// without the words of the library and with the line hidden.
func parseTOML(file, text string) ([]entry, error) {
	var doc map[string]any
	if err := toml.Unmarshal([]byte(text), &doc); err != nil {
		return nil, syntaxProblem(file, text, err)
	}
	lines := keyLines([]byte(text))
	var entries []entry
	for name, v := range doc {
		switch v := v.(type) {
		case map[string]any:
			for key, value := range v {
				entries = append(entries, entry{section: name, key: key, value: plain(value), line: lines[name+"\x00"+key]})
			}
		case []map[string]any, []any:
			if knownSections[name] {
				return nil, diag.Newf("`[[%s]]` is a list of tables, and %s is a section, written once as `[%s]`", name, name, name).
					At(file, lines["\x00"+name], 1).WithSource(text).
					Fix("write it as [" + name + "]")
			}
			// An unknown section, written as a list: it is warned about like any other.
			entries = append(entries, entry{section: name, value: v, line: lines["\x00"+name]})
		default:
			entries = append(entries, entry{key: name, value: v, line: lines["\x00"+name]})
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].line != entries[j].line {
			return entries[i].line < entries[j].line
		}
		return entries[i].section+"."+entries[i].key < entries[j].section+"."+entries[j].key
	})
	for i := range entries {
		if entries[i].line < 1 {
			entries[i].line = 1
		}
	}
	return entries, nil
}

// plain turns a list that holds only texts into a list of texts, which is the one kind
// of list the settings take. Anything else stays as it was read, and its setter says
// what it expected.
func plain(value any) any {
	list, ok := value.([]any)
	if !ok {
		return value
	}
	texts := make([]string, 0, len(list))
	for _, item := range list {
		text, ok := item.(string)
		if !ok {
			return value
		}
		texts = append(texts, text)
	}
	return texts
}

// syntaxProblem is a file that is not TOML. It says where, and what the library
// found, except in the credentials section.
func syntaxProblem(file, text string, err error) error {
	line, column := 1, 1
	message := err.Error()
	var decodeErr *toml.DecodeError
	if errors.As(err, &decodeErr) {
		line, column = decodeErr.Position()
	}
	message = strings.TrimPrefix(message, "toml: ")
	source := text
	if sectionAt(text, line) == "credentials" {
		message = "the value must be the NAME of an environment variable, in quotes, like \"BOB_TOKEN\", not the token itself"
		source, column = maskLine(text, line), 1
	} else {
		message = "this is not TOML I can read: " + message
	}
	return diag.New(message).At(file, line, column).WithSource(source).
		Fix("check the quotes, the commas and the [section] names in metagente.toml")
}

// sectionAt is the section that is open on a line of the text, as far as the headers
// before it say. It is only used to decide whether a line is hidden, so a header it
// cannot read counts as one that might be the credentials.
func sectionAt(text string, line int) string {
	section := ""
	for i, raw := range strings.Split(text, "\n") {
		if i+1 > line {
			break
		}
		trimmed := strings.TrimSpace(raw)
		if !strings.HasPrefix(trimmed, "[") {
			continue
		}
		name := strings.Trim(strings.TrimSpace(strings.SplitN(trimmed, "#", 2)[0]), "[]")
		name = strings.Trim(strings.TrimSpace(name), `"'`)
		section = name
		if strings.Contains(name, "credentials") {
			section = "credentials"
		}
	}
	return section
}

// keyLines finds the line of each setting: "section\x00key", and "\x00name" for a key
// or a table at the top. The file was already read whole, so a problem here only
// means a line is not known, and the first line is said instead.
func keyLines(data []byte) map[string]int {
	lines := map[string]int{}
	var p unstable.Parser
	p.Reset(data)
	section := ""
	for p.NextExpression() {
		node := p.Expression()
		switch node.Kind {
		case unstable.Table, unstable.ArrayTable:
			path := keyPath(node.Key())
			if len(path) == 0 {
				continue
			}
			section = path[0]
			if _, seen := lines["\x00"+section]; !seen {
				lines["\x00"+section] = lineOf(&p, node.Key())
			}
		case unstable.KeyValue:
			path := keyPath(node.Key())
			if len(path) == 0 {
				continue
			}
			line := lineOf(&p, node.Key())
			if section == "" {
				lines["\x00"+path[0]] = line
				if len(path) > 1 {
					lines[path[0]+"\x00"+path[1]] = line
				}
				continue
			}
			lines[section+"\x00"+path[0]] = line
		}
	}
	return lines
}

func keyPath(it unstable.Iterator) []string {
	var path []string
	for it.Next() {
		path = append(path, string(it.Node().Data))
	}
	return path
}

// lineOf is the line of the first part of a key. A key in quotes may not point into
// the file, and then its line is not known.
func lineOf(p *unstable.Parser, it unstable.Iterator) (line int) {
	defer func() {
		if recover() != nil {
			line = 0
		}
	}()
	if !it.Next() {
		return 0
	}
	node := it.Node()
	if node.Raw.Length > 0 {
		return p.Shape(node.Raw).Start.Line
	}
	return p.Shape(p.Range(node.Data)).Start.Line
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
