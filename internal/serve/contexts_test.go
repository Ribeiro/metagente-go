package serve

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeClock is read by the goroutines the code starts and moved by the test, so
// it has to be safe to use from both.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (f *fakeClock) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fakeClock) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func newContexts(limit int, ttl time.Duration) (*Contexts[string], *fakeClock) {
	clock := &fakeClock{now: time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)}
	c := NewContexts[string](limit, ttl)
	c.now = clock.Now
	return c, clock
}

// req: S7
func TestTheServerIssuesTheIdOfAConversationAndItCannotBeGuessed(t *testing.T) {
	c, _ := newContexts(10, time.Hour)
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		id, err := c.Open("state")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(id, "ctx-") || len(id) != len("ctx-")+32 {
			t.Fatalf("id = %q", id)
		}
		if seen[id] {
			t.Fatal("the same id twice")
		}
		seen[id] = true
	}
}

// req: S7
func TestAnIdTheServerNeverIssuedIsRefused(t *testing.T) {
	c, _ := newContexts(10, time.Hour)
	if _, err := c.Open("mine"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "ctx-00000000000000000000000000000000", "ctx-", "../x", "other-conversation", strings.Repeat("a", 10000)} {
		err := c.With(context.Background(), id, func(string) error { t.Error("ran for an unknown id"); return nil })
		if !errors.Is(err, ErrUnknownContext) {
			t.Errorf("%.20q: err = %v", id, err)
		}
	}
}

// req: S7
func TestEachConversationSeesOnlyItsOwnState(t *testing.T) {
	c, _ := newContexts(10, time.Hour)
	a, _ := c.Open("state of a")
	b, _ := c.Open("state of b")
	var got []string
	for _, id := range []string{a, b} {
		_ = c.With(context.Background(), id, func(v string) error { got = append(got, v); return nil })
	}
	if got[0] != "state of a" || got[1] != "state of b" {
		t.Errorf("got %v", got)
	}
}

// req: S9
func TestAConversationThatIsNotUsedExpiresAndOneThatIsUsedDoesNot(t *testing.T) {
	c, clock := newContexts(10, time.Hour)
	idle, _ := c.Open("idle")
	busy, _ := c.Open("busy")
	for i := 0; i < 5; i++ {
		clock.Advance(30 * time.Minute)
		if err := c.With(context.Background(), busy, func(string) error { return nil }); err != nil {
			t.Fatalf("a conversation in use expired: %v", err)
		}
	}
	if err := c.With(context.Background(), idle, func(string) error { return nil }); !errors.Is(err, ErrUnknownContext) {
		t.Errorf("an idle conversation is still there: %v", err)
	}
	if c.Len() != 2 { // not swept yet, only unusable
		t.Logf("len = %d", c.Len())
	}
	if removed := c.Sweep(); removed != 1 || c.Len() != 1 {
		t.Errorf("swept %d, %d left", removed, c.Len())
	}
}

// req: S9
func TestThereIsALimitOfOpenConversationsAndExpiredOnesMakeRoom(t *testing.T) {
	c, clock := newContexts(3, time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := c.Open("x"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Open("x"); !errors.Is(err, ErrTooManyContexts) {
		t.Fatalf("a fourth conversation: %v", err)
	}
	clock.Advance(2 * time.Hour)
	if _, err := c.Open("x"); err != nil {
		t.Errorf("expired conversations did not make room: %v", err)
	}
	if c.Len() != 1 {
		t.Errorf("len = %d", c.Len())
	}
}

func TestOneConversationDoesOneThingAtATime(t *testing.T) {
	c, _ := newContexts(10, time.Hour)
	id, _ := c.Open("x")
	var inside, worst int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.With(context.Background(), id, func(string) error {
				n := atomic.AddInt32(&inside, 1)
				if n > atomic.LoadInt32(&worst) {
					atomic.StoreInt32(&worst, n)
				}
				time.Sleep(2 * time.Millisecond)
				atomic.AddInt32(&inside, -1)
				return nil
			})
		}()
	}
	wg.Wait()
	if worst != 1 {
		t.Errorf("%d things happened at once in one conversation", worst)
	}
}

func TestWaitingForAConversationEndsWhenTheCallerGivesUp(t *testing.T) {
	c, _ := newContexts(10, time.Hour)
	id, _ := c.Open("x")
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = c.With(context.Background(), id, func(string) error { close(started); <-release; return nil })
	}()
	<-started
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := c.With(ctx, id, func(string) error { t.Error("ran although the caller gave up"); return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v", err)
	}
	close(release)
}

// req: S9
func TestAConversationThatIsInUseIsNeverSweptAwayInTheMiddleOfACall(t *testing.T) {
	c, clock := newContexts(10, time.Minute)
	id, _ := c.Open("x")
	release := make(chan struct{})
	started := make(chan struct{})
	go func() {
		_ = c.With(context.Background(), id, func(string) error { close(started); <-release; return nil })
	}()
	<-started
	waiting := make(chan error, 1)
	go func() {
		waiting <- c.With(context.Background(), id, func(string) error { return nil })
	}()
	time.Sleep(10 * time.Millisecond)
	clock.Advance(time.Hour) // far longer than the time to live
	if removed := c.Sweep(); removed != 0 || c.Len() != 1 {
		t.Errorf("swept %d, %d left: a conversation in use was removed", removed, c.Len())
	}
	close(release)
	if err := <-waiting; err != nil {
		t.Errorf("the one that waited: %v", err)
	}
}

func TestTheJanitorRemovesWhatExpiredUntilItIsStopped(t *testing.T) {
	c, clock := newContexts(10, time.Minute)
	_, _ = c.Open("x")
	clock.Advance(time.Hour)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { c.Run(ctx, 5*time.Millisecond); close(done) }()
	deadline := time.Now().Add(2 * time.Second)
	for c.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if c.Len() != 0 {
		t.Error("the janitor did not remove it")
	}
	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Error("the janitor did not stop")
	}
}

// req: S9
func TestWhatASweptConversationHeldIsLetGo(t *testing.T) {
	c, clock := newContexts(10, time.Minute)
	var let []string
	c.OnRemove = func(id string, value string) { let = append(let, id+"="+value) }
	keep, _ := c.Open("kept")
	gone, _ := c.Open("gone")
	clock.Advance(30 * time.Second)
	_ = c.With(context.Background(), keep, func(string) error { return nil })
	clock.Advance(45 * time.Second)
	if c.Sweep() != 1 || len(let) != 1 || let[0] != gone+"=gone" {
		t.Errorf("let go: %v", let)
	}
}

func TestAConversationThatFailsToStartLeavesNothingBehindAndKeepsNoPlace(t *testing.T) {
	c, _ := newContexts(1, time.Hour)
	if _, err := c.OpenWith(func(string) (string, error) { return "", errors.New("it did not start") }); err == nil {
		t.Fatal("a failure was hidden")
	}
	if c.Len() != 0 {
		t.Errorf("len = %d", c.Len())
	}
	if _, err := c.Open("fits"); err != nil {
		t.Errorf("the failed one kept its place: %v", err)
	}
}

func TestAPlaceIsKeptWhileAConversationIsBeingMade(t *testing.T) {
	c, _ := newContexts(1, time.Hour)
	making := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.OpenWith(func(string) (string, error) { close(making); <-release; return "x", nil })
		done <- err
	}()
	<-making
	if _, err := c.Open("second"); !errors.Is(err, ErrTooManyContexts) {
		t.Errorf("the limit was passed while one was being made: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if c.Len() != 1 {
		t.Errorf("len = %d", c.Len())
	}
}
