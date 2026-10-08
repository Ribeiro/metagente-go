package consume

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/runtime"
)

// worker answers 200 for a destination that is well, and fails in the ways an agent can.
const workerSource = `agent Worker
  goal "Load a batch"
  tool http allow private
  accepts batch url
  accepts bad
  accepts many items
  on batch
    answer = http.get url: url
    if answer.status is 200
      reply "loaded"
    otherwise
      fail "the destination is busy" retry
  on bad
    fail "a rule of the business stopped this batch"
  on many
    reply "ok"
`

type harness struct {
	t       *testing.T
	mem     *broker.Memory
	def     *lang.AgentDef
	rt      *runtime.Runtime
	mu      sync.Mutex
	logs    []string
	healthy atomic.Bool
	hits    atomic.Int64
	server  *httptest.Server
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, mem: broker.NewMemory()}
	h.healthy.Store(true)
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.hits.Add(1)
		if h.healthy.Load() {
			fmt.Fprint(w, "fine")
			return
		}
		http.Error(w, "busy", http.StatusServiceUnavailable)
	}))
	t.Cleanup(h.server.Close)
	agents, err := lang.ParseFile("worker.ag", "", workerSource)
	if err != nil {
		t.Fatal(err)
	}
	h.def = agents[0]
	cfg := config.Default()
	cfg.Root = t.TempDir()
	h.rt = runtime.New(cfg)
	t.Cleanup(func() { _ = h.rt.Close() })
	return h
}

func (h *harness) config() Config {
	return Config{
		Runtime: h.rt, Agent: h.def, Message: "batch", Broker: h.mem,
		Spec:    broker.ConsumerSpec{Durable: "worker", Subject: "etl.orders.batch", AckWait: 5 * time.Second},
		Dead:    "etl.orders.dead",
		Backoff: []time.Duration{5 * time.Millisecond}, BreakerAfter: 100, BreakerWait: 20 * time.Millisecond,
		FetchWait: 20 * time.Millisecond, IdleExit: 150 * time.Millisecond,
		Log: func(line string) { h.mu.Lock(); h.logs = append(h.logs, line); h.mu.Unlock() },
	}
}

func (h *harness) log() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.logs, "\n")
}

func (h *harness) publish(subject, id, data string) {
	h.t.Helper()
	if _, err := h.mem.Publish(context.Background(), broker.Message{Subject: subject, ID: id, Data: []byte(data)}); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) batch(id string) {
	h.publish("etl.orders.batch", id, fmt.Sprintf(`{"url":%q}`, h.server.URL))
}

func (h *harness) dead() []broker.Message {
	var out []broker.Message
	for _, m := range h.mem.Messages() {
		if m.Subject == "etl.orders.dead" {
			out = append(out, m)
		}
	}
	return out
}

func consumeAll(t *testing.T, cfg Config) Stats {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	stats, err := Run(ctx, cfg)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return stats
}

func TestAnEventTheAgentRepliesToIsConfirmedAndNotDeliveredAgain(t *testing.T) {
	h := newHarness(t)
	h.batch("job:1")
	h.batch("job:2")
	stats := consumeAll(t, h.config())
	if stats.Taken != 2 || stats.Done != 2 || stats.Dead != 0 || stats.Retried != 0 || h.hits.Load() != 2 {
		t.Errorf("stats = %+v, hits = %d", stats, h.hits.Load())
	}
	if len(h.dead()) != 0 {
		t.Errorf("dead letters: %v", h.dead())
	}
	again := consumeAll(t, h.config())
	if again.Taken != 0 {
		t.Errorf("a confirmed event came again: %+v", again)
	}
}

func TestAFailureThatMayPassIsAskedForAgainAfterAWaitAndThenWorks(t *testing.T) {
	h := newHarness(t)
	h.healthy.Store(false)
	h.batch("job:1")
	cfg := h.config()
	cfg.Backoff = []time.Duration{30 * time.Millisecond}
	go func() { time.Sleep(50 * time.Millisecond); h.healthy.Store(true) }()
	stats := consumeAll(t, cfg)
	if stats.Done != 1 || stats.Retried < 1 || stats.Dead != 0 {
		t.Fatalf("stats = %+v\n%s", stats, h.log())
	}
	if !strings.Contains(h.log(), "failed, may pass") || !strings.Contains(h.log(), "done in") {
		t.Errorf("log:\n%s", h.log())
	}
}

func TestAFailureThatNeverPassesBecomesADeadLetterAfterTheLastDelivery(t *testing.T) {
	h := newHarness(t)
	h.healthy.Store(false)
	h.batch("job:1")
	cfg := h.config()
	cfg.MaxDeliver = 3
	stats := consumeAll(t, cfg)
	dead := h.dead()
	if stats.Dead != 1 || stats.Retried != 2 || len(dead) != 1 {
		t.Fatalf("stats = %+v, dead = %v\n%s", stats, dead, h.log())
	}
	d := dead[0]
	if d.ID != "dead:job:1" || d.Headers["Metagente-Dead-Attempts"] != "3" || d.Headers["Metagente-Dead-Subject"] != "etl.orders.batch" ||
		!strings.Contains(d.Headers["Metagente-Dead-Reason"], "gave up after 3 deliveries") || d.Headers["Metagente-Dead-Event"] != "job:1" {
		t.Errorf("dead letter = %+v", d)
	}
	if !strings.Contains(string(d.Data), h.server.URL) {
		t.Errorf("the dead letter does not keep the event: %s", d.Data)
	}
	if h.hits.Load() != 3 {
		t.Errorf("%d calls, want 3 deliveries", h.hits.Load())
	}
}

