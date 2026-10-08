//go:build integration

package integration

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// The image is pinned by digest (see the comment in sql_test.go about METAGENTE_IT_REGISTRY).
const natsImage = "library/nats:2.10-alpine@sha256:b83efabe3e7def1e0a4a31ec6e078999bb17c80363f881df35edc70fcb6bb927"

const (
	natsUser   = "etl"
	natsSecret = "n4ts-s3cret"
)

// natsServer is a NATS server with JetStream, and a connection of the test to it.
type natsServer struct {
	host string
	port int
	js   jetstream.JetStream
	ctr  testcontainers.Container // to pause it (the pilot does)
}

func startNATS(t *testing.T) natsServer {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctr, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image(natsImage),
			ExposedPorts: []string{"4222/tcp"},
			Cmd:          []string{"-js", "--user", natsUser, "--pass", natsSecret},
			WaitingFor:   wait.ForLog("Server is ready"),
		},
		Started: true,
	})
	if ctr != nil {
		testcontainers.CleanupContainer(t, ctr)
	}
	if err != nil {
		t.Fatalf("starting NATS: %v", err)
	}
	s := natsServer{ctr: ctr}
	if s.host, err = ctr.Host(ctx); err != nil {
		t.Fatal(err)
	}
	mapped, err := ctr.MappedPort(ctx, "4222/tcp")
	if err != nil {
		t.Fatal(err)
	}
	s.port, _ = strconv.Atoi(mapped.Port())
	conn, err := nats.Connect(fmt.Sprintf("nats://%s:%d", s.host, s.port), nats.UserInfo(natsUser, natsSecret))
	if err != nil {
		t.Fatalf("the test cannot reach NATS: %v", err)
	}
	t.Cleanup(conn.Close)
	if s.js, err = jetstream.New(conn); err != nil {
		t.Fatal(err)
	}
	return s
}

func (s natsServer) stream(t *testing.T, cfg jetstream.StreamConfig) jetstream.Stream {
	t.Helper()
	stream, err := s.js.CreateStream(context.Background(), cfg)
	if err != nil {
		t.Fatalf("creating the stream: %v", err)
	}
	return stream
}

func (s natsServer) messages(t *testing.T, stream jetstream.Stream) uint64 {
	t.Helper()
	info, err := stream.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return info.State.Msgs
}

const senderAgent = `agent Sender
  goal "Send the batches"
  tool events from broker "main" publish "etl.orders.batch" "etl.orders.control"
  accepts go start
  on go
    for n in [1, 2, 3]
      events.publish subject: "etl.orders.batch" id: "job7:{n}" data: [n, "x"]
    sent = events.publish subject: "etl.orders.control" id: "job7:end" data: "done"
    again = events.publish subject: "etl.orders.control" id: "job7:end" data: "done"
    reply "last {sent.seq}; again is a copy: {again.duplicate}"
`

// brokerProject writes a folder with the configuration of the broker and the agent that publishes.
func (s natsServer) project(t *testing.T, extra string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "metagente.toml"), fmt.Sprintf(`[credentials]
events = "BROKER_PASSWORD"

[broker.main]
driver = "jetstream"
url = "nats://%s:%d"
user = %q
tls = "disable"
%s`, s.host, s.port, natsUser, extra))
	write(t, filepath.Join(dir, "sender.ag"), senderAgent)
	return dir
}

func runSender(t *testing.T, dir, password string) (string, error) {
	t.Helper()
	if out, err := metagente(t, dir, password, "trust", "sender.ag", "--yes"); err != nil {
		t.Fatalf("trust: %v\n%s", err, out)
	}
	return metagente(t, dir, password, "run", "sender.ag", "go", "start=x")
}

func TestAnAgentPublishesBatchesAndTheServerDropsACopy(t *testing.T) {
	s := startNATS(t)
	stream := s.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Discard: jetstream.DiscardNew})
	dir := s.project(t, `stream = "ETL"`)
	out, err := runSender(t, dir, natsSecret)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "last 4; again is a copy: yes") {
		t.Errorf("answer = %s", out)
	}
	if n := s.messages(t, stream); n != 4 {
		t.Errorf("the stream holds %d messages, want 4 (the copy is dropped by the server)", n)
	}
	first, err := stream.GetMsg(context.Background(), 1)
	if err != nil || first.Subject != "etl.orders.batch" || string(first.Data) != `[1,"x"]` || first.Header.Get(jetstream.MsgIDHeader) != "job7:1" {
		t.Errorf("first message = %+v, %v", first, err)
	}
}

func TestAFullStreamRefusesTheNewMessagesAndTheFailureMayPass(t *testing.T) {
	s := startNATS(t)
	stream := s.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Discard: jetstream.DiscardNew, MaxMsgs: 2})
	dir := s.project(t, "")
	out, err := runSender(t, dir, natsSecret)
	if err == nil {
		t.Fatalf("a full stream took everything:\n%s", out)
	}
	for _, want := range []string{"the broker is full", "This may pass"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if n := s.messages(t, stream); n != 2 {
		t.Errorf("the stream holds %d messages, want the 2 it can hold", n)
	}
}

func TestASubjectThatNoStreamTakesIsAFinalFailure(t *testing.T) {
	s := startNATS(t)
	dir := s.project(t, "")
	out, err := runSender(t, dir, natsSecret)
	if err == nil || !strings.Contains(out, "no stream of the broker takes the subject `etl.orders.batch`") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if strings.Contains(out, "This may pass") {
		t.Errorf("a missing stream will not pass by itself:\n%s", out)
	}
}

func TestAMessageForAStreamThatIsNotTheExpectedOneIsRefused(t *testing.T) {
	s := startNATS(t)
	other := s.stream(t, jetstream.StreamConfig{Name: "OTHER", Subjects: []string{"etl.>"}})
	dir := s.project(t, `stream = "ETL"`)
	out, err := runSender(t, dir, natsSecret)
	if err == nil || strings.Contains(out, "This may pass") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if n := s.messages(t, other); n != 0 {
		t.Errorf("%d messages went to the wrong stream", n)
	}
}

func TestAWrongPasswordForTheBrokerIsToldWithoutShowingIt(t *testing.T) {
	s := startNATS(t)
	dir := s.project(t, "")
	out, err := runSender(t, dir, "wrong-"+natsSecret)
	if err == nil || !strings.Contains(out, "I could not reach the broker") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	if strings.Contains(out, natsSecret) {
		t.Errorf("the password is in the answer:\n%s", out)
	}
}

func TestABrokerThatIsDownIsAFailureThatMayPass(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close() // nobody listens there any more
	s := natsServer{host: "127.0.0.1", port: port}
	dir := s.project(t, "")
	out, err := runSender(t, dir, natsSecret)
	if err == nil {
		t.Fatalf("a broker that is down took the messages:\n%s", out)
	}
	for _, want := range []string{"I could not reach the broker", "This may pass"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

// natsMsg builds a message with the id the server uses to drop a copy.
type natsMsg struct {
	Subject string
	Data    []byte
}

func (m *natsMsg) build(id string) *nats.Msg {
	msg := &nats.Msg{Subject: m.Subject, Data: m.Data, Header: nats.Header{}}
	msg.Header.Set(jetstream.MsgIDHeader, id)
	return msg
}
