package tools

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

func memoryBroker(t *testing.T, allow ...string) (*Broker, *broker.Memory) {
	t.Helper()
	pool := NewBrokerPool()
	t.Cleanup(func() { _ = pool.Close() })
	conn := &config.BrokerConn{Name: "main", Driver: "memory"}
	decl := &lang.ToolDecl{Name: "events", Kind: lang.ToolBroker, Command: "main", Allow: allow}
	tool, err := NewBroker(decl, BrokerOptions{
		Conns: map[string]*config.BrokerConn{"main": conn}, Getenv: func(string) string { return "" }, Pool: pool,
	}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	return tool, pool.Memory("main")
}

func publishArgs(subject, id string, data value.Value) Args {
	args := Args{"subject": value.Text(subject), "data": data}
	if id != "" {
		args["id"] = value.Text(id)
	}
	return args
}

func TestAMessageIsPublishedAndTheAnswerSaysWhereItWent(t *testing.T) {
	tool, mem := memoryBroker(t, "etl.orders.*")
	record := value.Record(map[string]value.Value{"id": value.Number(7), "rows": value.List([]value.Value{value.Text("a")})})
	got, err := tool.Call(context.Background(), "publish", publishArgs("etl.orders.batch", "job:1", record))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	seq, _ := got.Field("seq")
	dup, _ := got.Field("duplicate")
	stream, _ := got.Field("stream")
	if seq.Number != 1 || dup.Bool || stream.Text != "memory" {
		t.Errorf("answer = %s", got.Display())
	}
	if _, err := tool.Call(context.Background(), "publish", publishArgs("etl.orders.control", "job:2", value.Text("plain text"))); err != nil {
		t.Fatal(rendered(t, err))
	}
	msgs := mem.Messages()
	if len(msgs) != 2 || msgs[0].Subject != "etl.orders.batch" || msgs[0].ID != "job:1" {
		t.Fatalf("messages = %+v", msgs)
	}
	if string(msgs[0].Data) != `{"id":7,"rows":["a"]}` || string(msgs[1].Data) != "plain text" {
		t.Errorf("data = %q and %q", msgs[0].Data, msgs[1].Data)
	}
}

func TestACopyOfAMessageIsDroppedAndTheAnswerSaysSo(t *testing.T) {
	tool, mem := memoryBroker(t, "etl.>")
	for i, want := range []bool{false, true} {
		got, err := tool.Call(context.Background(), "publish", publishArgs("etl.a", "job:1", value.Text("x")))
		if err != nil {
			t.Fatal(rendered(t, err))
		}
		if dup, _ := got.Field("duplicate"); dup.Bool != want {
			t.Errorf("try %d: duplicate = %v", i, dup.Bool)
		}
	}
	if len(mem.Messages()) != 1 {
		t.Errorf("%d messages", len(mem.Messages()))
	}
}

func TestOnlyTheSubjectsThatWereDeclaredCanBePublishedTo(t *testing.T) {
	tool, mem := memoryBroker(t, "etl.orders.batch", "audit.*")
	for subject, want := range map[string]string{
		"etl.orders.control": "may not publish to `etl.orders.control`",
		"audit.a.b":          "may not publish to",
		"etl.orders":         "may not publish to",
		"etl.*":              "is names made of letters",
		"etl..x":             "is names made of letters",
		"":                   "needs a value for `subject`",
	} {
		args := publishArgs(subject, "id", value.Text("x"))
		if subject == "" {
			delete(args, "subject")
		}
		_, err := tool.Call(context.Background(), "publish", args)
		if err == nil || !strings.Contains(rendered(t, err), want) {
			t.Errorf("%q: %v", subject, err)
		}
	}
	if _, err := tool.Call(context.Background(), "publish", publishArgs("audit.x", "id", value.Text("x"))); err != nil {
		t.Errorf("a subject that is allowed: %v", err)
	}
	if len(mem.Messages()) != 1 {
		t.Errorf("%d messages, want only the allowed one", len(mem.Messages()))
	}
	_, err := tool.Call(context.Background(), "publish", publishArgs("etl.orders.control", "id", value.Text("x")))
	if !strings.Contains(rendered(t, err), `"etl.orders.batch", "audit.*"`) {
		t.Errorf("the problem does not say what was declared: %s", rendered(t, err))
	}
}

func TestEveryMessageNeedsAnIDAndData(t *testing.T) {
	tool, _ := memoryBroker(t, "etl.>")
	for name, c := range map[string]struct {
		args Args
		want string
	}{
		"no id":      {publishArgs("etl.a", "", value.Text("x")), "needs a value for `id`"},
		"nothing id": {Args{"subject": value.Text("etl.a"), "id": value.Nothing, "data": value.Text("x")}, "needs a value for `id`"},
		"a space":    {publishArgs("etl.a", "job 1", value.Text("x")), "cannot be one"},
		"too long":   {publishArgs("etl.a", strings.Repeat("x", 256), value.Text("x")), "cannot be one"},
		"no data":    {Args{"subject": value.Text("etl.a"), "id": value.Text("j:1")}, "needs a value for `data`"},
		"no data 2":  {publishArgs("etl.a", "j:1", value.Nothing), "needs a value for `data`"},
	} {
		_, err := tool.Call(context.Background(), "publish", c.args)
		if err == nil || !strings.Contains(rendered(t, err), c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := tool.Call(context.Background(), "publish", Args{"subject": value.Text("etl.a"), "id": value.Number(12), "data": value.Text("x")}); err != nil {
		t.Errorf("a number as the id: %v", err)
	}
	if _, err := tool.Call(context.Background(), "nothing", Args{}); err == nil || !strings.Contains(rendered(t, err), "no action called `nothing`") {
		t.Errorf("an action that is not there: %v", err)
	}
}

func TestAMessageLargerThanTheLimitIsAProblemAndNothingIsSent(t *testing.T) {
	tool, mem := memoryBroker(t, "etl.>")
	tool.limits.MaxBrokerBytes = 10
	_, err := tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:1", value.Text(strings.Repeat("x", 11))))
	if err == nil || !strings.Contains(rendered(t, err), "has 11 bytes, and a message may have 10") {
		t.Errorf("error = %v", err)
	}
	if _, err := tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:2", value.Text(strings.Repeat("x", 10)))); err != nil {
		t.Errorf("a message on the limit: %v", err)
	}
	if len(mem.Messages()) != 1 {
		t.Errorf("%d messages", len(mem.Messages()))
	}
}

func TestAFullOrUnreachableBrokerIsAFailureThatMayPass(t *testing.T) {
	tool, mem := memoryBroker(t, "etl.>")
	mem.MaxMessages = 1
	if _, err := tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:1", value.Text("x"))); err != nil {
		t.Fatal(err)
	}
	_, err := tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:2", value.Text("x")))
	if r, ok := diag.RetryOf(err); !ok || r.After != 0 || !strings.Contains(rendered(t, err), "the broker is full") {
		t.Errorf("full: %v", err)
	}
	mem.MaxMessages = 0
	mem.SetDown(true)
	_, err = tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:3", value.Text("x")))
	if _, ok := diag.RetryOf(err); !ok || !strings.Contains(rendered(t, err), "I could not reach the broker of `events`") {
		t.Errorf("down: %v", err)
	}
	mem.SetDown(false)
	if _, err := tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:3", value.Text("x"))); err != nil {
		t.Errorf("working again: %v", err)
	}
}

