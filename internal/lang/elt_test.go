package lang

import (
	"strings"
	"testing"
)

func eltAgent(decl string) string {
	return "agent Extractor\n  goal \"Copy a table\"\n  " + decl + "\n"
}

func TestAToolCanBeAnExtractorOrAWorkerThatADescriptionMakes(t *testing.T) {
	for role := range map[string]bool{"extract": true, "load": true} {
		agents := mustParse(t, eltAgent(`tool orders from elt "orders-2" `+role))
		tool := agents[0].FindTool("orders")
		if tool == nil || tool.Kind != ToolELT || tool.ConnectionName() != "orders-2" || tool.ELTRole() != role {
			t.Fatalf("%s: tool = %+v", role, tool)
		}
		// Such an agent is whole before the description gives it its messages: the checks have nothing to say.
		if res := Check(agents); len(res.Problems) != 0 {
			t.Errorf("%s: problems: %v", role, res.Problems)
		}
	}
}

func TestAMistakeInADescriptionToolIsExplained(t *testing.T) {
	for decl, want := range map[string]string{
		`tool orders from elt orders extract`:     "must be in quotes",
		`tool orders from elt "a b" extract`:      "letters, digits",
		`tool orders from elt "orders"`:           "`extract` (the Extractor) or `load` (the Worker)",
		`tool orders from elt "orders" copy`:      "`extract` (the Extractor) or `load` (the Worker)",
		`tool orders from elt "orders" extract 1`: "",
	} {
		_, err := ParseFile("a.ag", "", eltAgent(decl))
		if err == nil {
			t.Errorf("%s: no problem was found", decl)
			continue
		}
		if got := errorText(t, err); !strings.Contains(got, want) {
			t.Errorf("%s: the problem is\n%s\nwant %q", decl, got, want)
		}
	}
}
