// Package diag defines the only error shape users ever see: a problem in
// plain words, with the place where it happened and how to fix it.
package diag

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Severity tells a blocking problem from advice.
type Severity int

const (
	// SeverityProblem blocks the run.
	SeverityProblem Severity = iota
	// SeverityWarning is advice. It never blocks, unless strict mode is on.
	SeverityWarning
)

// Diagnostic is a problem in plain words: where it is, what went wrong, and
// how to fix it.
//
// Message and Suggestion must not contain file names or absolute paths: put
// them in Related instead. Public relies on that to keep them away from
// remote callers (requirement P1).
type Diagnostic struct {
	File       string
	Line       int // 1-based; 0 when the place is unknown
	Column     int // 1-based; 0 when unknown
	Message    string
	Suggestion string
	Related    []string
	Source     string // the whole source text, kept to show the offending line
	Severity   Severity
}

// New starts a diagnostic with a message.
func New(message string) *Diagnostic {
	return &Diagnostic{Message: message}
}

// Newf is New with a format string.
func Newf(format string, args ...any) *Diagnostic {
	return New(fmt.Sprintf(format, args...))
}

// At sets the place of the problem.
func (d *Diagnostic) At(file string, line, column int) *Diagnostic {
	d.File, d.Line, d.Column = file, line, column
	return d
}

// WithSource keeps the source text so Render can show the offending line.
func (d *Diagnostic) WithSource(text string) *Diagnostic {
	d.Source = text
	return d
}

// Fix sets the suggested fix.
func (d *Diagnostic) Fix(suggestion string) *Diagnostic {
	d.Suggestion = suggestion
	return d
}

// Fixf is Fix with a format string.
func (d *Diagnostic) Fixf(format string, args ...any) *Diagnostic {
	return d.Fix(fmt.Sprintf(format, args...))
}

// AddRelated adds one line of extra context, for example a problem that
// happened inside a linked agent.
func (d *Diagnostic) AddRelated(line string) *Diagnostic {
	d.Related = append(d.Related, line)
	return d
}

// AsWarning marks the diagnostic as advice instead of a blocking problem.
func (d *Diagnostic) AsWarning() *Diagnostic {
	d.Severity = SeverityWarning
	return d
}

// Located adds a place to a diagnostic that does not have one yet.
func (d *Diagnostic) Located(file string, line, column int, source string) *Diagnostic {
	if d.Line == 0 {
		d.File, d.Line, d.Column = file, line, column
	}
	if d.Source == "" {
		d.Source = source
	}
	return d
}

// Render returns plain text with no colors, suitable for terminals, logs and
// tests.
func (d *Diagnostic) Render() string {
	var b strings.Builder
	label := "Problem"
	if d.Severity == SeverityWarning {
		label = "Warning"
	}
	if d.Line > 0 {
		file := d.File
		if file == "" {
			file = "your file"
		}
		fmt.Fprintf(&b, "%s on line %d of %s: %s\n", label, d.Line, file, d.Message)
		b.WriteString(d.snippet())
	} else {
		fmt.Fprintf(&b, "%s: %s\n", label, d.Message)
	}
	for _, line := range d.Related {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	if d.Suggestion != "" {
		fmt.Fprintf(&b, "Fix: %s\n", d.Suggestion)
	}
	return b.String()
}

// Public returns the short form that is safe to send to remote callers: the
// message and the fix, with no source line, file name, line number or
// related lines (requirement P1). The full form stays on the local machine.
func (d *Diagnostic) Public() string {
	var b strings.Builder
	b.WriteString(d.Message)
	b.WriteString("\n")
	if d.Suggestion != "" {
		fmt.Fprintf(&b, "Fix: %s\n", d.Suggestion)
	}
	return b.String()
}

// snippet draws the offending line with a marker under the place, using
// plain characters.
func (d *Diagnostic) snippet() string {
	if d.Source == "" || d.Line < 1 {
		return ""
	}
	lines := strings.Split(d.Source, "\n")
	if d.Line > len(lines) {
		return ""
	}
	text := strings.TrimRight(lines[d.Line-1], "\r")
	number := strconv.Itoa(d.Line)
	gutter := strings.Repeat(" ", len(number))
	pad := ""
	if d.Column > 1 {
		pad = strings.Repeat(" ", d.Column-1)
	}
	return fmt.Sprintf("  %s | %s\n  %s | %s^\n", number, text, gutter, pad)
}

// Error makes a Diagnostic usable as an error.
func (d *Diagnostic) Error() string {
	return d.Render()
}

// From extracts a Diagnostic from err, if it holds one.
func From(err error) (*Diagnostic, bool) {
	var d *Diagnostic
	if errors.As(err, &d) {
		return d, true
	}
	return nil, false
}

// SortByPlace orders diagnostics by line and column. The order of diagnostics
// with the same place is kept.
func SortByPlace(list []*Diagnostic) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].Line != list[j].Line {
			return list[i].Line < list[j].Line
		}
		return list[i].Column < list[j].Column
	})
}
