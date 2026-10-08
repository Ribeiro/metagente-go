package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/clip"
	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/secret"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// brokerDrivers say how to connect to each kind of broker: they are given the connection, the secret (a
// password or a token, which may be empty) and the folder of the project.
var brokerDrivers = map[string]func(conn *config.BrokerConn, secret, root string) (broker.Broker, error){
	"jetstream": func(conn *config.BrokerConn, secret, root string) (broker.Broker, error) {
		cfg := broker.JetStreamConfig{URL: conn.URL, User: conn.User, Secret: secret, TLS: conn.TLS, Stream: conn.Stream}
		if conn.CAFile != "" {
			cfg.CAFile = absolute(root, conn.CAFile)
		}
		return broker.DialJetStream(cfg)
	},
	"memory": func(*config.BrokerConn, string, string) (broker.Broker, error) {
		return broker.NewMemory(), nil
	},
}

// BrokerOptions is what `tool x from broker` needs from the runtime.
type BrokerOptions struct {
	// Conns are the [broker.NAME] sections of metagente.toml.
	Conns map[string]*config.BrokerConn
	// Credentials say, for the name of a tool, the NAME of the variable that holds the password or the token.
	Credentials map[string]string
	// Getenv reads the variable of a credential when a broker is reached.
	Getenv func(string) string
	// Root is the folder of the project: a relative path of a file starts there.
	Root string
	// Pool keeps the connections; a server shares one between its agents.
	Pool *BrokerPool
	// Allow is asked before a broker is reached for the first time: it is the approval of `metagente trust`.
	Allow func(BrokerSpec) error
}

// BrokerSpec says what a `tool x from broker` reaches. It is what the person approves with `metagente trust`.
type BrokerSpec struct {
	Tool       string
	Connection string
	Driver     string
	// Target is where the broker is, for the person to read.
	Target string
	// Credential is the NAME of the variable that holds the password or the token, if one is set.
	Credential string
	// Subjects are the subjects the agent may publish to (patterns).
	Subjects []string
	// Fingerprint changes when the broker or the subjects change.
	Fingerprint string
}

