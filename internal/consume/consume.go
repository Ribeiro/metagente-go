// Package consume is `metagente consume`: it takes the messages of a stream and gives each one to an
// agent, as a call would, and answers the broker according to what came of it (section 13 of
// docs/design-async-elt.md).
//
//   - The agent replied: the message is confirmed.
//   - The agent failed with a failure that may pass (`fail ... retry`, or a tool that says so): the message
//     is asked for again after a wait, up to a number of deliveries; after the last one it is a dead letter.
//   - The agent failed for good, or the message is not one the agent takes: it goes to the dead letters
//     with the reason, and is ended.
//
// When the failures that may pass come one after the other, a circuit breaker stops taking messages for a
// while, so that an outage of the destination does not use up the deliveries of the batches that wait.
package consume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/clip"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/runtime"
	"github.com/Ribeiro/metagente-go/internal/tools"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// Defaults, from the starting values of section 14 of the design.
const (
	DefaultInFlight   = 1
	DefaultMaxDeliver = 5
	DefaultAckWait    = 60 * time.Second
	// DefaultBreakerAfter is how many failures that may pass, one after the other, open the breaker.
	DefaultBreakerAfter = 3
	// DefaultBreakerWait and DefaultBreakerMaxWait are the first wait of an open breaker, and the most it grows to.
	DefaultBreakerWait    = 30 * time.Second
	DefaultBreakerMaxWait = 5 * time.Minute
	// maxReason is how much of a reason travels with a dead letter.
	maxReason = 300
)

// DefaultBackoff is the wait before a message is delivered again, by the number of the delivery that failed.
var DefaultBackoff = []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}

// Config says what to consume and how.
type Config struct {
	Runtime *runtime.Runtime
	Agent   *lang.AgentDef
	// Message is the message that the agent is sent for each event.
	Message string
	Broker  broker.Broker
	Spec    broker.ConsumerSpec
	// Dead is the subject where the messages that are given up on are put, with the reason.
	Dead string

	// InFlight is how many events are worked on at the same time.
	InFlight int
	// MaxDeliver is the most deliveries of one event; the failure of the last is final.
	MaxDeliver int
	// Backoff is the wait before the delivery that follows a failure that may pass, by the number of the
	// delivery that failed; the last one repeats.
	Backoff []time.Duration
	// BreakerAfter, BreakerWait and BreakerMaxWait describe the circuit breaker.
	BreakerAfter   int
	BreakerWait    time.Duration
	BreakerMaxWait time.Duration

	// IdleExit ends the run when no event came for that long and none is being worked on; zero means never.
	IdleExit time.Duration
	// MaxEvents ends the run after that many events were taken; zero means no limit.
	MaxEvents int
	// FetchWait is how long one request for events waits.
	FetchWait time.Duration

	// Log receives a line for each thing that happens. It never has the content of an event.
	Log func(string)
}

// Stats counts what happened to the events.
type Stats struct {
	Taken, Done, Retried, Dead, Interrupted, BreakerOpened int
}

func (c *Config) defaults() {
	if c.InFlight <= 0 {
		c.InFlight = DefaultInFlight
	}
	if c.MaxDeliver <= 0 {
		c.MaxDeliver = DefaultMaxDeliver
	}
	if len(c.Backoff) == 0 {
		c.Backoff = DefaultBackoff
	}
	if c.BreakerAfter <= 0 {
		c.BreakerAfter = DefaultBreakerAfter
	}
	if c.BreakerWait <= 0 {
		c.BreakerWait = DefaultBreakerWait
	}
	if c.BreakerMaxWait < c.BreakerWait {
		c.BreakerMaxWait = max(c.BreakerWait, DefaultBreakerMaxWait)
	}
	if c.FetchWait <= 0 {
		c.FetchWait = 2 * time.Second
	}
	if c.Spec.AckWait <= 0 {
		c.Spec.AckWait = DefaultAckWait
	}
	c.Spec.MaxDeliver = c.MaxDeliver
	c.Spec.MaxInFlight = c.InFlight
	if c.Log == nil {
		c.Log = func(string) {
			// No one is listening: the counts still say what happened.
		}
	}
}

