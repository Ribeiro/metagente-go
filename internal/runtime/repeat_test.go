package runtime

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/tools"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// pages serves a list of pages the way many APIs do: each one says where the next is, and the last
// one says null. It counts the requests.
func pages(t *testing.T, n int) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var page int
		fmt.Sscanf(r.URL.Path, "/page/%d", &page)
		next := "null"
		if page < n {
			next = fmt.Sprintf("%q", fmt.Sprintf("%s/page/%d", server.URL, page+1))
		}
		fmt.Fprintf(w, `{"page":%d,"next":%s}`, page, next)
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func repeatSource(lines string) string {
	return "agent Pager\n  goal \"Read pages\"\n  tool http allow private\n  accepts go start\n  on go\n" + lines
}

func TestRepeatWhileReadsEveryPageOfAnApi(t *testing.T) {
	server, hits := pages(t, 4)
	source := repeatSource("    url = start\n    last = nothing\n    repeat while url is not nothing\n" +
		"      page = http.get url: url\n      last = page.json.page\n      url = page.json.next\n    reply \"read until page {last}\"\n")
	got, err := runSource(t, newRT(t.TempDir(), nil), source, "go", "start="+server.URL+"/page/1")
	if err != nil || got.Text != "read until page 4" {
		t.Fatalf("got %q, %v", got.Display(), err)
	}
	if hits.Load() != 4 {
		t.Errorf("%d requests, want 4", hits.Load())
	}
}

func TestRepeatWhileDoesNotRunItsLinesWhenTheConditionStartsFalse(t *testing.T) {
	server, hits := pages(t, 1)
	source := repeatSource("    repeat while no\n      http.get url: start\n    reply \"after\"\n")
	got, err := runSource(t, newRT(t.TempDir(), nil), source, "go", "start="+server.URL+"/page/1")
	if err != nil || got.Text != "after" || hits.Load() != 0 {
		t.Errorf("got %q, %v, %d requests", got.Display(), err, hits.Load())
	}
}

func TestUpToNTimesEndsTheLoopAndTheLinesAfterItRun(t *testing.T) {
	server, hits := pages(t, 100)
	source := repeatSource("    repeat while yes up to 3 times\n      http.get url: start\n    reply \"after\"\n")
	got, err := runSource(t, newRT(t.TempDir(), nil), source, "go", "start="+server.URL+"/page/1")
	if err != nil || got.Text != "after" {
		t.Fatalf("got %q, %v", got.Display(), err)
	}
	if hits.Load() != 3 {
		t.Errorf("%d turns, want 3", hits.Load())
	}
}

func TestTheConditionIsNotLookedAtAgainAfterTheLastTurnOfALimit(t *testing.T) {
	// The condition is a call: it must not be made a fourth time after `up to 3 times`.
	server, hits := pages(t, 100)
	source := repeatSource("    repeat while http.get url: start up to 3 times\n      seen = 1\n    reply \"after\"\n")
	got, err := runSource(t, newRT(t.TempDir(), nil), source, "go", "start="+server.URL+"/page/1")
	if err != nil || got.Text != "after" {
		t.Fatalf("got %q, %v", got.Display(), err)
	}
	// Three turns, and the condition (a call) is looked at before each of them.
	if hits.Load() != 3 {
		t.Errorf("%d calls of the condition, want 3", hits.Load())
	}
}

func TestALoopThatNeverEndsStopsAtTheCeilingWithAProblem(t *testing.T) {
	server, hits := pages(t, 100)
	rt := newRT(t.TempDir(), func(c *config.Config) { c.Runtime.MaxLoopTurns = 5 })
	source := repeatSource("    repeat while yes\n      http.get url: start\n    reply \"never\"\n")
	_, err := runSource(t, rt, source, "go", "start="+server.URL+"/page/1")
	text := errText(t, err)
	mustContain(t, text, "line 6", "this `repeat` took 5 turns and its condition is still true", "max_loop_turns")
	if hits.Load() != 5 {
		t.Errorf("%d turns, want 5", hits.Load())
	}
}

func TestALoopThatEndsExactlyAtTheCeilingIsNotAProblem(t *testing.T) {
	server, hits := pages(t, 5)
	rt := newRT(t.TempDir(), func(c *config.Config) { c.Runtime.MaxLoopTurns = 5 })
	source := repeatSource("    url = start\n    repeat while url is not nothing\n      page = http.get url: url\n      url = page.json.next\n    reply \"done\"\n")
	got, err := runSource(t, rt, source, "go", "start="+server.URL+"/page/1")
	if err != nil || got.Text != "done" || hits.Load() != 5 {
		t.Errorf("got %q, %v, %d requests", got.Display(), err, hits.Load())
	}
}

func TestALimitAboveTheCeilingDoesNotRaiseIt(t *testing.T) {
	server, hits := pages(t, 100)
	rt := newRT(t.TempDir(), func(c *config.Config) { c.Runtime.MaxLoopTurns = 4 })
	source := repeatSource("    repeat while yes up to 50 times\n      http.get url: start\n")
	_, err := runSource(t, rt, source, "go", "start="+server.URL+"/page/1")
	mustContain(t, errText(t, err), "`up to 50 times` is more than the 4 turns this setup allows", "max_loop_turns")
	if hits.Load() != 4 {
		t.Errorf("%d turns, want 4", hits.Load())
	}
}

func TestAReplyInsideARepeatEndsTheHandler(t *testing.T) {
	source := repeatSource("    repeat while yes\n      reply \"out\"\n    reply \"never\"\n")
	got, err := runSource(t, newRT(t.TempDir(), nil), source, "go", "start=x")
	if err != nil || got.Text != "out" {
		t.Errorf("got %q, %v", got.Display(), err)
	}
}

func TestARepeatThatIsStoppedBetweenTurnsSaysSo(t *testing.T) {
	rt := newRT(t.TempDir(), nil)
	agent := agentFrom(t, rt, repeatSource("    repeat while yes\n      x = 1\n"), "c")
	args := tools.Args{"start": value.Text("x")}

	stopped, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := agent.Handle(stopped, &Call{TaskID: "t"}, "go", args)
	mustContain(t, errText(t, err), "the run was stopped before this loop finished")

	late, cancelLate := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelLate()
	_, err = agent.Handle(late, &Call{TaskID: "t"}, "go", args)
	mustContain(t, errText(t, err), "the time for this run ended before the loop finished")
}

func TestAnErrorInsideARepeatIsToldWithItsLine(t *testing.T) {
	source := repeatSource("    repeat while yes\n      page = http.get url: start\n      oops = page.json.missing\n")
	server, _ := pages(t, 1)
	_, err := runSource(t, newRT(t.TempDir(), nil), source, "go", "start="+server.URL+"/page/1")
	mustContain(t, errText(t, err), "line 8")
	if strings.Contains(errText(t, err), "took") {
		t.Errorf("the ceiling was blamed for another problem:\n%s", errText(t, err))
	}
}