func TestAFailureThatIsFinalGoesToTheDeadLettersAtOnce(t *testing.T) {
	h := newHarness(t)
	h.publish("etl.orders.batch", "job:9", `{}`)
	cfg := h.config()
	cfg.Message = "bad"
	stats := consumeAll(t, cfg)
	dead := h.dead()
	if stats.Dead != 1 || stats.Retried != 0 || len(dead) != 1 || dead[0].Headers["Metagente-Dead-Attempts"] != "1" ||
		!strings.Contains(dead[0].Headers["Metagente-Dead-Reason"], "a rule of the business") {
		t.Fatalf("stats = %+v, dead = %+v", stats, dead)
	}
}

func TestAnEventThatTheAgentDoesNotTakeIsADeadLetterWithTheReason(t *testing.T) {
	h := newHarness(t)
	h.publish("etl.orders.batch", "j:1", `{"other": 1}`)
	h.publish("etl.orders.batch", "j:2", `[1, 2]`)
	h.publish("etl.orders.batch", "j:3", `{"url": "x", "extra": 1}`)
	stats := consumeAll(t, h.config())
	dead := h.dead()
	if stats.Done != 0 || stats.Dead != 3 || len(dead) != 3 || h.hits.Load() != 0 {
		t.Fatalf("stats = %+v, dead = %d, calls = %d\n%s", stats, len(dead), h.hits.Load(), h.log())
	}
	reasons := map[string]string{}
	for _, d := range dead {
		reasons[d.Headers["Metagente-Dead-Event"]] = d.Headers["Metagente-Dead-Reason"]
	}
	if !strings.Contains(reasons["j:1"], "needs a value for `url`") && !strings.Contains(reasons["j:1"], "url") {
		t.Errorf("j:1: %q", reasons["j:1"])
	}
	if !strings.Contains(reasons["j:3"], "extra") {
		t.Errorf("j:3: %q", reasons["j:3"])
	}
}

func TestTheValuesOfAnEventAreReadAsTheMessageTakesThem(t *testing.T) {
	h := newHarness(t)
	for name, c := range map[string]struct {
		message, data, wantErr string
		want                   map[string]string
	}{
		"an object":       {"batch", `{"url":"http://x"}`, "", map[string]string{"url": "http://x"}},
		"a bare text":     {"batch", `http://x`, "", map[string]string{"url": "http://x"}},
		"a json text":     {"batch", `"http://x"`, "", map[string]string{"url": "http://x"}},
		"a list for one":  {"many", `[1, 2, 3]`, "", map[string]string{"items": "[1, 2, 3]"}},
		"an empty event":  {"bad", ``, "", map[string]string{}},
		"a missing value": {"batch", `{}`, "url", nil},
		"too many values": {"batch", `{"url":"x","y":1}`, "y", nil},
		"not an object":   {"bad", `[1]`, "takes 0 values", nil},
		"another message": {"nope", `{}`, "does not accept", nil},
	} {
		args, err := Decode(h.def, c.message, []byte(c.data))
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("%s: err = %v", name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		for key, want := range c.want {
			if got := args[key].Display(); got != want {
				t.Errorf("%s: %s = %q, want %q", name, key, got, want)
			}
		}
	}
}

func TestTheContentOfAnEventIsNeverInTheLog(t *testing.T) {
	h := newHarness(t)
	h.publish("etl.orders.batch", "j:1", `{"url": "x", "secret": "ana@example.com"}`)
	consumeAll(t, h.config())
	if strings.Contains(h.log(), "ana@example.com") {
		t.Errorf("content in the log:\n%s", h.log())
	}
	for _, d := range h.dead() {
		for name, v := range d.Headers {
			if strings.Contains(v, "ana@example.com") {
				t.Errorf("content in the header %s: %s", name, v)
			}
		}
	}
}

func TestEventsAreWorkedOnTogetherUpToTheLimit(t *testing.T) {
	h := newHarness(t)
	var inside, most atomic.Int64
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inside.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(80 * time.Millisecond)
		inside.Add(-1)
		fmt.Fprint(w, "fine")
	})
	for i := 0; i < 6; i++ {
		h.batch(fmt.Sprintf("job:%d", i))
	}
	cfg := h.config()
	cfg.InFlight = 3
	stats := consumeAll(t, cfg)
	if stats.Done != 6 || most.Load() != 3 {
		t.Errorf("stats = %+v, most at the same time = %d, want 3", stats, most.Load())
	}
}

