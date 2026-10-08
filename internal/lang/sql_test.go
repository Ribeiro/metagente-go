package lang

import (
	"strings"
	"testing"
)

func sqlAgent(decl string) string {
	return "agent Reader\n  goal \"Read orders\"\n  " + decl + "\n  accepts go start\n  on go\n    rows = orders.next_page after: 0 size: 10\n    reply \"ok\"\n"
}

func TestAToolCanBeADatabaseConnection(t *testing.T) {
	agents := mustParse(t, sqlAgent(`tool orders from sql "orders-ro"`))
	var found *ToolDecl
	for _, tool := range agents[0].Tools {
		if tool.Name == "orders" {
			found = tool
		}
	}
	if found == nil || found.Kind != ToolSQL || found.ConnectionName() != "orders-ro" {
		t.Fatalf("tool = %+v", found)
	}
}

func TestTheActionsOfADatabaseToolAreNotCheckedBeforeTheRun(t *testing.T) {
	agents := mustParse(t, sqlAgent(`tool orders from sql "orders-ro"`))
	if res := Check(agents); len(res.Problems) != 0 {
		t.Errorf("problems: %v", res.Problems)
	}
}

func TestTheNameOfAConnectionMustBeWrittenRight(t *testing.T) {
	for decl, want := range map[string]string{
		`tool orders from sql orders`:                            "must be in quotes",
		`tool orders from sql "orders db"`:                       "letters, digits",
		`tool orders from sql "../orders"`:                       "letters, digits",
		`tool orders from sql ""`:                                "letters, digits",
		`tool orders from sql "a" extra`:                         "",
		`tool orders from sql "` + strings.Repeat("a", 65) + `"`: "letters, digits",
	} {
		msg := parseError(t, sqlAgent(decl))
		if want != "" && !strings.Contains(msg, want) {
			t.Errorf("%s: %s", decl, msg)
		}
	}
}

func TestOtherServersAreStillExplainedAsBefore(t *testing.T) {
	msg := parseError(t, sqlAgent(`tool orders from http "x"`))
	if !strings.Contains(msg, "after `from` I expected `mcp`") {
		t.Errorf("message changed: %s", msg)
	}
}
