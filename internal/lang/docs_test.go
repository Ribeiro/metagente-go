package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// docAgents are the blocks of a document that are whole agents: a ```text block whose first line
// begins with `agent `. The other blocks are commands, answers or pieces of an agent.
func docAgents(t *testing.T, file string) []string {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	var blocks []string
	var block []string
	inside := false
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case !inside && strings.TrimSpace(line) == "```text":
			inside, block = true, nil
		case inside && strings.TrimSpace(line) == "```":
			inside = false
			if len(block) > 0 && strings.HasPrefix(block[0], "agent ") {
				blocks = append(blocks, strings.Join(block, "\n")+"\n")
			}
		case inside:
			block = append(block, line)
		}
	}
	return blocks
}

// The documents promise that every agent they show passes `metagente check`.
func TestEveryAgentInTheDocumentsPassesTheChecks(t *testing.T) {
	// How many there are at least, so that a change to how blocks are found cannot skip them all.
	for name, least := range map[string]int{"LANGUAGE.md": 3, "tutorial.md": 8, "tutorial-elt.md": 2} {
		file := filepath.Join("..", "..", "docs", name)
		blocks := docAgents(t, file)
		if len(blocks) < least {
			t.Fatalf("%s: found only %d agents", name, len(blocks))
		}
		for i, text := range blocks {
			agents, err := ParseFile(name, file, text)
			if err != nil {
				t.Errorf("%s, agent %d does not parse:\n%s", name, i+1, errorText(t, err))
				continue
			}
			if res := Check(agents); len(res.Problems) != 0 {
				t.Errorf("%s, agent %d does not pass the checks:\n%s\n%v", name, i+1, text, res.Problems)
			}
		}
	}
}
