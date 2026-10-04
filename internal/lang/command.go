package lang

import (
	"path/filepath"
	"strings"
	"unicode"
)

// SplitCommand splits a command line into words, honoring simple quotes. It
// does not use a shell: the words are exactly what the program receives.
func SplitCommand(line string) []string {
	var words []string
	var current strings.Builder
	var quote rune
	hasWord := false
	for _, c := range line {
		switch {
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			current.WriteRune(c)
		case c == '"' || c == '\'':
			quote = c
			hasWord = true
		case unicode.IsSpace(c):
			if hasWord || current.Len() > 0 {
				words = append(words, current.String())
				current.Reset()
				hasWord = false
			}
		default:
			current.WriteRune(c)
		}
	}
	if hasWord || current.Len() > 0 {
		words = append(words, current.String())
	}
	return words
}

func isURL(command string) bool {
	return strings.HasPrefix(command, "http://") || strings.HasPrefix(command, "https://")
}

// IsURL tells a tool server that is an address (`from mcp "https://..."`) from
// one that is a program to start.
func IsURL(command string) bool { return isURL(command) }

// unpinnedPackage looks at a package runner command (npx, uvx, pipx run, bunx)
// and returns the package it starts when no version is pinned. It is a
// heuristic: flags that take a value, like `uvx --from pkg cmd`, can fool it,
// and then it stays quiet rather than wrong.
func unpinnedPackage(command string) (spec string, unpinned bool) {
	words := SplitCommand(command)
	if len(words) == 0 {
		return "", false
	}
	program := strings.ToLower(filepath.Base(words[0]))
	program = strings.TrimSuffix(strings.TrimSuffix(program, ".exe"), ".cmd")
	switch program {
	case "npx", "uvx", "bunx", "pipx":
	default:
		return "", false
	}
	args := words[1:]
	if program == "pipx" {
		if len(args) == 0 || args[0] != "run" {
			return "", false
		}
		args = args[1:]
	}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		return arg, !isPinned(arg)
	}
	return "", false
}

// isPinned reports whether a package spec names a version: `pkg@1.2.3`,
// `@scope/pkg@1.2.3` or `pkg==1.2.3`. `@latest` is not a pin.
func isPinned(spec string) bool {
	s := strings.TrimPrefix(spec, "@") // the marker of an npm scope
	if i := strings.Index(s, "=="); i >= 0 {
		return s[i+2:] != ""
	}
	i := strings.LastIndex(s, "@")
	if i < 0 {
		return false
	}
	version := s[i+1:]
	return version != "" && version != "latest"
}
