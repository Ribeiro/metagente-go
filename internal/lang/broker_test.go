package lang

import (
	"strings"
	"testing"
)

func brokerAgent(decl string) string {
	return "agent Sender\n  goal \"Send\"\n  " + decl + "\n  accepts go start\n  on go\n    events.publish subject: \"etl.orders.batch\" id: \"j:1\" data: start\n    reply \"ok\"\n"
}

func TestAToolCanBeABrokerWithTheSubjectsItMayPublishTo(t *testing.T) {
	agents := mustParse(t, brokerAgent(`tool events from broker "main" publish "etl.orders.batch" "etl.orders.control"`))
	var found *ToolDecl
	for _, tool := range agents[0].Tools {
		if tool.Name == "events" {
			found = tool
		}
	}
	if found == nil || found.Kind != ToolBroker || found.ConnectionName() != "main" ||
		strings.Join(found.Allow, " ") != "etl.orders.batch etl.orders.control" {
		t.Fatalf("tool = %+v", found)
	}
	if res := Check(agents); len(res.Problems) != 0 {
		t.Errorf("problems: %v", res.Problems)
	}
}

func TestPublishClausesAddUpAndWildcardsAreAccepted(t *testing.T) {
	agents := mustParse(t, brokerAgent(`tool events from broker "main" publish "etl.*.batch" publish "audit.>"`))
	if got := strings.Join(agents[0].Tools[0].Allow, " "); got != "etl.*.batch audit.>" {
		t.Errorf("subjects = %q", got)
	}
}

func TestAMistakeInABrokerDeclarationIsExplained(t *testing.T) {
	for decl, want := range map[string]string{
		`tool events from broker main publish "a.b"`:            "must be in quotes",
		`tool events from broker "a b" publish "a.b"`:           "letters, digits",
		`tool events from broker "main"`:                        "needs `publish`",
		`tool events from broker "main" publish`:                "needs the subjects",
		`tool events from broker "main" publish "a..b"`:         "is not a subject",
		`tool events from broker "main" publish "a.>.b"`:        "is not a subject",
		`tool events from broker "main" publish "a b"`:          "is not a subject",
		`tool events from broker "main" publish "a.b" readonly`: "",
		`tool events from broker "main" publish a.b`:            "",
	} {
		msg := parseError(t, brokerAgent(decl))
		if want != "" && !strings.Contains(msg, want) {
			t.Errorf("%s: %s", decl, msg)
		}
	}
}

func TestTheActionsOfABrokerAreCheckedBeforeTheRun(t *testing.T) {
	source := "agent Sender\n  goal \"Send\"\n  tool events from broker \"main\" publish \"a.b\"\n  accepts go start\n  on go\n    events.send subject: \"a.b\"\n    events.publish subject: \"a.b\" id: \"x\"\n    reply \"ok\"\n"
	res := Check(mustParse(t, source))
	var all []string
	for _, p := range res.Problems {
		all = append(all, p.Message)
	}
	joined := strings.Join(all, "\n")
	if !strings.Contains(joined, "`events` has no action called `send`") || !strings.Contains(joined, "data") {
		t.Errorf("problems: %v", all)
	}
}

func TestASubjectWrittenInTheCallMustBeOneTheToolDeclared(t *testing.T) {
	source := func(subject string) string {
		return "agent Sender\n  goal \"Send\"\n  tool events from broker \"main\" publish \"etl.orders.*\"\n  accepts go job\n  on go\n    events.publish subject: " +
			subject + " id: \"x\" data: \"y\"\n    reply \"ok\"\n"
	}
	if res := Check(mustParse(t, source(`"etl.orders.batch"`))); len(res.Problems) != 0 {
		t.Errorf("a subject that is allowed: %v", res.Problems)
	}
	if res := Check(mustParse(t, source(`"etl.{job}.batch"`))); len(res.Problems) != 0 {
		t.Errorf("a subject made of values is left to the run: %v", res.Problems)
	}
	res := Check(mustParse(t, source(`"etl.customers.batch"`)))
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0].Message, "may not publish to `etl.customers.batch`") ||
		!strings.Contains(res.Problems[0].Suggestion, "etl.orders.*") {
		t.Errorf("problems: %v", res.Problems)
	}
}
