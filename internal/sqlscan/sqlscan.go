// Package sqlscan reads the SQL of a named statement: which words are its parameters, and whether it is
// one statement that only reads. The author writes the parameters as :name, and the same way for every
// database; a driver gets them as the signs it understands.
//
// It is not a SQL parser. It tells apart code from text in quotes and from comments, which is all that
// is needed to find the parameters and the end of the statement.
package sqlscan

import (
	"errors"
	"fmt"
	"strings"
)

// MaxBytes is the longest statement that is read.
const MaxBytes = 64 << 10

// Statement is one SQL statement written with :name parameters.
type Statement struct {
	// Text is the statement as written, without the final semicolon.
	Text string
	// Params are the distinct names of the parameters, in the order they are first used.
	Params []string

	uses []use
}

type use struct {
	start, end int
	name       string
}

// Parse reads a statement. It must be one statement that begins with SELECT or WITH.
func Parse(text string) (*Statement, error) {
	if len(text) > MaxBytes {
		return nil, fmt.Errorf("it is longer than %d bytes", MaxBytes)
	}
	s := &Statement{}
	sc := scanner{text: text, s: s}
	if err := sc.run(); err != nil {
		return nil, err
	}
	if sc.first == "" {
		return nil, errors.New("it is empty")
	}
	if sc.first != "select" && sc.first != "with" {
		return nil, fmt.Errorf("it begins with %s, and a statement here has to begin with SELECT or WITH", strings.ToUpper(sc.first))
	}
	end := len(text)
	if sc.semicolon >= 0 {
		end = sc.semicolon
	}
	s.Text = strings.TrimRight(text[:end], " \t\r\n")
	return s, nil
}

// Rewrite returns the statement with each parameter replaced by placeholder(n), where n counts the
// places from 1, and the name of the parameter of each place, in order. A name used twice gives two
// places.
func (s *Statement) Rewrite(placeholder func(n int) string) (string, []string) {
	var out strings.Builder
	order := make([]string, 0, len(s.uses))
	last := 0
	for i, u := range s.uses {
		out.WriteString(s.Text[last:u.start])
		out.WriteString(placeholder(i + 1))
		order = append(order, u.name)
		last = u.end
	}
	out.WriteString(s.Text[last:])
	return out.String(), order
}

type scanner struct {
	text      string
	s         *Statement
	first     string // the first word, in lower case
	semicolon int    // where the final ; is, or -1
	seen      map[string]bool
}

func (sc *scanner) run() error {
	sc.semicolon = -1
	sc.seen = map[string]bool{}
	for i := 0; i < len(sc.text); {
		next, err := sc.step(i)
		if err != nil {
			return err
		}
		i = next
	}
	return nil
}

// step reads what begins at i and returns where the next thing begins.
func (sc *scanner) step(i int) (int, error) {
	if next, done, err := sc.trivia(i); done {
		return next, err
	}
	// Everything else is code, and none may follow the final semicolon.
	if err := sc.noMoreAfterEnd(i); err != nil {
		return 0, err
	}
	t := sc.text
	c := t[i]
	switch {
	case c == '\'' || c == '"' || c == '`':
		end, ok := skipQuoted(t, i)
		if !ok {
			return 0, errors.New("a quote is never closed")
		}
		return end, nil
	case c == ':':
		return sc.colon(i)
	case isIdentStart(c):
		end := i
		for end < len(t) && isIdentChar(t[end]) {
			end++
		}
		if sc.first == "" {
			sc.first = strings.ToLower(t[i:end])
		}
		return end, nil
	}
	return i + 1, nil
}

// trivia reads what is not code: spaces, comments and the semicolon that ends the statement. done says
// whether that is what begins at i.
func (sc *scanner) trivia(i int) (next int, done bool, err error) {
	t := sc.text
	c := t[i]
	switch {
	case c == '-' && i+1 < len(t) && t[i+1] == '-':
		return skipLine(t, i), true, nil
	case c == '/' && i+1 < len(t) && t[i+1] == '*':
		end := strings.Index(t[i+2:], "*/")
		if end < 0 {
			return 0, true, errors.New("a comment is never closed")
		}
		return i + 2 + end + 2, true, nil
	case c == ';':
		if sc.semicolon >= 0 {
			return 0, true, errors.New("it has more than one statement")
		}
		sc.semicolon = i
		return i + 1, true, nil
	case c == ' ' || c == '\t' || c == '\r' || c == '\n':
		return i + 1, true, nil
	}
	return 0, false, nil
}

// noMoreAfterEnd refuses code after the final semicolon: only spaces and comments may follow it.
func (sc *scanner) noMoreAfterEnd(int) error {
	if sc.semicolon >= 0 {
		return errors.New("it has more than one statement")
	}
	return nil
}

// colon reads what begins with a colon: a cast (::), a parameter (:name), or a colon that is just a sign.
func (sc *scanner) colon(i int) (int, error) {
	t := sc.text
	if i+1 < len(t) && t[i+1] == ':' {
		return i + 2, nil
	}
	if i+1 >= len(t) || !isNameStart(t[i+1]) || (i > 0 && isIdentChar(t[i-1])) {
		return i + 1, nil
	}
	end := i + 1
	for end < len(t) && isNameChar(t[end]) {
		end++
	}
	name := t[i+1 : end]
	sc.s.uses = append(sc.s.uses, use{start: i, end: end, name: name})
	if !sc.seen[name] {
		sc.seen[name] = true
		sc.s.Params = append(sc.s.Params, name)
	}
	return end, nil
}

// skipQuoted returns the index after the quoted text that begins at i. A quote doubled inside it is
// part of the text.
func skipQuoted(t string, i int) (int, bool) {
	quote := t[i]
	for j := i + 1; j < len(t); j++ {
		if t[j] != quote {
			continue
		}
		if j+1 < len(t) && t[j+1] == quote {
			j++
			continue
		}
		return j + 1, true
	}
	return 0, false
}

func skipLine(t string, i int) int {
	if end := strings.IndexByte(t[i:], '\n'); end >= 0 {
		return i + end + 1
	}
	return len(t)
}

// isIdentStart and isIdentChar are the letters of words of SQL; a byte of a letter outside ASCII counts.
func isIdentStart(c byte) bool {
	return c == '_' || c >= 0x80 || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentChar(c byte) bool { return isIdentStart(c) || (c >= '0' && c <= '9') || c == '$' }

// isNameStart and isNameChar are the letters of the name of a parameter.
func isNameStart(c byte) bool { return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func isNameChar(c byte) bool { return isNameStart(c) || (c >= '0' && c <= '9') }
