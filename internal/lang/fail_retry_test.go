package lang

import (
	"strings"
	"testing"
)

func failAgent(line string) string {
	return "agent Worker\n  goal \"Try\"\n  accepts go start\n  on go\n    " + line + "\n"
}

func firstFailRetry(t *testing.T, line string) *FailRetryStmt {
	t.Helper()
	agents := mustParse(t, failAgent(line))
	for _, stmt := range agents[0].Handlers[0].Body {
		if f, ok := stmt.(*FailRetryStmt); ok {
			return f
		}
	}
	t.Fatalf("no fail with retry in: %s", line)
	return nil
}

func TestFailMayAskForAnotherTryWithAnOptionalWait(t *testing.T) {
	plain := firstFailRetry(t, `fail "The destination is busy" retry`)
	if plain.HasAfter {
		t.Errorf("a wait that was not written: %+v", plain)
	}
	waits := firstFailRetry(t, `fail "The destination is busy" retry in 60 seconds`)
	if !waits.HasAfter || waits.After != 60 {
		t.Errorf("wait = %+v", waits)
	}
	if one := firstFailRetry(t, `fail "x" retry in 1 second`); one.After != 1 {
		t.Errorf("one second = %+v", one)
	}
}

func TestAFailWithoutRetryIsStillFinalAndParsesAsBefore(t *testing.T) {
	agents := mustParse(t, failAgent(`fail "No way"`))
	if _, ok := agents[0].Handlers[0].Body[0].(*FailStmt); !ok {
		t.Errorf("statement = %T", agents[0].Handlers[0].Body[0])
	}
}

func TestRetryIsAWordOnlyRightAfterTheValueOfAFail(t *testing.T) {
	source := "agent Worker\n  goal \"Try\"\n  accepts go start\n  on go\n    retry = 3\n    fail retry\n"
	agents := mustParse(t, source)
	body := agents[0].Handlers[0].Body
	if _, ok := body[0].(*AssignStmt); !ok {
		t.Errorf("first = %T", body[0])
	}
	if _, ok := body[1].(*FailStmt); !ok {
		t.Errorf("second = %T", body[1])
	}
}

func TestAMistakeInRetryIsExplained(t *testing.T) {
	for line, want := range map[string]string{
		`fail "x" retry in`:             "needs a number of seconds",
		`fail "x" retry in soon`:        "needs a number of seconds",
		`fail "x" retry in 60`:          "I expected `seconds`",
		`fail "x" retry in 60 minutes`:  "I expected `seconds`",
		`fail "x" retry later`:          "",
		`fail "x" retry in 5 seconds 7`: "",
		`reply "x" retry`:               "",
	} {
		msg := parseError(t, failAgent(line))
		if want != "" && !strings.Contains(msg, want) {
			t.Errorf("%s: %s", line, msg)
		}
	}
}

func TestAWaitThatCannotBeMeantIsWarnedAbout(t *testing.T) {
	for line, want := range map[string]string{
		`fail "x" retry in 0 seconds`:     "not a positive number",
		`fail "x" retry in 99999 seconds`: "longer than the 3600 seconds",
	} {
		res := Check(mustParse(t, failAgent(line)))
		if len(res.Problems) != 0 || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Message, want) {
			t.Errorf("%s: %+v", line, res)
		}
	}
	if res := Check(mustParse(t, failAgent(`fail "x" retry in 60 seconds`))); len(res.Warnings) != 0 {
		t.Errorf("a good wait was warned about: %+v", res.Warnings)
	}
}

func TestTheValueOfAFailWithRetryIsChecked(t *testing.T) {
	res := Check(mustParse(t, failAgent(`fail "x {nobody}" retry`)))
	if len(res.Problems) == 0 {
		t.Error("a name that does not exist went unnoticed")
	}
}
