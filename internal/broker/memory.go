package broker

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DefaultWindow is how long the memory broker remembers the id of a message to drop its copies, as the
// default window of duplicates of a JetStream stream does.
const DefaultWindow = 2 * time.Minute

// Memory is a broker that lives in the memory of the process: one stream that takes every subject. It
// serves the tests, and the trying of agents that publish and consume in the same process. Nothing in it
// survives the process.
type Memory struct {
	mu   sync.Mutex
	msgs []Message
	seen map[string]seenID
	size int
	down bool
	now  func() time.Time

	// MaxMessages and MaxBytes make the stream refuse what comes after (the policy "discard new"); zero is
	// no limit.
	MaxMessages int
	MaxBytes    int
	// Window is how long an id is remembered; zero means DefaultWindow.
	Window time.Duration
}

type seenID struct {
	seq uint64
	at  time.Time
}

// NewMemory creates an empty broker.
func NewMemory() *Memory {
	return &Memory{seen: map[string]seenID{}, now: time.Now}
}

// Publish keeps the message, unless its id was seen inside the window, the stream is full, or the broker is
// down.
func (m *Memory) Publish(ctx context.Context, msg Message) (PubAck, error) {
	if err := ctx.Err(); err != nil {
		return PubAck{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return PubAck{}, fmt.Errorf("%w: the memory broker is down", ErrUnavailable)
	}
	now := m.now()
	window := m.Window
	if window == 0 {
		window = DefaultWindow
	}
	if msg.ID != "" {
		if first, ok := m.seen[msg.ID]; ok && now.Sub(first.at) < window {
			return PubAck{Stream: "memory", Seq: first.seq, Duplicate: true}, nil
		}
	}
	if m.MaxMessages > 0 && len(m.msgs) >= m.MaxMessages {
		return PubAck{}, fmt.Errorf("%w: it holds %d messages", ErrFull, len(m.msgs))
	}
	if m.MaxBytes > 0 && m.size+len(msg.Data) > m.MaxBytes {
		return PubAck{}, fmt.Errorf("%w: it holds %d bytes", ErrFull, m.size)
	}
	stored := Message{Subject: msg.Subject, ID: msg.ID, Data: append([]byte(nil), msg.Data...)}
	m.msgs = append(m.msgs, stored)
	m.size += len(stored.Data)
	seq := uint64(len(m.msgs))
	if msg.ID != "" {
		m.seen[msg.ID] = seenID{seq: seq, at: now}
	}
	return PubAck{Stream: "memory", Seq: seq}, nil
}

// Close does nothing: the messages stay for whoever looks at them.
func (m *Memory) Close() error { return nil }

// Messages are the messages kept, in order.
func (m *Memory) Messages() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.msgs...)
}

// SetDown makes the broker refuse everything as if it could not be reached, or work again.
func (m *Memory) SetDown(down bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.down = down
}

// Advance moves the clock of the window of duplicates, for the tests.
func (m *Memory) Advance(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	was := m.now
	m.now = func() time.Time { return was().Add(d) }
}