func TestAnOutageOpensTheBreakerAndOneEventTestsTheDestinationBeforeTheRest(t *testing.T) {
	h := newHarness(t)
	h.healthy.Store(false)
	for i := 0; i < 4; i++ {
		h.batch(fmt.Sprintf("job:%d", i))
	}
	cfg := h.config()
	cfg.BreakerAfter = 2
	cfg.BreakerWait = 120 * time.Millisecond
	cfg.BreakerMaxWait = 120 * time.Millisecond
	cfg.MaxDeliver = 10
	cfg.IdleExit = 600 * time.Millisecond
	go func() { time.Sleep(450 * time.Millisecond); h.healthy.Store(true) }()
	started := time.Now()
	stats := consumeAll(t, cfg)
	if stats.Done != 4 || stats.Dead != 0 || stats.BreakerOpened < 1 {
		t.Fatalf("stats = %+v\n%s", stats, h.log())
	}
	// Without the breaker the four events would have used up their tries in a few milliseconds.
	if hits := h.hits.Load(); hits > 14 {
		t.Errorf("%d calls to a destination that was down: the breaker did not hold the events", hits)
	}
	if time.Since(started) < 450*time.Millisecond {
		t.Errorf("the run ended before the destination was back")
	}
	if !strings.Contains(h.log(), "no more events are taken") {
		t.Errorf("the breaker is not told:\n%s", h.log())
	}
}

func TestWhenTheRunIsToldToStopTheEventsInHandAreGivenBack(t *testing.T) {
	h := newHarness(t)
	started := make(chan struct{}, 1)
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	})
	h.batch("job:1")
	cfg := h.config()
	cfg.IdleExit = 0
	ctx, cancel := context.WithCancel(context.Background())
	go func() { <-started; cancel() }()
	stats, err := Run(ctx, cfg)
	if err != nil || stats.Interrupted != 1 || stats.Done != 0 || stats.Dead != 0 {
		t.Fatalf("stats = %+v, err = %v\n%s", stats, err, h.log())
	}
	// Given back at once: the next run gets it.
	h.healthy.Store(true)
	h.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "fine") })
	next := h.config()
	again := consumeAll(t, next)
	if again.Done != 1 {
		t.Errorf("the event was lost: %+v\n%s", again, h.log())
	}
}

func TestTheDeadLettersAreTriedAgainBeforeTheEventIsGivenBack(t *testing.T) {
	h := newHarness(t)
	h.publish("etl.orders.batch", "job:1", `{}`)
	h.mem.SetLimits(1, 0) // the event itself fills the stream: the dead letter has no room
	cfg := h.config()
	cfg.Message = "bad"
	cfg.Backoff = []time.Duration{40 * time.Millisecond}
	go func() { time.Sleep(60 * time.Millisecond); h.mem.SetLimits(0, 0) }()
	stats := consumeAll(t, cfg)
	if stats.Dead != 1 || len(h.dead()) != 1 || stats.Retried != 0 {
		t.Fatalf("stats = %+v\n%s", stats, h.log())
	}
}

func TestAnEventStaysInTheStreamWhenTheDeadLettersNeverTakeIt(t *testing.T) {
	h := newHarness(t)
	h.publish("etl.orders.batch", "job:1", `{}`)
	h.mem.SetLimits(1, 0)
	cfg := h.config()
	cfg.Message = "bad"
	cfg.MaxDeliver = 2
	cfg.IdleExit = 200 * time.Millisecond
	cfg.Backoff = []time.Duration{2 * time.Millisecond}
	stats := consumeAll(t, cfg)
	if stats.Dead != 0 || len(h.dead()) != 0 || !strings.Contains(h.log(), "it stays in the stream") {
		t.Fatalf("stats = %+v\n%s", stats, h.log())
	}
	if n := len(h.mem.Messages()); n != 1 {
		t.Errorf("%d messages in the stream, want the event itself", n)
	}
}

func TestTheRunEndsWhenItHasTakenAsManyEventsAsItWasToldTo(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		h.batch(fmt.Sprintf("job:%d", i))
	}
	cfg := h.config()
	cfg.MaxEvents = 2
	cfg.IdleExit = 0
	stats := consumeAll(t, cfg)
	if stats.Taken != 2 || stats.Done != 2 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestADeadLetterSubjectIsRequiredAndMustBeASubject(t *testing.T) {
	h := newHarness(t)
	for _, dead := range []string{"", "etl.*", "a b"} {
		cfg := h.config()
		cfg.Dead = dead
		if _, err := Run(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), "dead letters") {
			t.Errorf("%q: %v", dead, err)
		}
	}
}

func TestADownBrokerIsAFailureThatMayPass(t *testing.T) {
	h := newHarness(t)
	h.mem.SetDown(true)
	_, err := Run(context.Background(), h.config())
	if err == nil || !strings.Contains(err.Error(), "I could not make the consumer") || !strings.Contains(err.Error(), "This may pass") {
		t.Errorf("error = %v", err)
	}
}
