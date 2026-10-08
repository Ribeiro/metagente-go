//go:build !nojetstream

package broker

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// JetStreamAvailable says whether this build has the JetStream broker (a build made with -tags
// nojetstream leaves it out).
const JetStreamAvailable = true

// JetStreamConfig says where a JetStream server is and how to reach it.
type JetStreamConfig struct {
	// URL is the address of the server, like nats://host:4222 or tls://host:4222.
	URL string
	// User and Secret are the user and the password; with no User, Secret is a token. Both may be empty.
	User, Secret string
	// TLS is "verify", "require" or "disable" (see config.TLSVerify).
	TLS string
	// CAFile is the file with the certificates to trust, for "verify"; empty means those of the system.
	CAFile string
	// Stream, when it is set, is the stream that has to take what is published; a message that no other
	// stream would take is refused.
	Stream string
}

// publishTimeout is how long a publication waits for the broker when the caller set no time of its own.
const publishTimeout = 15 * time.Second

type jetStream struct {
	conn   *nats.Conn
	js     jetstream.JetStream
	stream string
}

// DialJetStream connects to a JetStream server.
func DialJetStream(cfg JetStreamConfig) (Broker, error) {
	opts := []nats.Option{
		nats.Name("metagente"),
		nats.Timeout(10 * time.Second),
		// A message is never kept in memory while the connection is down: the publication fails, and the
		// caller decides when to try again.
		nats.ReconnectBufSize(-1),
	}
	switch {
	case cfg.User != "":
		opts = append(opts, nats.UserInfo(cfg.User, cfg.Secret))
	case cfg.Secret != "":
		opts = append(opts, nats.Token(cfg.Secret))
	}
	switch cfg.TLS {
	case "disable":
	case "require":
		opts = append(opts, nats.Secure(&tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true})) //nolint:gosec // asked for with tls = "require"
	default:
		config := &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.CAFile != "" {
			pem, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("I could not read the certificates in ca_file: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("ca_file has no certificate in PEM form: %s", cfg.CAFile)
			}
			config.RootCAs = pool
		}
		opts = append(opts, nats.Secure(config))
	}
	conn, err := nats.Connect(cfg.URL, opts...)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("%w: %s", ErrUnavailable, err)
	}
	return &jetStream{conn: conn, js: js, stream: cfg.Stream}, nil
}

func (b *jetStream) Publish(ctx context.Context, m Message) (PubAck, error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, publishTimeout)
		defer cancel()
	}
	msg := &nats.Msg{Subject: m.Subject, Data: m.Data}
	if m.ID != "" || len(m.Headers) > 0 {
		msg.Header = nats.Header{}
		for name, v := range m.Headers {
			msg.Header.Set(name, v)
		}
		if m.ID != "" {
			msg.Header.Set(jetstream.MsgIDHeader, m.ID)
		}
	}
	opts := []jetstream.PublishOpt{jetstream.WithRetryAttempts(2), jetstream.WithRetryWait(250 * time.Millisecond)}
	if b.stream != "" {
		opts = append(opts, jetstream.WithExpectStream(b.stream))
	}
	ack, err := b.js.PublishMsg(ctx, msg, opts...)
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return PubAck{}, err
		}
		return PubAck{}, classify(err)
	}
	return PubAck{Stream: ack.Stream, Seq: ack.Sequence, Duplicate: ack.Duplicate}, nil
}

// Consume makes the consumer on the server, or finds the one of that name, and gives its messages.
func (b *jetStream) Consume(ctx context.Context, spec ConsumerSpec) (Consumer, error) {
	if spec.Durable == "" {
		return nil, fmt.Errorf("%w: a consumer needs a name", ErrRefused)
	}
	stream := spec.Stream
	if stream == "" {
		found, err := b.js.StreamNameBySubject(ctx, spec.Subject)
		if err != nil {
			return nil, classify(err)
		}
		stream = found
	}
	cfg := jetstream.ConsumerConfig{
		Durable:       spec.Durable,
		FilterSubject: spec.Subject,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		AckWait:       spec.AckWait,
		MaxAckPending: spec.MaxInFlight,
	}
	if spec.MaxDeliver > 0 {
		// One more than the program uses, so that the delivery in which the program gives up still happens.
		cfg.MaxDeliver = spec.MaxDeliver + 1
	}
	cons, err := b.js.CreateOrUpdateConsumer(ctx, stream, cfg)
	if err != nil {
		return nil, classify(err)
	}
	return &jsConsumer{cons: cons}, nil
}