// BrokerSpecOf is the spec of a `tool x from broker`. It is a problem when the broker is not in metagente.toml.
func BrokerSpecOf(decl *lang.ToolDecl, conns map[string]*config.BrokerConn, credentials map[string]string) (BrokerSpec, error) {
	conn, ok := conns[decl.ConnectionName()]
	if !ok {
		return BrokerSpec{}, diag.Newf("the tool `%s` uses the broker `%s`, and %s has no section [broker.%s]",
			decl.Name, decl.ConnectionName(), config.FileName, decl.ConnectionName()).
			Fixf("add [broker.%s] with a driver and a url (see docs/LANGUAGE.md)", decl.ConnectionName())
	}
	spec := BrokerSpec{
		Tool: decl.Name, Connection: conn.Name, Driver: conn.Driver,
		Credential: credentials[decl.Name], Subjects: append([]string(nil), decl.Allow...),
	}
	switch conn.Driver {
	case "memory":
		spec.Target = "in the memory of this process"
	default:
		spec.Target = conn.URL
		if conn.User != "" {
			spec.Target = conn.User + "@" + conn.URL
		}
		spec.Target += " (tls " + conn.TLS + ")"
		if conn.Stream != "" {
			spec.Target += ", stream " + conn.Stream
		}
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00", conn.Driver, conn.URL, conn.User, conn.TLS, conn.CAFile, conn.Stream, spec.Credential)
	for _, subject := range spec.Subjects {
		fmt.Fprintf(sum, "%s\x00", subject)
	}
	spec.Fingerprint = hex.EncodeToString(sum.Sum(nil))[:12]
	return spec, nil
}

// BrokerPool keeps the connections that were made, so a server does not make one for each conversation.
type BrokerPool struct {
	mu      sync.Mutex
	brokers map[string]broker.Broker
}

// NewBrokerPool creates an empty pool.
func NewBrokerPool() *BrokerPool { return &BrokerPool{brokers: map[string]broker.Broker{}} }

func (p *BrokerPool) get(key string, open func() (broker.Broker, error)) (broker.Broker, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, ok := p.brokers[key]; ok {
		return b, nil
	}
	b, err := open()
	if err != nil {
		return nil, err
	}
	p.brokers[key] = b
	return b, nil
}

// Memory is the broker in memory that the connection of that name uses (it is made if it is not there yet).
// It serves the tests, which look at what was published.
func (p *BrokerPool) Memory(connection string) *broker.Memory {
	b, _ := p.get(memoryKey(connection), func() (broker.Broker, error) { return broker.NewMemory(), nil })
	return b.(*broker.Memory)
}

func memoryKey(connection string) string { return "memory\x00" + connection }

// Close closes the connections.
func (p *BrokerPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var errs []error
	for key, b := range p.brokers {
		errs = append(errs, b.Close())
		delete(p.brokers, key)
	}
	return errors.Join(errs...)
}

// Broker is `tool x from broker "NAME"`: it publishes messages to a message broker, only to the subjects
// that the agent declared, and every message carries an id that lets the broker drop a copy.
type Broker struct {
	decl   *lang.ToolDecl
	conn   *config.BrokerConn
	spec   BrokerSpec
	opts   BrokerOptions
	limits config.Limits
	owned  bool // the pool is this tool's own
}

// NewBroker creates the tool. It is a problem when the broker is not in metagente.toml.
func NewBroker(decl *lang.ToolDecl, opts BrokerOptions, limits config.Limits) (*Broker, error) {
	spec, err := BrokerSpecOf(decl, opts.Conns, opts.Credentials)
	if err != nil {
		return nil, err
	}
	b := &Broker{decl: decl, conn: opts.Conns[decl.ConnectionName()], spec: spec, opts: opts, limits: limits}
	if b.opts.Pool == nil {
		b.opts.Pool, b.owned = NewBrokerPool(), true
	}
	return b, nil
}

// Close ends the connections the tool made itself.
func (b *Broker) Close() error {
	if b.owned {
		return b.opts.Pool.Close()
	}
	return nil
}

func (b *Broker) Name() string { return b.decl.Name }

// Actions is publish.
func (b *Broker) Actions(context.Context) ([]lang.ActionInfo, error) {
	return lang.BuiltinActions(b.decl), nil
}

func (b *Broker) Call(ctx context.Context, action string, args Args) (value.Value, error) {
	if action != "publish" {
		return value.Nothing, UnknownAction(b.decl.Name, action, []string{"publish"})
	}
	subject, err := b.subject(args)
	if err != nil {
		return value.Nothing, err
	}
	id, err := b.id(args)
	if err != nil {
		return value.Nothing, err
	}
	data, err := b.data(args)
	if err != nil {
		return value.Nothing, err
	}
	conn, err := b.connect()
	if err != nil {
		return value.Nothing, err
	}
	ack, err := conn.Publish(ctx, broker.Message{Subject: subject, ID: id, Data: data})
	if err != nil {
		if ctx.Err() != nil {
			return value.Nothing, ctx.Err()
		}
		return value.Nothing, b.failure(subject, err)
	}
	return value.Record(map[string]value.Value{
		"stream":    value.Text(ack.Stream),
		"seq":       value.Number(float64(ack.Seq)),
		"duplicate": value.Bool(ack.Duplicate),
	}), nil
}

// subject reads the subject, which has to be one that the declaration allows.
func (b *Broker) subject(args Args) (string, error) {
	subject, err := NeedText(b.decl.Name, "publish", args, "subject")
	if err != nil {
		return "", err
	}
	if !broker.ValidSubject(subject) {
		return "", diag.Newf("`%s.publish` was given `%s` as the subject, and a subject is names made of letters, digits, `_` and `-`, joined by dots", b.decl.Name, clip.Collapse(subject, 80)).
			Fix(`write it like: "etl.orders.batch"`)
	}
	for _, pattern := range b.decl.Allow {
		if broker.Matches(pattern, subject) {
			return subject, nil
		}
	}
	return "", diag.Newf("the tool `%s` may not publish to `%s`", b.decl.Name, subject).
		Fixf("it declared: %s; to allow more, add them after `publish` in the tool line", quoteAll(b.decl.Allow))
}

// id reads the id of the message, which the broker uses to drop a copy.
func (b *Broker) id(args Args) (string, error) {
	v, ok := args["id"]
	if !ok || v.Kind == value.KindNothing {
		return "", diag.Newf("`%s.publish` needs a value for `id`", b.decl.Name).
			Fix(`give the message an id that is the same every time the same message is sent, for example: id: "{job}:{number}"`)
	}
	id := strings.TrimSpace(v.Display())
	if id == "" || len(id) > 255 || strings.IndexFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", diag.Newf("`%s.publish` was given an `id` that cannot be one: it has to be 1 to 255 characters with no spaces", b.decl.Name).
			Fix(`write it like: id: "job7:batch12"`)
	}
	return id, nil
}

