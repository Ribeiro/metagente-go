package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const examplesDir = "../../testdata/examples"

func exampleFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(examplesDir, "*.ag"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no example agents found in %s: %v", examplesDir, err)
	}
	return files
}

func readExample(t *testing.T, file string) string {
	t.Helper()
	text, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func TestValidExamplesPassTheChecks(t *testing.T) {
	count := 0
	for _, file := range exampleFiles(t) {
		name := filepath.Base(file)
		if strings.HasPrefix(name, "broken_") {
			continue
		}
		agents, err := ParseFile(name, file, readExample(t, file))
		if err != nil {
			t.Errorf("%s should parse:\n%s", name, errorText(t, err))
			continue
		}
		if res := Check(agents); len(res.Problems) != 0 {
			t.Errorf("%s should pass the checks: %v", name, res.Problems)
		}
		count++
	}
	if count < 6 {
		t.Errorf("expected the beginner examples to be there, found %d", count)
	}
}

func TestBrokenExamplesFailWithAPlainMessage(t *testing.T) {
	// A syntax error stops the parser; an undeclared tool is found by the checks.
	syntax := filepath.Join(examplesDir, "broken_syntax.ag")
	_, err := ParseFile("broken_syntax.ag", syntax, readExample(t, syntax))
	if err == nil {
		t.Fatal("broken_syntax.ag should not parse")
	}
	text := errorText(t, err)
	assertContains(t, text, "Problem", "Fix:", "did you mean `goal`?")
	assertPlain(t, text)

	undeclared := filepath.Join(examplesDir, "broken_undeclared_tool.ag")
	agents, err := ParseFile("broken_undeclared_tool.ag", undeclared, readExample(t, undeclared))
	if err != nil {
		t.Fatal(errorText(t, err))
	}
	res := Check(agents)
	if len(res.Problems) == 0 {
		t.Fatal("broken_undeclared_tool.ag should fail the checks")
	}
	assertContains(t, located(t, res.Problems[0]), "never declared it")
}

// broken_permission.ag is only broken when it runs: reading outside the folder
// of `tool file "data/"` is refused by the tool, not by the checks. The test
// that runs it comes with the interpreter.
func TestBrokenPermissionExamplePassesTheStaticChecks(t *testing.T) {
	file := filepath.Join(examplesDir, "broken_permission.ag")
	agents, err := ParseFile("broken_permission.ag", file, readExample(t, file))
	if err != nil {
		t.Fatal(errorText(t, err))
	}
	if res := Check(agents); len(res.Problems) != 0 {
		t.Errorf("unexpected problems: %v", res.Problems)
	}
}

func TestTheReferenceWeatherAgentHasFewerThanTenLines(t *testing.T) {
	text := readExample(t, filepath.Join(examplesDir, "weather.ag"))
	lines := len(strings.Split(strings.TrimRight(text, "\n"), "\n"))
	if lines >= 10 {
		t.Errorf("examples/weather.ag has %d lines", lines)
	}
}