// refusing is a broker that says what the test tells it to.
type refusing struct{ err error }

func (r refusing) Publish(context.Context, broker.Message) (broker.PubAck, error) {
	return broker.PubAck{}, r.err
}
func (refusing) Consume(context.Context, broker.ConsumerSpec) (broker.Consumer, error) {
	return nil, errors.New("not used")
}
func (refusing) Close() error { return nil }

func TestOtherRefusalsAreFinalAndNeverShowThePassword(t *testing.T) {
	for name, c := range map[string]struct {
		err   error
		want  string
		retry bool
	}{
		"refused":     {errors.Join(broker.ErrRefused, errors.New("bad subject with s3cret in it")), "did not take the message", false},
		"unavailable": {errors.Join(broker.ErrUnavailable, errors.New("dial s3cret")), "I could not reach the broker", true},
		"stream":      {errors.Join(broker.ErrNoStream, errors.New("none for s3cret")), "no stream of the broker takes the subject `etl.a`", false},
		"not built":   {broker.ErrNotInBuild, "was made without the `jetstream` broker", false},
	} {
		pool := NewBrokerPool()
		pool.brokers["jetstream\x00nats://h:4222\x00\x00BROKER_PASSWORD\x00"] = refusing{c.err}
		conn := &config.BrokerConn{Name: "main", Driver: "jetstream", URL: "nats://h:4222", TLS: config.TLSDisable}
		decl := &lang.ToolDecl{Name: "events", Kind: lang.ToolBroker, Command: "main", Allow: []string{"etl.>"}}
		tool, err := NewBroker(decl, BrokerOptions{
			Conns: map[string]*config.BrokerConn{"main": conn}, Credentials: map[string]string{"events": "BROKER_PASSWORD"},
			Getenv: func(string) string { return "s3cret" }, Pool: pool,
		}, config.Default().Limits)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:1", value.Text("x")))
		shown := rendered(t, err)
		if !strings.Contains(shown, c.want) || strings.Contains(shown, "s3cret") {
			t.Errorf("%s: %s", name, shown)
		}
		if _, ok := diag.RetryOf(err); ok != c.retry {
			t.Errorf("%s: retry = %v", name, ok)
		}
	}
}

