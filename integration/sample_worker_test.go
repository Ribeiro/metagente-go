//go:build integration

package integration

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

// The Worker of the asynchronous ELT (samples/async-elt) with the PostgreSQL configuration of the sample,
// against a real PostgreSQL and a real JetStream server. The Extractor fills the stream from a SQLite source,
// then the Worker lands the batches, transforms them and closes the job, through the commands a person types.

const checkAgent = `agent Check
  goal "Count what the Worker wrote"
  tool dest from sql "dest"
  accepts count start
  on count
    loaded = dest.loaded
    rejected = dest.rejected
    state = dest.state
    done = dest.done_batches
    reply "final {loaded}; rejected {rejected}; job {state}; batches done {done}"
`

// worker runs the program the way the machine of the Worker has it: the password of the database and the one of
// the broker are two variables.
func worker(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DEST_DB_PASSWORD="+dbSecret, "BROKER_PASSWORD="+natsSecret, "METAGENTE_CONFIG_DIR="+approvals(t, dir))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (s natsServer) postgresWorkerProject(t *testing.T, extractor string, pg server) {
	t.Helper()
	toml := sampleText(t, "metagente.postgres.toml")
	toml = strings.Replace(toml, `host = "db.example.com"`, fmt.Sprintf("host = %q\nport = %d\ntls = \"disable\"", pg.host, pg.port), 1)
	toml = strings.Replace(toml, `database = "warehouse"`, fmt.Sprintf("database = %q", dbName), 1)
	toml = regexp.MustCompile(`(?m)^user = "worker"$`).ReplaceAllString(toml, fmt.Sprintf("user = %q", dbUser))
	toml = strings.Replace(toml, `url = "nats://127.0.0.1:4222"`, fmt.Sprintf("url = \"nats://%s:%d\"\nuser = %q\ntls = \"disable\"", s.host, s.port, natsUser), 1)
	toml = regexp.MustCompile(`(?m)^# events = .*\n`).ReplaceAllString(toml, "main = \"BROKER_PASSWORD\"\n")
	toml = strings.Replace(toml, "[sql.dest.transactions]", `loaded = { sql = "SELECT count(*) FROM orders_final", result = "value" }
rejected = { sql = "SELECT count(*) FROM etl_rejects", result = "value" }
state = { sql = "SELECT state FROM etl_jobs WHERE job_id = 'j1'", result = "value" }
done_batches = { sql = "SELECT count(*) FROM etl_batches WHERE state = 'done'", result = "value" }

[sql.dest.transactions]`, 1)
	write(t, filepath.Join(extractor, "worker.postgres.toml"), toml)
	write(t, filepath.Join(extractor, "worker.ag"), sampleText(t, "worker.ag"))
	write(t, filepath.Join(extractor, "check.ag"), checkAgent)
}

func TestTheWorkerLandsTransformsAndClosesAJobInPostgreSQL(t *testing.T) {
	nats := startNATS(t)
	nats.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Storage: jetstream.MemoryStorage, Discard: jetstream.DiscardNew, MaxMsgs: 1000})
	pg := start(t, "postgres")
	dir := nats.extractorProject(t)
	nats.postgresWorkerProject(t, dir, pg)
	password := func(args ...string) (string, error) {
		return metagente(t, dir, natsSecret, args...)
	}
	// The Extractor fills the stream.
	if out, err := password("trust", "extractor.ag", "--yes"); err != nil {
		t.Fatalf("trust: %v\n%s", err, out)
	}
	if out, err := password("run", "extractor.ag", "extract", "job=j1", "size=1000", "bytes=5000000"); err != nil || !strings.Contains(out, "job j1: 3 batches, 2500 rows") {
		t.Fatalf("extractor (err = %v):\n%s", err, out)
	}
	// The Worker, with the configuration for PostgreSQL: the batches first, then the control event.
	run := func(subject, message string) string {
		approve := []string{"trust", "worker.ag", "--config", "worker.postgres.toml", "--from", "main", "--subject", subject, "--dead", "etl.dead", "--yes"}
		if out, err := worker(t, dir, approve...); err != nil {
			t.Fatalf("trust: %v\n%s", err, out)
		}
		out, err := worker(t, dir, "consume", "worker.ag", "--config", "worker.postgres.toml", "--from", "main",
			"--subject", subject, "--dead", "etl.dead", "--message", message, "--idle-exit", "3", "--durable", "it-"+message)
		if err != nil {
			t.Fatalf("consume %s: %v\n%s", message, err, out)
		}
		return out
	}
	if out := run("etl.*.batch", "batch"); !strings.Contains(out, "Taken 3: done 3, asked for again 0, dead letters 0") {
		t.Errorf("batches:\n%s", out)
	}
	if out := run("etl.*.control", "control"); !strings.Contains(out, "Taken 1: done 1") {
		t.Errorf("control:\n%s", out)
	}
	if out, err := worker(t, dir, "trust", "check.ag", "--config", "worker.postgres.toml", "--yes"); err != nil {
		t.Fatalf("trust check: %v\n%s", err, out)
	}
	out, err := worker(t, dir, "run", "check.ag", "count", "start=x", "--config", "worker.postgres.toml")
	if err != nil || !strings.Contains(out, "final 2500; rejected 0; job done; batches done 3") {
		t.Errorf("check (err = %v):\n%s", err, out)
	}
}
