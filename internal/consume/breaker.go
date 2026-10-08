package consume

import (
	"sync"
	"time"
)

// breaker is the circuit breaker of section 8: when failures that may pass come one after the other, it stops
// the taking of events for a while, then lets one through to see whether the destination is back.
type breaker struct {
	mu        sync.Mutex
	after     int
	first     time.Duration
	most      time.Duration
	fails     int
	wait      time.Duration
	openUntil time.Time
	open      bool
	now       func() time.Time
}

func newBreaker(cfg Config) *breaker {
	return &breaker{after: cfg.BreakerAfter, first: cfg.BreakerWait, most: cfg.BreakerMaxWait, now: time.Now}
}

// gate says how many events may be taken now, given those being worked on and the free places, and, when none
// may, how long to wait before asking again. When the wait is over the breaker lets one event through, and
// nothing else until that one is answered.
func (b *breaker) gate(running, slots int) (n int, wait time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.open {
		return slots, 0
	}
	if left := b.openUntil.Sub(b.now()); left > 0 {
		return 0, left
	}
	if running > 0 {
		return 0, time.Second // the one that was let through has not answered yet
	}
	return 1, 0
}

// failure counts a failure that may pass, and says whether the breaker has just opened (or opened again).
func (b *breaker) failure() (opened bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails++
	if b.fails < b.after && !b.open {
		return false
	}
	if b.open && b.now().Before(b.openUntil) {
		return false // another failure from the time before it opened
	}
	switch {
	case b.wait == 0:
		b.wait = b.first
	default:
		b.wait = min(b.wait*2, b.most)
	}
	b.open = true
	b.openUntil = b.now().Add(b.wait)
	return true
}

// success closes the breaker.
func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails, b.wait, b.open = 0, 0, false
}

func (b *breaker) currentWait() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.wait
}
