package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/consume"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/runtime"
	"github.com/Ribeiro/metagente-go/internal/tools"
	"github.com/Ribeiro/metagente-go/internal/trust"
)

// consumeArgs are the words of `metagente consume`.
type consumeArgs struct {
	file, agent, message, configPath string
	from, subject, dead              string
	stream, durable                  string
	inFlight, maxDeliver, maxEvents  int
	breakerAfter                     int
	ackWait, idleExit                time.Duration
	backoff                          []time.Duration
	quiet                            bool
}

// parseConsumeArgs reads the command line. The options with a value take it after a space or an `=`.
func parseConsumeArgs(args []string) (*consumeArgs, error) {
	c := &consumeArgs{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--quiet" {
			c.quiet = true
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			if c.file != "" {
				return nil, badUsage("`consume` takes one file.", "write it like: metagente consume worker.ag --from main --subject etl.orders.batch --dead etl.orders.dead")
			}
			c.file = arg
			continue
		}
		name, value, hasValue := strings.Cut(arg, "=")
		if !consumeOptions[name] {
			return nil, unknownConsumeOption(name)
		}
		if !hasValue {
			if i+1 >= len(args) {
				return nil, badUsage(fmt.Sprintf("`%s` needs a value.", name), fmt.Sprintf("write it like: %s VALUE", name))
			}
			i++
			value = args[i]
		}
		if err := c.set(name, value); err != nil {
			return nil, err
		}
	}
	return c, c.check()
}

func (c *consumeArgs) set(name, value string) error {
	number := func(into *int, least int) error {
		n, err := strconv.Atoi(value)
		if err != nil || n < least {
			return badUsage(fmt.Sprintf("`%s` needs a whole number of %d or more, not `%s`.", name, least, value), fmt.Sprintf("write it like: %s %d", name, max(least, 1)))
		}
		*into = n
		return nil
	}
	seconds := func(into *time.Duration) error {
		n, err := strconv.ParseFloat(value, 64)
		if err != nil || n < 0 {
			return badUsage(fmt.Sprintf("`%s` needs a number of seconds, not `%s`.", name, value), fmt.Sprintf("write it like: %s 60", name))
		}
		*into = time.Duration(n * float64(time.Second))
		return nil
	}
	switch name {
	case "--agent":
		c.agent = value
	case "--config":
		c.configPath = value
	case "--message":
		c.message = value
	case "--from":
		c.from = value
	case "--subject":
		c.subject = value
	case "--dead":
		c.dead = value
	case "--stream":
		c.stream = value
	case "--durable":
		c.durable = value
	case "--in-flight":
		return number(&c.inFlight, 1)
	case "--max-deliver":
		return number(&c.maxDeliver, 1)
	case "--max-events":
		return number(&c.maxEvents, 1)
	case "--breaker-after":
		return number(&c.breakerAfter, 1)
	case "--ack-wait":
		return seconds(&c.ackWait)
	case "--idle-exit":
		return seconds(&c.idleExit)
	case "--backoff":
		waits, err := parseWaits(value)
		if err != nil {
			return badUsage(fmt.Sprintf("`--backoff` is a list of waits, like 10s,1m,5m, and `%s` is not.", value), "write it like: --backoff 10s,1m,5m,15m")
		}
		c.backoff = waits
	}
	return nil
}

// consumeOptions are the options that take a value.
var consumeOptions = map[string]bool{
	"--agent": true, "--config": true, "--message": true, "--from": true, "--subject": true, "--dead": true, "--stream": true,
	"--durable": true, "--in-flight": true, "--max-deliver": true, "--max-events": true, "--breaker-after": true,
	"--ack-wait": true, "--idle-exit": true, "--backoff": true,
}

func unknownConsumeOption(name string) error {
	return badUsage(fmt.Sprintf("`consume` does not take the option `%s`.", name),
		"the options are --from, --subject, --dead, --message, --agent, --stream, --durable, --in-flight, --max-deliver, --ack-wait, --backoff, --breaker-after, --idle-exit, --max-events, --quiet and --config.")
}

// parseWaits reads waits written as Go writes durations (10s, 1m30s), separated by commas.
func parseWaits(text string) ([]time.Duration, error) {
	var waits []time.Duration
	for _, part := range strings.Split(text, ",") {
		d, err := time.ParseDuration(strings.TrimSpace(part))
		if err != nil || d < 0 {
			return nil, fmt.Errorf("not a wait: %s", part)
		}
		waits = append(waits, d)
	}
	return waits, nil
}

func (c *consumeArgs) check() error {
	switch {
	case c.file == "":
		return badUsage("`consume` needs the name of a file.", "write it like: metagente consume worker.ag --from main --subject etl.orders.batch --dead etl.orders.dead")
	case c.from == "" || c.subject == "" || c.dead == "":
		return badUsage("`consume` needs --from (a broker of metagente.toml), --subject (what to read) and --dead (where the events that are given up on go).",
			"write it like: metagente consume worker.ag --from main --subject etl.orders.batch --dead etl.orders.dead")
	}
	return nil
}