// Run takes events until it is told to stop (the context ends), the limits are reached, or the broker fails
// in a way that will not pass. Events that were being worked on when it is told to stop are asked for again.
func Run(ctx context.Context, cfg Config) (Stats, error) {
	cfg.defaults()
	if cfg.Dead == "" || !broker.ValidSubject(cfg.Dead) {
		return Stats{}, diag.New("consume needs a subject for the dead letters").Fix("add --dead SUBJECT, for example: --dead etl.orders.dead")
	}
	consumer, err := cfg.Broker.Consume(ctx, cfg.Spec)
	if err != nil {
		return Stats{}, brokerProblem("I could not make the consumer", err)
	}
	defer consumer.Close()
	r := &run{cfg: cfg, consumer: consumer, breaker: newBreaker(cfg), done: make(chan struct{}, 1024), last: time.Now()}
	return r.loop(ctx)
}

type run struct {
	cfg      Config
	consumer broker.Consumer
	breaker  *breaker
	wg       sync.WaitGroup
	done     chan struct{} // a worker finished

	mu      sync.Mutex
	running int
	stats   Stats
	last    time.Time // the last time something was taken or finished
}

func (r *run) loop(ctx context.Context) (Stats, error) {
	var fatal error
	for ctx.Err() == nil {
		r.mu.Lock()
		running, taken := r.running, r.stats.Taken
		r.mu.Unlock()
		if r.cfg.MaxEvents > 0 && taken >= r.cfg.MaxEvents {
			break
		}
		slots := r.cfg.InFlight - running
		if slots <= 0 {
			r.waitForWorker(ctx)
			continue
		}
		n, wait := r.breaker.gate(running, slots)
		if n == 0 {
			r.sleepOrWorker(ctx, wait)
			continue
		}
		if r.cfg.MaxEvents > 0 {
			n = min(n, r.cfg.MaxEvents-taken)
		}
		deliveries, err := r.consumer.Fetch(ctx, n, r.cfg.FetchWait)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			if !broker.MayPass(err) {
				fatal = brokerProblem("the broker refused to give events", err)
				break
			}
			r.cfg.Log("the broker did not answer: " + clip.Collapse(err.Error(), maxReason) + "; waiting")
			r.sleepOrWorker(ctx, 2*time.Second)
			continue
		}
		if len(deliveries) == 0 {
			if r.idle() {
				break
			}
			continue
		}
		for _, d := range deliveries {
			r.start(ctx, d)
		}
	}
	r.wg.Wait()
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats, fatal
}

// idle says whether the run was told to end when nothing happens, and nothing has for long enough.
func (r *run) idle() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cfg.IdleExit > 0 && r.running == 0 && time.Since(r.last) >= r.cfg.IdleExit
}

func (r *run) waitForWorker(ctx context.Context) {
	select {
	case <-r.done:
	case <-ctx.Done():
	}
}

func (r *run) sleepOrWorker(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.done:
	case <-ctx.Done():
	}
}

func (r *run) start(ctx context.Context, d broker.Delivery) {
	r.mu.Lock()
	r.running++
	r.stats.Taken++
	r.last = time.Now()
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.work(ctx, d)
		r.mu.Lock()
		r.running--
		r.last = time.Now()
		r.mu.Unlock()
		select {
		case r.done <- struct{}{}:
		default:
		}
	}()
}

// outcome is what came of one delivery.
type outcome int

const (
	finished outcome = iota
	mayPass
	failed
	interrupted
)

// work gives one event to the agent and answers the broker.
func (r *run) work(ctx context.Context, d broker.Delivery) {
	m := d.Message()
	label := fmt.Sprintf("event %d (delivery %d)", d.Seq(), d.Attempt())
	beating := make(chan struct{})
	go r.heartbeat(ctx, d, beating)
	defer close(beating) // the broker is told that the work goes on until the answer is given
	started := time.Now()
	result, retry, reason := r.give(ctx, m)

	// The answers to the broker are made even if the run was told to stop: the event must not be left waiting.
	answer, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	switch result {
	case finished:
		r.account(func(s *Stats) { s.Done++ })
		r.breaker.success()
		r.ack(answer, d, label, fmt.Sprintf("%s: done in %s", label, time.Since(started).Round(time.Millisecond)))
	case interrupted:
		r.account(func(s *Stats) { s.Interrupted++ })
		if err := d.Nak(answer, 0); err != nil {
			r.cfg.Log(fmt.Sprintf("%s: stopped, and I could not give it back: %s", label, clip.Collapse(err.Error(), maxReason)))
			return
		}
		r.cfg.Log(label + ": stopped, given back to the broker")
	case mayPass:
		r.retry(answer, d, label, retry, reason)
	default:
		// The agent answered, with a failure that is final: whatever it works with is there.
		r.breaker.success()
		r.bury(answer, d, label, reason)
	}
}

