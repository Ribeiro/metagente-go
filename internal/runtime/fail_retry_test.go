package runtime

import (
	"strings"
	"testing"
	"time"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

func failSource(line string) string {
	return "agent Worker\n  goal \"Try\"\n  accepts go start\n  on go\n    " + line + "\n"
}

func TestAFailThatMayPassCarriesTheMarkAndTheWait(t *testing.T) {
	for line, want := range map[string]time.Duration{
		`fail "The destination is busy" retry`:                  0,
		`fail "The destination is busy" retry in 60 seconds`:    60 * time.Second,
		`fail "The destination is busy" retry in 0.5 seconds`:   500 * time.Millisecond,
		`fail "The destination is busy" retry in 99999 seconds`: time.Hour,
		`fail "The destination is busy" retry in 0 seconds`:     0,
	} {
		_, err := runSource(t, newRT(t.TempDir(), nil), failSource(line), "go", "start=x")
		r, ok := diag.RetryOf(err)
		if !ok || r.After != want {
			t.Errorf("%s: retry = %v, %v (err %v)", line, r, ok, err)
		}
		if d, _ := diag.From(err); d == nil || d.Message != "The destination is busy" || d.Line != 5 {
			t.Errorf("%s: problem = %+v", line, d)
		}
	}
}

func TestAFailWithoutRetryStaysFinal(t *testing.T) {
	_, err := runSource(t, newRT(t.TempDir(), nil), failSource(`fail "No way"`), "go", "start=x")
	if err == nil || strings.Contains(err.Error(), "may pass") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := diag.RetryOf(err); ok {
		t.Error("a final failure came as one that may pass")
	}
}

func TestAFailureThatMayPassStaysSoWhenALinkedAgentFailsInsideAnother(t *testing.T) {
	dir := t.TempDir()
	source := `agent Caller
  goal "Ask"
  link Busy from "busy.ag"
  accepts go start
  on go
    reply Busy.ask start: start
`
	busy := "agent Busy\n  goal \"Fails\"\n  accepts ask start\n  on ask\n    fail \"The destination is busy\" retry in 30 seconds\n"
	put(t, dir, "busy.ag", busy)
	_, err := runSource(t, newRT(dir, nil), source, "go", "start=x")
	r, ok := diag.RetryOf(err)
	if !ok || r.After != 30*time.Second {
		t.Fatalf("retry = %v, %v (err %v)", r, ok, err)
	}
	if strings.Count(err.Error(), "This may pass") != 1 {
		t.Errorf("the note is not said once:\n%s", err)
	}
}
