package consume

import (
	"testing"
	"time"
)

func TestTheBreakerOpensAfterFailuresInARowLetsOneEventTestAndGrowsItsWait(t *testing.T) {
	clock := time.Unix(1000, 0)
	b := newBreaker(Config{BreakerAfter: 3, BreakerWait: 30 * time.Second, BreakerMaxWait: 100 * time.Second})
	b.now = func() time.Time { return clock }

	if n, _ := b.gate(0, 4); n != 4 {
		t.Fatalf("closed: n = %d", n)
	}
	if b.failure() {
		t.Fatal("opened at the first failure")
	}
	if b.failure() {
		t.Fatal("opened at the second failure")
	}
	if !b.failure() {
		t.Fatal("did not open at the third failure")
	}
	if n, wait := b.gate(0, 4); n != 0 || wait != 30*time.Second {
		t.Fatalf("open: n = %d, wait = %s", n, wait)
	}
	if b.failure() {
		t.Error("a failure from before it opened opened it again")
	}

	clock = clock.Add(31 * time.Second)
	if n, _ := b.gate(0, 4); n != 1 {
		t.Fatalf("half open: n = %d, want one event to test", n)
	}
	if n, _ := b.gate(1, 3); n != 0 {
		t.Fatalf("half open with the test running: n = %d", n)
	}
	if !b.failure() || b.currentWait() != 60*time.Second {
		t.Fatalf("the test failed: it should open again for 60 s, not %s", b.currentWait())
	}
	clock = clock.Add(61 * time.Second)
	b.gate(0, 4)
	b.failure()
	if b.currentWait() != 100*time.Second {
		t.Errorf("the wait grows no more than its limit: %s", b.currentWait())
	}

	clock = clock.Add(2 * time.Minute)
	b.success()
	if n, _ := b.gate(0, 4); n != 4 || b.currentWait() != 0 {
		t.Errorf("after a success: n = %d, wait = %s", n, b.currentWait())
	}
	if b.failure() {
		t.Error("the count of failures was not cleared by the success")
	}
}
