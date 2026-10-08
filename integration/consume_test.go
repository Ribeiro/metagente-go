//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
)

const workerAgent = `agent Worker
  goal "Write a batch to a file"
  tool file
  accepts batch n text
  accepts busy
  accepts bad
  on batch
    file.write path: "batch-{n}.txt" text: text
    reply "ok"
  on busy
    fail "the destination is busy" retry
  on bad
    fail "a rule of the business"
`

// workerProject writes a folder with the configuration of the broker and the agent that consumes.
func (s natsServer) workerProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "metagente.toml"), fmt.Sprintf(`[credentials]
main = "BROKER_PASSWORD"

[broker.main]
driver = "jetstream"
url = "nats://%s:%d"
user = %q
tls = "disable"
stream = "ETL"
`, s.host, s.port, natsUser))
	write(t, filepath.Join(dir, "worker.ag"), workerAgent)
	return dir
}

func (s natsServer) publish(t *testing.T, subject, id, data string) {
	t.Helper()
	msg := &natsMsg{Subject: subject, Data: []byte(data)}
	if _, err := s.js.PublishMsg(context.Background(), msg.build(id)); err != nil {
		t.Fatalf("publishing: %v", err)
	}
}

// consumeArgs are the words of a consume that ends by itself when the stream is empty.
func consumeArgs(subject, message string, extra ...string) []string {
	args := []string{"consume", "worker.ag", "--from", "main", "--subject", subject, "--dead", "etl.orders.dead",
		"--message", message, "--idle-exit", "2", "--durable", "it-" + message}
	return append(args, extra...)
}

func approveConsume(t *testing.T, dir, subject string) {
	t.Helper()
	out, err := metagente(t, dir, natsSecret, "trust", "worker.ag", "--from", "main", "--subject", subject, "--dead", "etl.orders.dead", "--yes")
	if err != nil {
		t.Fatalf("trust: %v\n%s", err, out)
	}
}

func lastDeadLetter(t *testing.T, stream jetstream.Stream) *jetstream.RawStreamMsg {
	t.Helper()
	msg, err := stream.GetLastMsgForSubject(context.Background(), "etl.orders.dead")
	if err != nil {
		t.Fatalf("no dead letter: %v", err)
	}
	return msg
}

func TestEventsOfAStreamAreGivenToTheAgentAndConfirmed(t *testing.T) {
	s := startNATS(t)
	stream := s.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Discard: jetstream.DiscardNew})
	for i := 1; i <= 3; i++ {
		s.publish(t, "etl.orders.batch", fmt.Sprintf("job7:%d", i), fmt.Sprintf(`{"n": %d, "text": "rows of batch %d"}`, i, i))
	}
	dir := s.workerProject(t)
	approveConsume(t, dir, "etl.orders.batch")
	out, err := metagente(t, dir, natsSecret, consumeArgs("etl.orders.batch", "batch", "--in-flight", "2")...)
	if err != nil {
		t.Fatalf("consume: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Taken 3: done 3, asked for again 0, dead letters 0") {
		t.Errorf("summary:\n%s", out)
	}
	for i := 1; i <= 3; i++ {
		got, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("batch-%d.txt", i)))
		if err != nil || string(got) != fmt.Sprintf("rows of batch %d", i) {
			t.Errorf("batch %d: %q, %v", i, got, err)
		}
	}
	cons, err := s.js.Consumer(context.Background(), "ETL", "it-batch")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := cons.Info(context.Background())
	if info.NumPending != 0 || info.NumAckPending != 0 || info.AckFloor.Stream != 3 {
		t.Errorf("the consumer: pending %d, waiting for ack %d, ack floor %d", info.NumPending, info.NumAckPending, info.AckFloor.Stream)
	}
	_ = stream
}

