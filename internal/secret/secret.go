// Package secret holds the small rules about secrets that more than one part of
// Metagente needs: how to hide one in a text, and what may be the name of the
// variable that holds one.
package secret

import "strings"

// Redact hides secrets in a text. A short secret is left alone, because hiding
// a word that happens to look like it would make messages unreadable.
func Redact(text string, secrets ...string) string {
	for _, s := range secrets {
		if len(s) >= 6 {
			text = strings.ReplaceAll(text, s, "[hidden]")
		}
	}
	return text
}

// ValidEnvName reports whether text can be the name of an environment variable.
// It is how a key written where the name goes is told apart from a name.
func ValidEnvName(text string) bool {
	if text == "" {
		return false
	}
	for i, c := range text {
		letter := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_'
		digit := c >= '0' && c <= '9'
		if !letter && !(digit && i > 0) {
			return false
		}
	}
	return true
}
