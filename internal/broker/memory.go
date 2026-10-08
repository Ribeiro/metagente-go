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
	// places remembers, for each durable consumer, what happened to each message.
	places map[string]*place

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
	return &Memory{seen: map[string]seenID{}, now: time.Now, places: map[string]*place{}}
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
	stored := Message{Subject: msg.Subject, ID: msg.ID, Data: append([]byte(nil), msg.Data...), Headers: copyHeaders(msg.Headers)}
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

// SetLimits changes the limits of the stream while it is in use (zero is no limit).
func (m *Memory) SetLimits(maxMessages, maxBytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.MaxMessages, m.MaxBytes = maxMessages, maxBytes
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

func copyHeaders(h map[string]string) map[string]string {
	if h == nil {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}

// place is what a durable consumer remembers about the messages, by their index.
type place struct {
	delivered map[int]int       // deliveries of each message
	finished  map[int]bool      // confirmed, or ended
	waitUntil map[int]time.Time // the confirmation is awaited until then
	notBefore map[int]time.Time // not delivered before then (the delay of a nak)
}

func newPlace() *place {
	return &place{delivered: map[int]int{}, finished: map[int]bool{}, waitUntil: map[int]time.Time{}, notBefore: map[int]time.Time{}}
}

type memConsumer struct {
	m    *Memory
	spec ConsumerSpec
	p    *place
}

// Consume gives the consumer of that name; two with the same name share what they remember.
func (m *Memory) Consume(ctx context.Context, spec ConsumerSpec) (Consumer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !ValidPattern(spec.Subject) {
		return nil, fmt.Errorf("%w: `%s` is not a subject", ErrRefused, spec.Subject)
	}
	if spec.AckWait <= 0 {
		spec.AckWait = 30 * time.Second
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.down {
		return nil, fmt.Errorf("%w: the memory broker is down", ErrUnavailable)
	}
	key := spec.Durable
	if key == "" {
		key = fmt.Sprintf("anonymous-%p", &spec)
	}
	p, ok := m.places[key]
	if !ok {
		p = newPlace()
		m.places[key] = p
	}
	return &memConsumer{m: m, spec: spec, p: p}, nil
}

func (c *memConsumer) Close() error { return nil }

// Fetch gives the messages that are ready: not confirmed, not awaited, not delayed, and not delivered as
// many times as the broker is allowed to (MaxDeliver, which a real broker keeps as well).
func (c *memConsumer) Fetch(ctx context.Context, n int, wait time.Duration) ([]Delivery, error) {
	deadline := time.Now().Add(wait)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		got, err := c.take(n)
		if err != nil || len(got) > 0 || !time.Now().Before(deadline) {
			return got, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func (c *memConsumer) take(n int) ([]Delivery, error) {
	c.m.mu.Lock()
	defer c.m.mu.Unlock()
	if c.m.down {
		return nil, fmt.Errorf("%w: the memory broker is down", ErrUnavailable)
	}
	now := c.m.now()
	waiting := 0
	for i := range c.m.msgs {
		if until, ok := c.p.waitUntil[i]; ok && now.Before(until) && !c.p.finished[i] {
			waiting++
		}
	}
	var out []Delivery
	for i, msg := range c.m.msgs {
		if len(out) >= n || (c.spec.MaxInFlight > 0 && waiting+len(out) >= c.spec.MaxInFlight) {
			break
		}
		switch {
		case !Matches(c.spec.Subject, msg.Subject), c.p.finished[i]:
			continue
		case c.p.waitUntil[i].After(now), c.p.notBefore[i].After(now):
			continue
		case c.spec.MaxDeliver > 0 && c.p.delivered[i] >= c.spec.MaxDeliver:
			continue
		}
		c.p.delivered[i]++
		c.p.waitUntil[i] = now.Add(c.spec.AckWait)
		out = append(out, &memDelivery{c: c, index: i, msg: msg, attempt: c.p.delivered[i]})
	}
	return out, nil
}

type memDelivery struct {
	c       *memConsumer
	index   int
	msg     Message
	attempt int
}

func (d *memDelivery) Message() Message { return d.msg }
func (d *memDelivery) Seq() uint64      { return uint64(d.index) + 1 }
func (d *memDelivery) Attempt() int     { return d.attempt }

func (d *memDelivery) act(ctx context.Context, change func(p *place, now time.Time)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.c.m.mu.Lock()
	defer d.c.m.mu.Unlock()
	if d.c.m.down {
		return fmt.Errorf("%w: the memory broker is down", ErrUnavailable)
	}
	change(d.c.p, d.c.m.now())
	return nil
}

func (d *memDelivery) Ack(ctx context.Context) error {
	return d.act(ctx, func(p *place, _ time.Time) { p.finished[d.index] = true })
}

func (d *memDelivery) Term(ctx context.Context, _ string) error { return d.Ack(ctx) }

func (d *memDelivery) Nak(ctx context.Context, delay time.Duration) error {
	return d.act(ctx, func(p *place, now time.Time) {
		delete(p.waitUntil, d.index)
		p.notBefore[d.index] = now.Add(delay)
	})
}

func (d *memDelivery) InProgress(ctx context.Context) error {
	return d.act(ctx, func(p *place, now time.Time) { p.waitUntil[d.index] = now.Add(d.c.spec.AckWait) })
}
