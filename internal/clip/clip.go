// Package clip shortens a text that came from outside before a message repeats it.
//
// Such a text may be very long, may carry control characters that a terminal would
// obey (a sequence that starts with ESC can move the cursor or change what is
// shown), and may be cut in the middle of a character. The three functions here are
// the one place where that is dealt with.
package clip

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

const ellipsis = "..."

// Cut returns the text if it fits in limit bytes. Otherwise it returns its start, cut on
// the border of a character and followed by "...", so the result is valid UTF-8 if the
// text was.
func Cut(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	if limit < 0 {
		limit = 0
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit-- // do not end in the middle of a character
	}
	return text[:limit] + ellipsis
}

// Collapse makes a text fit in one line of at most limit bytes: every control character
// and every run of spaces and line breaks becomes a single space, and what is left is cut.
func Collapse(text string, limit int) string {
	return Cut(strings.Join(strings.Fields(neutral(text, false)), " "), limit)
}

// Lines cuts a text of several lines and keeps its lines. A control character other than
// the line break and the tab becomes a space.
func Lines(text string, limit int) string {
	return Cut(neutral(text, true), limit)
}

// neutral replaces the control characters that are not wanted by a space. A byte that is
// not valid UTF-8 comes out as the replacement character, so the result is always valid.
func neutral(text string, keepLines bool) string {
	return strings.Map(func(r rune) rune {
		if keepLines && (r == '\n' || r == '\t') {
			return r
		}
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, text)
}