func TestAFailureThatMayPassIsDeliveredAgainAndThenBecomesADeadLetter(t *testing.T) {
	s := startNATS(t)
	stream := s.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Discard: jetstream.DiscardNew})
	s.publish(t, "etl.orders.busy", "job7:1", `{}`)
	dir := s.workerProject(t)
	approveConsume(t, dir, "etl.orders.busy")
	out, err := metagente(t, dir, natsSecret, consumeArgs("etl.orders.busy", "busy", "--max-deliver", "3", "--backoff", "200ms,200ms", "--breaker-after", "10")...)
	if err != nil {
		t.Fatalf("consume: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Taken 3: done 0, asked for again 2, dead letters 1") {
		t.Errorf("summary:\n%s", out)
	}
	dead := lastDeadLetter(t, stream)
	if dead.Header.Get("Metagente-Dead-Attempts") != "3" || dead.Header.Get("Metagente-Dead-Event") != "job7:1" ||
		!strings.Contains(dead.Header.Get("Metagente-Dead-Reason"), "gave up after 3 deliveries") || dead.Header.Get("Metagente-Dead-Subject") != "etl.orders.busy" {
		t.Errorf("dead letter headers: %v", dead.Header)
	}
}

func TestAFinalFailureAndAnEventTheAgentDoesNotTakeAreDeadLettersAtOnce(t *testing.T) {
	s := startNATS(t)
	stream := s.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Discard: jetstream.DiscardNew})
	s.publish(t, "etl.orders.bad", "job7:1", `{}`)
	s.publish(t, "etl.orders.batch", "job7:2", `{"n": 2}`) // the text is missing
	dir := s.workerProject(t)
	approveConsume(t, dir, "etl.orders.bad")
	approveConsume(t, dir, "etl.orders.batch")
	for _, c := range []struct{ message, event, reason string }{
		{"bad", "job7:1", "a rule of the business"},
		{"batch", "job7:2", "text"},
	} {
		out, err := metagente(t, dir, natsSecret, consumeArgs("etl.orders."+c.message, c.message)...)
		if err != nil || !strings.Contains(out, "dead letters 1") {
			t.Fatalf("%s: %v\n%s", c.message, err, out)
		}
		dead := lastDeadLetter(t, stream)
		if dead.Header.Get("Metagente-Dead-Event") != c.event || dead.Header.Get("Metagente-Dead-Attempts") != "1" ||
			!strings.Contains(dead.Header.Get("Metagente-Dead-Reason"), c.reason) {
			t.Errorf("%s: dead letter headers: %v", c.message, dead.Header)
		}
	}
}

func TestWhatConsumeReachesNeedsTheApprovalOfWhoRunsIt(t *testing.T) {
	s := startNATS(t)
	s.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}})
	dir := s.workerProject(t)
	out, err := metagente(t, dir, natsSecret, consumeArgs("etl.orders.batch", "batch")...)
	if err == nil || !strings.Contains(out, "have not approved") ||
		!strings.Contains(out, "metagente trust worker.ag --from main --subject etl.orders.batch --dead etl.orders.dead") {
		t.Fatalf("err = %v\n%s", err, out)
	}
	approveConsume(t, dir, "etl.orders.batch")
	if out, err := metagente(t, dir, natsSecret, consumeArgs("etl.orders.batch", "batch")...); err != nil {
		t.Fatalf("after the approval: %v\n%s", err, out)
	}
	// Another subject is another thing to approve.
	out, err = metagente(t, dir, natsSecret, consumeArgs("etl.orders.control", "batch")...)
	if err == nil || !strings.Contains(out, "have not approved") {
		t.Errorf("a subject that was not approved was read: %v\n%s", err, out)
	}
}

func TestTheConsumerEndsOnSIGTERMAndDoesNotLoseWhatItHolds(t *testing.T) {
	s := startNATS(t)
	s.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}})
	dir := s.workerProject(t)
	approveConsume(t, dir, "etl.orders.batch")
	cmd := exec.Command(binary, "consume", "worker.ag", "--from", "main", "--subject", "etl.orders.batch", "--dead", "etl.orders.dead",
		"--message", "batch", "--durable", "it-term")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "BROKER_PASSWORD="+natsSecret, "METAGENTE_CONFIG_DIR="+approvals(t, dir))
	var output strings.Builder
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond) // it is reading, and nothing comes
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil || !strings.Contains(output.String(), "Taken 0") {
			t.Errorf("it ended with %v:\n%s", err, output.String())
		}
	case <-time.After(15 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("it did not end:\n%s", output.String())
	}
}