// ack confirms an event. A confirmation that does not arrive is told, and the event will come again.
func (r *run) ack(ctx context.Context, d broker.Delivery, label, line string) {
	if err := d.Ack(ctx); err != nil {
		r.cfg.Log(fmt.Sprintf("%s: done, but the broker did not get the confirmation (%s); it will come again", label, clip.Collapse(err.Error(), maxReason)))
		return
	}
	r.cfg.Log(line)
}

// retry asks for the event again after a wait, or gives up on it when this was the last delivery.
func (r *run) retry(ctx context.Context, d broker.Delivery, label string, suggested time.Duration, reason string) {
	if opened := r.breaker.failure(); opened {
		r.account(func(s *Stats) { s.BreakerOpened++ })
		r.cfg.Log(fmt.Sprintf("the destination failed %d times in a row: no more events are taken for %s", r.cfg.BreakerAfter, r.breaker.currentWait().Round(time.Millisecond)))
	}
	if d.Attempt() >= r.cfg.MaxDeliver {
		r.bury(ctx, d, label, fmt.Sprintf("gave up after %d deliveries: %s", d.Attempt(), reason))
		return
	}
	delay := max(suggested, r.backoff(d.Attempt()))
	if err := d.Nak(ctx, delay); err != nil {
		r.cfg.Log(fmt.Sprintf("%s: could not be given back (%s); it will come again when the broker's time runs out", label, clip.Collapse(err.Error(), maxReason)))
		return
	}
	r.account(func(s *Stats) { s.Retried++ })
	r.cfg.Log(fmt.Sprintf("%s: failed, may pass (%s); again in %s", label, reason, delay.Round(time.Millisecond)))
}

func (r *run) backoff(attempt int) time.Duration {
	i := min(max(attempt-1, 0), len(r.cfg.Backoff)-1)
	return r.cfg.Backoff[i]
}

// bury puts the event in the dead letters, with the reason, and ends it. If the dead letters cannot be written,
// the event is asked for again instead: it is never lost.
func (r *run) bury(ctx context.Context, d broker.Delivery, label, reason string) {
	m := d.Message()
	reason = clip.Collapse(reason, maxReason)
	id := m.ID
	if id == "" {
		id = "seq" + strconv.FormatUint(d.Seq(), 10)
	}
	letter := broker.Message{
		Subject: r.cfg.Dead,
		ID:      "dead:" + id,
		Data:    m.Data,
		Headers: map[string]string{
			"Metagente-Dead-Reason":   reason,
			"Metagente-Dead-Subject":  m.Subject,
			"Metagente-Dead-Event":    id,
			"Metagente-Dead-Attempts": strconv.Itoa(d.Attempt()),
		},
	}
	if err := r.writeDeadLetter(ctx, letter); err != nil {
		if r.breaker.failure() {
			r.account(func(s *Stats) { s.BreakerOpened++ })
		}
		delay := r.backoff(d.Attempt())
		r.cfg.Log(fmt.Sprintf("%s: should be a dead letter (%s), but the dead letters did not take it (%s); it stays in the stream, again in %s",
			label, reason, clip.Collapse(err.Error(), maxReason), delay))
		_ = d.Nak(ctx, delay)
		return
	}
	if err := d.Term(ctx, reason); err != nil {
		r.cfg.Log(fmt.Sprintf("%s: put in the dead letters, but not ended (%s); it may come again", label, clip.Collapse(err.Error(), maxReason)))
		return
	}
	r.account(func(s *Stats) { s.Dead++ })
	r.cfg.Log(fmt.Sprintf("%s: dead letter on %s (%s)", label, r.cfg.Dead, reason))
}