func TestTheBrokerIsNotReachedBeforeItIsApprovedAndNeedsItsPassword(t *testing.T) {
	conn := &config.BrokerConn{Name: "main", Driver: "jetstream", URL: "nats://h:4222", TLS: config.TLSDisable}
	decl := &lang.ToolDecl{Name: "events", Kind: lang.ToolBroker, Command: "main", Allow: []string{"etl.>"}}
	asked := 0
	opts := BrokerOptions{
		Conns: map[string]*config.BrokerConn{"main": conn}, Credentials: map[string]string{"events": "BROKER_PASSWORD"},
		Getenv: func(string) string { return "" }, Pool: NewBrokerPool(),
		Allow: func(spec BrokerSpec) error {
			asked++
			if spec.Target != "nats://h:4222 (tls disable)" || spec.Credential != "BROKER_PASSWORD" || spec.Driver != "jetstream" {
				t.Errorf("spec = %+v", spec)
			}
			return diag.New("not approved")
		},
	}
	tool, err := NewBroker(decl, opts, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:1", value.Text("x")))
	if err == nil || !strings.Contains(rendered(t, err), "not approved") || asked != 1 || len(opts.Pool.brokers) != 0 {
		t.Errorf("error = %v, asked = %d", err, asked)
	}
	opts.Allow = nil
	tool, _ = NewBroker(decl, opts, config.Default().Limits)
	_, err = tool.Call(context.Background(), "publish", publishArgs("etl.a", "j:1", value.Text("x")))
	if err == nil || !strings.Contains(rendered(t, err), "the variable BROKER_PASSWORD is empty") {
		t.Errorf("without the password: %v", err)
	}
}

func TestTheSpecOfABrokerSaysWhereItIsAndChangesWithIt(t *testing.T) {
	build := func(mutate func(*config.BrokerConn, *lang.ToolDecl)) BrokerSpec {
		conn := &config.BrokerConn{Name: "main", Driver: "jetstream", URL: "tls://b:4222", User: "etl", TLS: config.TLSVerify, Stream: "ETL"}
		decl := &lang.ToolDecl{Name: "events", Kind: lang.ToolBroker, Command: "main", Allow: []string{"etl.>"}}
		mutate(conn, decl)
		spec, err := BrokerSpecOf(decl, map[string]*config.BrokerConn{"main": conn}, map[string]string{"events": "PW"})
		if err != nil {
			t.Fatal(err)
		}
		return spec
	}
	base := build(func(*config.BrokerConn, *lang.ToolDecl) {})
	if base.Target != "etl@tls://b:4222 (tls verify), stream ETL" || base.Credential != "PW" || strings.Join(base.Subjects, " ") != "etl.>" {
		t.Errorf("spec = %+v", base)
	}
	for name, mutate := range map[string]func(*config.BrokerConn, *lang.ToolDecl){
		"url":      func(c *config.BrokerConn, _ *lang.ToolDecl) { c.URL = "tls://other:4222" },
		"user":     func(c *config.BrokerConn, _ *lang.ToolDecl) { c.User = "bob" },
		"tls":      func(c *config.BrokerConn, _ *lang.ToolDecl) { c.TLS = config.TLSRequire },
		"ca":       func(c *config.BrokerConn, _ *lang.ToolDecl) { c.CAFile = "ca.pem" },
		"stream":   func(c *config.BrokerConn, _ *lang.ToolDecl) { c.Stream = "OTHER" },
		"subjects": func(_ *config.BrokerConn, d *lang.ToolDecl) { d.Allow = []string{"etl.>", "more.x"} },
	} {
		if build(mutate).Fingerprint == base.Fingerprint {
			t.Errorf("changing the %s did not change what is approved", name)
		}
	}
	memory := build(func(c *config.BrokerConn, _ *lang.ToolDecl) { *c = config.BrokerConn{Name: "main", Driver: "memory"} })
	if memory.Target != "in the memory of this process" {
		t.Errorf("memory = %+v", memory)
	}
	decl := &lang.ToolDecl{Name: "events", Kind: lang.ToolBroker, Command: "missing", Allow: []string{"a"}}
	if _, err := BrokerSpecOf(decl, nil, nil); err == nil || !strings.Contains(rendered(t, err), "no section [broker.missing]") {
		t.Errorf("a broker that is not there: %v", err)
	}
}

func TestToolsFromTheBuilderIncludeTheBroker(t *testing.T) {
	conn := &config.BrokerConn{Name: "main", Driver: "memory"}
	def := &lang.AgentDef{Name: "A", Tools: []*lang.ToolDecl{{Name: "events", Kind: lang.ToolBroker, Command: "main", Allow: []string{"a.b"}}}}
	registry, err := Build(def, Options{Limits: config.Default().Limits, Broker: BrokerOptions{
		Conns: map[string]*config.BrokerConn{"main": conn}, Getenv: func(string) string { return "" },
	}})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	tool, ok := registry["events"].(*Broker)
	if !ok {
		t.Fatalf("tool = %T", registry["events"])
	}
	actions, _ := tool.Actions(context.Background())
	if len(actions) != 1 || actions[0].Name != "publish" || tool.Name() != "events" {
		t.Errorf("actions = %+v", actions)
	}
	if err := tool.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
	def.Tools[0].Command = "nowhere"
	if _, err := Build(def, Options{Limits: config.Default().Limits}); err == nil {
		t.Error("a broker that is not configured was built")
	}
}
