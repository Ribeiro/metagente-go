package serve

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

// Contexts keeps what each conversation holds (its state). The server issues the
// id of a conversation, and a caller cannot name one it was not given, so no
// caller can make up the id of someone else's conversation (requirement S7).
// There is a limit of conversations, and one that is not used expires (S9).
type Contexts[T any] struct {
	room places // one place for each conversation held or being made
	ttl  time.Duration
	now  func() time.Time

	// OnRemove, when set, is called with each conversation that is swept away, so
	// what it holds can be let go. It is called without any lock held.
	OnRemove func(id string, value T)

	mu    sync.Mutex
	items map[string]*entry[T]
}

type entry[T any] struct {
	value T
	last  time.Time
	busy  chan struct{} // holding a place in it means being the one that uses it
}

var (
	// ErrUnknownContext is for an id that was never issued, or has expired.
	ErrUnknownContext = errors.New("unknown conversation")
	// ErrTooManyContexts is for a server that holds all the conversations it may.
	ErrTooManyContexts = errors.New("too many open conversations")
)

// NewContexts makes a registry of at most limit conversations, each forgotten after
// ttl without use.
func NewContexts[T any](limit int, ttl time.Duration) *Contexts[T] {
	return newContextsIn[T](newPlaces(limit), ttl)
}

// newContextsIn makes a registry whose conversations take their places from room,
// which others may share.
func newContextsIn[T any](room places, ttl time.Duration) *Contexts[T] {
	return &Contexts[T]{room: room, ttl: ttl, now: time.Now, items: map[string]*entry[T]{}}
}

// newContextID is 128 random bits.
func newContextID() string {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		panic("the system could not give random bytes")
	}
	return "ctx-" + hex.EncodeToString(raw)
}

// Open starts a conversation that holds the value and returns its id.
func (c *Contexts[T]) Open(value T) (string, error) {
	return c.OpenWith(func(string) (T, error) { return value, nil })
}

// OpenWith starts a conversation whose value is made by build, which is given
// the id the server issued. A place is kept for it while it is being made, so a
// slow build cannot let the limit be passed, and if build fails nothing is kept.
func (c *Contexts[T]) OpenWith(build func(id string) (T, error)) (string, error) {
	if !c.room.take() {
		c.Sweep()
		if !c.room.take() {
			return "", ErrTooManyContexts
		}
	}
	id := newContextID()

	value, err := build(id)

	if err != nil {
		c.room.give()
		return "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[id] = &entry[T]{value: value, last: c.now(), busy: make(chan struct{}, 1)}
	return id, nil
}

// With runs fn with the value of a conversation, and with nobody else using it. If
// the caller gives up while waiting, fn does not run.
func (c *Contexts[T]) With(ctx context.Context, id string, fn func(T) error) error {
	c.mu.Lock()
	e := c.items[id]
	if e == nil || c.expired(e) {
		c.mu.Unlock()
		return ErrUnknownContext
	}
	c.mu.Unlock()

	select {
	case e.busy <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() {
		c.mu.Lock()
		e.last = c.now()
		c.mu.Unlock()
		<-e.busy
	}()

	c.mu.Lock()
	still := c.items[id] == e
	if still {
		e.last = c.now()
	}
	c.mu.Unlock()
	if !still {
		return ErrUnknownContext
	}
	return fn(e.value)
}

func (c *Contexts[T]) expired(e *entry[T]) bool { return c.now().Sub(e.last) > c.ttl }

type removed[T any] struct {
	id    string
	value T
}

// Sweep forgets the conversations that expired and returns how many. One that is
// being used is never forgotten, whatever its age.
func (c *Contexts[T]) Sweep() int {
	c.mu.Lock()
	gone := c.sweepLocked()
	c.mu.Unlock()
	c.release(gone)
	return len(gone)
}

func (c *Contexts[T]) sweepLocked() []removed[T] {
	var gone []removed[T]
	for id, e := range c.items {
		if len(e.busy) == 0 && c.expired(e) {
			delete(c.items, id)
			c.room.give()
			gone = append(gone, removed[T]{id, e.value})
		}
	}
	return gone
}

// release tells the owner of what was swept away, with no lock held.
func (c *Contexts[T]) release(gone []removed[T]) {
	if c.OnRemove == nil {
		return
	}
	for _, r := range gone {
		c.OnRemove(r.id, r.value)
	}
}

// Len is the number of conversations held, expired or not.
func (c *Contexts[T]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}

// Run sweeps every so often until the context ends.
func (c *Contexts[T]) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			c.Sweep()
		case <-ctx.Done():
			return
		}
	}
}

// CloseAll forgets every conversation, in use or not, and lets go of what they
// hold. It is for a server that is stopping.
func (c *Contexts[T]) CloseAll() {
	c.mu.Lock()
	var gone []removed[T]
	for id, e := range c.items {
		gone = append(gone, removed[T]{id, e.value})
		delete(c.items, id)
		c.room.give()
	}
	c.mu.Unlock()
	c.release(gone)
}