// data reads the message: a text goes as it is, anything else as JSON.
func (b *Broker) data(args Args) ([]byte, error) {
	v, ok := args["data"]
	if !ok || v.Kind == value.KindNothing {
		return nil, diag.Newf("`%s.publish` needs a value for `data`", b.decl.Name).
			Fix("give a text, a record or a list")
	}
	var data []byte
	if v.Kind == value.KindText {
		data = []byte(v.Text)
	} else {
		encoded, err := json.Marshal(v.ToJSON())
		if err != nil {
			return nil, diag.Newf("`%s.publish` could not turn `data` into JSON", b.decl.Name)
		}
		data = encoded
	}
	if int64(len(data)) > b.limits.MaxBrokerBytes {
		return nil, diag.Newf("the message of `%s.publish` has %d bytes, and a message may have %d here", b.decl.Name, len(data), b.limits.MaxBrokerBytes).
			Fix("send less in each message (cut the batch by its size), or raise max_broker_bytes in the [limits] section of metagente.toml (the server has its own limit too)")
	}
	return data, nil
}

// connect reaches the broker the first time, after the approval.
func (b *Broker) connect() (broker.Broker, error) {
	if b.opts.Allow != nil {
		if err := b.opts.Allow(b.spec); err != nil {
			return nil, err
		}
	}
	driver, ok := brokerDrivers[b.conn.Driver]
	if !ok {
		return nil, diag.Newf("this build of Metagente cannot reach the broker `%s`", b.conn.Driver)
	}
	password := ""
	if variable := b.spec.Credential; variable != "" {
		password = b.opts.Getenv(variable)
		if password == "" {
			return nil, diag.Newf("the password of the broker for `%s` is not set: the variable %s is empty", b.decl.Name, variable).
				Fixf("set it in the terminal that runs Metagente, for example: export %s=...", variable)
		}
	}
	key := b.conn.Driver + "\x00" + b.conn.URL + "\x00" + b.conn.User + "\x00" + b.spec.Credential + "\x00" + b.conn.Stream
	if b.conn.Driver == "memory" {
		key = memoryKey(b.conn.Name)
	}
	conn, err := b.opts.Pool.get(key, func() (broker.Broker, error) { return driver(b.conn, password, b.opts.Root) })
	if err != nil {
		return nil, b.failure("", err)
	}
	return conn, nil
}

// failure is the problem of a publication that did not work, with the words of the broker cleaned of the
// secret. A failure that may pass says so, so whoever called may ask again later.
func (b *Broker) failure(subject string, err error) error {
	text := clip.Collapse(secret.Redact(err.Error(), b.secrets()...), 300)
	var d *diag.Diagnostic
	switch {
	case errors.Is(err, broker.ErrNotInBuild):
		return diag.Newf("this build of Metagente was made without the `%s` broker", b.conn.Driver).
			Fix("use a build that has it (the releases do), or build without the tag nojetstream")
	case errors.Is(err, broker.ErrFull):
		d = diag.Newf("the broker is full and did not take the message for `%s`: %s", b.decl.Name, text).
			Fix("wait for the consumers to empty the stream; nothing was lost, and the message can be sent again")
	case errors.Is(err, broker.ErrNoStream):
		d = diag.Newf("no stream of the broker takes the subject `%s`: %s", subject, text).
			Fixf("create a stream for it on the server, or check the subjects and the stream of [broker.%s]", b.conn.Name)
	case errors.Is(err, broker.ErrUnavailable):
		d = diag.Newf("I could not reach the broker of `%s`: %s", b.decl.Name, text).
			Fixf("check the url, the user and the password of [broker.%s], and that this computer may reach the broker", b.conn.Name)
	default:
		d = diag.Newf("the broker did not take the message for `%s`: %s", b.decl.Name, text)
	}
	if broker.MayPass(err) {
		d.WithRetry(0)
	}
	return d
}

func (b *Broker) secrets() []string {
	if variable := b.spec.Credential; variable != "" && b.opts.Getenv != nil {
		if s := b.opts.Getenv(variable); s != "" {
			return []string{s}
		}
	}
	return nil
}

func quoteAll(list []string) string {
	quoted := make([]string, len(list))
	for i, s := range list {
		quoted[i] = `"` + s + `"`
	}
	return strings.Join(quoted, ", ")
}