func runConsume(args []string, stdout, stderr io.Writer) int {
	c, err := parseConsumeArgs(args)
	if err != nil {
		printBadUsage(stderr, err)
		return 2
	}
	cfg, err := currentConfig(c.configPath)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	for _, note := range cfg.Warnings {
		fmt.Fprintf(stderr, "Note: %s\n", note)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rt := runtime.New(cfg)
	defer rt.Close()
	stats, err := consumeFile(ctx, rt, cfg, c, stdout, stderr)
	if err != nil {
		printError(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Taken %d: done %d, asked for again %d, dead letters %d, given back %d.\n",
		stats.Taken, stats.Done, stats.Retried, stats.Dead, stats.Interrupted)
	return 0
}

// consumeFile checks and approves what is needed, and runs the consumer until it is told to stop.
func consumeFile(ctx context.Context, rt *runtime.Runtime, cfg *config.Config, c *consumeArgs, stdout, stderr io.Writer) (consume.Stats, error) {
	agents, err := runtime.LoadAgents(c.file)
	if err != nil {
		return consume.Stats{}, err
	}
	rt.Linker.Register(agents)
	if err := runtime.CheckAgents(rt, agents, c.file); err != nil {
		return consume.Stats{}, err
	}
	def, err := runtime.PickAgent(agents, c.agent, c.file)
	if err != nil {
		return consume.Stats{}, err
	}
	message, err := consumeMessage(def, c)
	if err != nil {
		return consume.Stats{}, err
	}
	spec, err := consumeSpec(cfg, c)
	if err != nil {
		return consume.Stats{}, err
	}
	extra := []trust.Item{runtime.BrokerItem(spec)}
	confirm := func(missing []trust.Item) bool { return askToApprove(stderr, cfg.Root, missing) }
	if err := rt.AuthorizeWith(agents, extra, c.file, confirm); err != nil {
		return consume.Stats{}, withConsumeFlags(err, c)
	}
	conn := cfg.Broker[c.from]
	bus, err := tools.OpenBroker(conn, spec.Credential, rt.Getenv, cfg.Root)
	if err != nil {
		return consume.Stats{}, err
	}
	defer bus.Close()

	run := consume.Config{
		Runtime: rt, Agent: def, Message: message, Broker: bus,
		Spec: consumeConsumerSpec(def, message, conn, c),
		Dead: c.dead, InFlight: c.inFlight, MaxDeliver: c.maxDeliver, Backoff: c.backoff,
		BreakerAfter: c.breakerAfter, IdleExit: c.idleExit, MaxEvents: c.maxEvents,
	}
	if !c.quiet {
		run.Log = func(line string) { fmt.Fprintln(stderr, line) }
		fmt.Fprintf(stderr, "Reading %s from the broker %s for %s.%s; stop with Ctrl+C.\n", c.subject, c.from, def.Name, message)
	}
	return consume.Run(ctx, run)
}

// consumeMessage is the message that the agent is sent: the one asked for, or the only one it accepts.
func consumeMessage(def *lang.AgentDef, c *consumeArgs) (string, error) {
	if c.message != "" {
		if def.FindAccept(c.message) == nil {
			return "", runtime.CheckMessage(def, c.message, nil)
		}
		return c.message, nil
	}
	if len(def.Accepts) != 1 {
		names := make([]string, len(def.Accepts))
		for i, a := range def.Accepts {
			names[i] = a.Message
		}
		return "", diag.Newf("agent %s needs to be told which message the events are", def.Name).
			Fixf("add --message NAME; it accepts: %s", strings.Join(names, ", "))
	}
	return def.Accepts[0].Message, nil
}

// consumeSpec is what the command reaches, as an approval has it.
func consumeSpec(cfg *config.Config, c *consumeArgs) (tools.BrokerSpec, error) {
	if !broker.ValidPattern(c.subject) {
		return tools.BrokerSpec{}, diag.Newf("`%s` is not a subject to read", c.subject).
			Fix(`write names made of letters, digits, _ and -, joined by dots; a name may be * and the last may be >`)
	}
	if !broker.ValidSubject(c.dead) {
		return tools.BrokerSpec{}, diag.Newf("`%s` is not a subject for the dead letters", c.dead).
			Fix("write one subject, with no * or >, for example: etl.orders.dead")
	}
	return tools.ConsumeSpecOf(c.from, cfg.Broker, cfg.Credentials, c.subject, c.dead)
}

// consumeConsumerSpec says what the broker is asked to keep for this consumer. Its name, when none was given,
// is made from the agent and the message, so that two workers of the same file share the work.
func consumeConsumerSpec(def *lang.AgentDef, message string, conn *config.BrokerConn, c *consumeArgs) (spec broker.ConsumerSpec) {
	durable := c.durable
	if durable == "" {
		durable = "metagente-" + def.Name + "-" + message
	}
	stream := c.stream
	if stream == "" {
		stream = conn.Stream
	}
	return broker.ConsumerSpec{Stream: stream, Durable: sanitizeName(durable), Subject: c.subject, AckWait: c.ackWait}
}

// withConsumeFlags makes the advice of a refusal fit this command: the approval needs the same words.
func withConsumeFlags(err error, c *consumeArgs) error {
	d, ok := diag.From(err)
	if !ok || !strings.Contains(d.Suggestion, "metagente trust") {
		return err
	}
	d.Fixf("read the lines above, and if you trust them run: metagente trust %s --from %s --subject %s --dead %s", c.file, c.from, c.subject, c.dead)
	return d
}

func sanitizeName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}
