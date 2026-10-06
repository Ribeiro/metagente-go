package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/lang"
)

func TestAgentName(t *testing.T) {
	tests := map[string]string{
		"hello":      "Hello",
		"my agent!":  "Myagent",
		"snake_case": "Snake_case",
		"été":        "Été",
		"123":        "Helper",
		"_x":         "Helper",
		"":           "Helper",
		"!!!":        "Helper",
	}
	for input, want := range tests {
		if got := agentName(input); got != want {
			t.Errorf("agentName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestCreateWritesAnAgentThatPassesTheChecksAndAConfiguration(t *testing.T) {
	dir := t.TempDir()
	messages, err := Create(dir, "hello")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Created hello.ag", "Created metagente.toml", "Try it: metagente run hello.ag greet name=World"}
	if strings.Join(messages, "|") != strings.Join(want, "|") {
		t.Errorf("messages = %q, want %q", messages, want)
	}

	text, err := os.ReadFile(filepath.Join(dir, "hello.ag"))
	if err != nil {
		t.Fatal(err)
	}
	agents, err := lang.ParseFile("hello.ag", "", string(text))
	if err != nil {
		t.Fatalf("the starter agent does not parse: %v", err)
	}
	if res := lang.Check(agents); len(res.Problems) != 0 || len(res.Warnings) != 0 {
		t.Errorf("the starter agent should be clean: %v %v", res.Problems, res.Warnings)
	}
	if _, err := os.Stat(filepath.Join(dir, "metagente.toml")); err != nil {
		t.Errorf("metagente.toml was not created: %v", err)
	}
}

func TestCreateRefusesToOverwriteAndKeepsAnExistingConfiguration(t *testing.T) {
	dir := t.TempDir()
	custom := "# mine\n"
	if err := os.WriteFile(filepath.Join(dir, "metagente.toml"), []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}
	messages, err := Create(dir, "hello")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range messages {
		if strings.Contains(m, "Created metagente.toml") {
			t.Errorf("an existing metagente.toml must be left alone, got %q", messages)
		}
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "metagente.toml")); string(got) != custom {
		t.Errorf("metagente.toml was changed: %q", got)
	}

	_, err = Create(dir, "hello")
	if err == nil || !strings.Contains(err.Error(), "hello.ag already exists") {
		t.Errorf("a second `new hello` should be refused, got %v", err)
	}
}