type jsConsumer struct{ cons jetstream.Consumer }

func (c *jsConsumer) Close() error { return nil }

func (c *jsConsumer) Fetch(ctx context.Context, n int, wait time.Duration) ([]Delivery, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	batch, err := c.cons.Fetch(n, jetstream.FetchMaxWait(wait))
	if err != nil {
		return nil, classify(err)
	}
	var out []Delivery
	for msg := range batch.Messages() {
		meta, err := msg.Metadata()
		if err != nil {
			continue
		}
		headers := map[string]string{}
		for name := range msg.Headers() {
			headers[name] = msg.Headers().Get(name)
		}
		m := Message{Subject: msg.Subject(), ID: headers[jetstream.MsgIDHeader], Data: msg.Data(), Headers: headers}
		out = append(out, &jsDelivery{msg: msg, m: m, seq: meta.Sequence.Stream, attempt: int(meta.NumDelivered)})
	}
	if err := batch.Error(); err != nil && len(out) == 0 && !errors.Is(err, nats.ErrTimeout) {
		return nil, classify(err)
	}
	return out, nil
}

type jsDelivery struct {
	msg     jetstream.Msg
	m       Message
	seq     uint64
	attempt int
}

func (d *jsDelivery) Message() Message { return d.m }
func (d *jsDelivery) Seq() uint64      { return d.seq }
func (d *jsDelivery) Attempt() int     { return d.attempt }

// Ack waits for the server to say that it got the confirmation, so a confirmation that was lost is known.
func (d *jsDelivery) Ack(ctx context.Context) error { return answer(d.msg.DoubleAck(ctx)) }

func (d *jsDelivery) Nak(_ context.Context, delay time.Duration) error {
	if delay <= 0 {
		return answer(d.msg.Nak())
	}
	return answer(d.msg.NakWithDelay(delay))
}

func (d *jsDelivery) Term(_ context.Context, reason string) error {
	return answer(d.msg.TermWithReason(reason))
}

func (d *jsDelivery) InProgress(_ context.Context) error { return answer(d.msg.InProgress()) }

func answer(err error) error {
	if err == nil {
		return nil
	}
	return classify(err)
}

func (b *jetStream) Close() error {
	b.conn.Close()
	return nil
}

// classify puts the failure of a publication into one of the kinds of this package, keeping its words.
func classify(err error) error {
	var api *jetstream.APIError
	text := err.Error()
	switch {
	case errors.Is(err, jetstream.ErrNoStreamResponse):
		return fmt.Errorf("%w: no stream answered for this subject", ErrNoStream)
	case errors.As(err, &api):
		switch {
		case api.ErrorCode == jetstream.JSErrCodeStreamNotFound || strings.Contains(strings.ToLower(api.Description), "expected stream"):
			return fmt.Errorf("%w: %s", ErrNoStream, api.Description)
		case strings.Contains(api.Description, "maximum") || strings.Contains(api.Description, "insufficient resources"):
			return fmt.Errorf("%w: %s", ErrFull, api.Description)
		case api.Code >= 500:
			return fmt.Errorf("%w: %s", ErrUnavailable, api.Description)
		}
		return fmt.Errorf("%w: %s", ErrRefused, api.Description)
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, nats.ErrTimeout), errors.Is(err, nats.ErrNoResponders),
		errors.Is(err, nats.ErrConnectionClosed), errors.Is(err, nats.ErrDisconnected), errors.Is(err, nats.ErrNoServers),
		errors.Is(err, nats.ErrReconnectBufExceeded), errors.Is(err, nats.ErrConnectionReconnecting):
		return fmt.Errorf("%w: %s", ErrUnavailable, text)
	case errors.Is(err, nats.ErrMaxPayload):
		return fmt.Errorf("%w: the message is larger than the server takes", ErrRefused)
	}
	return fmt.Errorf("%w: %s", ErrRefused, text)
}