// writeDeadLetter tries a few times, with growing waits, before it gives up: a delivery of the event is used
// up each time the event is given back, and the deliveries are not many.
func (r *run) writeDeadLetter(ctx context.Context, letter broker.Message) (err error) {
	wait := r.backoff(1)
	for try := 0; try < 3; try++ {
		if try > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
			wait *= 2
		}
		if _, err = r.cfg.Broker.Publish(ctx, letter); err == nil {
			return nil
		}
	}
	return err
}

func (r *run) account(change func(*Stats)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(&r.stats)
}

// heartbeat tells the broker, while the agent works, that the work goes on.
func (r *run) heartbeat(ctx context.Context, d broker.Delivery, stop <-chan struct{}) {
	ticker := time.NewTicker(max(r.cfg.Spec.AckWait/3, time.Millisecond))
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = d.InProgress(context.WithoutCancel(ctx))
		case <-stop:
			return
		}
	}
}

// give runs the agent on the event. The reason never has the content of the event, only what the agent or the
// check said.
func (r *run) give(ctx context.Context, m broker.Message) (result outcome, retry time.Duration, reason string) {
	defer func() {
		if p := recover(); p != nil {
			result, reason = failed, "the agent stopped with an unexpected error"
		}
	}()
	args, err := Decode(r.cfg.Agent, r.cfg.Message, m.Data)
	if err != nil {
		return failed, 0, err.Error()
	}
	taskID := runtime.NewTaskID()
	defer r.cfg.Runtime.ForgetConversation(taskID)
	agent, err := runtime.NewAgent(r.cfg.Runtime, r.cfg.Agent, taskID)
	if err == nil {
		call := &runtime.Call{TaskID: taskID}
		if err = agent.Start(ctx, call); err == nil {
			_, err = agent.Handle(ctx, call, r.cfg.Message, args)
		}
	}
	return classify(ctx, err)
}

// classify says what a failure of the agent means (section 17, item 1 of the design).
func classify(ctx context.Context, err error) (outcome, time.Duration, string) {
	switch {
	case err == nil:
		return finished, 0, ""
	case ctx.Err() != nil:
		return interrupted, 0, ""
	}
	text := "the agent failed"
	if d, ok := diag.From(err); ok {
		text = d.Message
	}
	if r, ok := diag.RetryOf(err); ok {
		return mayPass, r.After, text
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return mayPass, 0, "a call took too long"
	}
	return failed, 0, text
}

// Decode turns the content of an event into the values of the message. A JSON object gives one value for
// each of its fields. Anything else, a list, a text or a number, is the one value of a message that takes
// exactly one. The values are checked against what the agent accepts before it runs.
func Decode(def *lang.AgentDef, message string, data []byte) (tools.Args, error) {
	accept := def.FindAccept(message)
	if accept == nil {
		if d := runtime.CheckMessage(def, message, nil); d != nil {
			return nil, errors.New(d.Message)
		}
		return nil, fmt.Errorf("the agent does not accept the message `%s`", message)
	}
	args := tools.Args{}
	trimmed := strings.TrimSpace(string(data))
	if trimmed != "" {
		var decoded any
		err := json.Unmarshal([]byte(trimmed), &decoded)
		fields, isObject := decoded.(map[string]any)
		switch {
		case err == nil && isObject:
			for name, v := range fields {
				args[name] = value.FromJSON(v)
			}
		case len(accept.Params) == 1 && err == nil:
			args[accept.Params[0]] = value.FromJSON(decoded)
		case len(accept.Params) == 1:
			args[accept.Params[0]] = value.Text(string(data))
		default:
			return nil, fmt.Errorf("the event is not a JSON object, and the message `%s` takes %d values", message, len(accept.Params))
		}
	}
	if d := runtime.CheckMessage(def, message, args); d != nil {
		return nil, errors.New(d.Message)
	}
	return args, nil
}

// brokerProblem is the problem of a broker that failed, in the words of the broker. A failure that may pass
// says so.
func brokerProblem(what string, err error) error {
	d := diag.Newf("%s: %s", what, clip.Collapse(err.Error(), maxReason)).
		Fix("check the broker, the stream and the subject, and that the stream exists on the server")
	if broker.MayPass(err) {
		d.WithRetry(0)
	}
	return d
}
