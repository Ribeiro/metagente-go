//go:build integration

package integration

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	_ "modernc.org/sqlite"
)

// The Extractor and the Worker that a description makes (samples/elt-tutorial): the agents are two lines each, a
// source in SQLite, a real JetStream server, and a real PostgreSQL, SQL Server or Oracle as the destination. The
// statements of the Worker are made for each of them by the description, and this is where they meet the database.

const eltCheckAgent = `agent Check
  goal "Count what the Worker wrote"
  tool dest from sql "check"
  accepts count start
  on count
    loaded = dest.loaded
    rejected = dest.rejected
    state = dest.state
    done = dest.done_batches
    tail = dest.tail
    reply "final {loaded}; rejected {rejected}; job {state}; batches done {done}; order 1 {tail}"
`

const eltCheckStatements = `
[sql.check]
driver = %q
host = %q
port = %d
database = %q
user = %q
tls = "disable"

[sql.check.statements]
loaded = { sql = "SELECT count(*) FROM orders_final", result = "value" }
rejected = { sql = "SELECT count(*) FROM etl_rejects", result = "value" }
state = { sql = "SELECT state FROM etl_jobs WHERE job_id = 'j1'", result = "value" }
done_batches = { sql = "SELECT count(*) FROM etl_batches WHERE state = 'done'", result = "value" }
tail = { sql = "SELECT %s FROM orders_final WHERE id = 1", result = "value" }
`

// eltDialect is what the description says in the SQL of each database: the expressions of the destination, the rule that
// rejects an empty customer (an empty text is nothing in Oracle), and how to put the final row in one text.
type eltDialect struct{ document, cents, empty, tail string }

var eltDialects = map[string]eltDialect{
	"postgres": {"right(document, 4)", "CAST(round(CAST(total * 100 AS numeric)) AS BIGINT)", "trim(customer) = ''",
		"document_tail || ' ' || total_cents || ' ' || loaded_job"},
	"sqlserver": {"RIGHT(document, 4)", "CAST(ROUND(total * 100, 0) AS BIGINT)", "TRIM(customer) = ''",
		"document_tail + ' ' + CAST(total_cents AS NVARCHAR(30)) + ' ' + loaded_job"},
	"oracle": {"substr(document, -4)", "CAST(round(total * 100) AS NUMBER(19))", "customer IS NULL",
		"document_tail || ' ' || total_cents || ' ' || loaded_job"},
}

func eltText(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "samples", "elt-tutorial", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// eltProject writes the folder of both machines: the settings of the tutorial, pointed at the servers.
func eltProject(t *testing.T, nats natsServer, db server) string {
	t.Helper()
	dir := t.TempDir()
	broker := func(toml string) string {
		for _, line := range []string{`(?m)^url = .*\n`, `(?m)^stream = .*\n`, `(?m)^driver = "jetstream"\n`} {
			toml = regexp.MustCompile(line).ReplaceAllString(toml, "")
		}
		toml = strings.Replace(toml, "[broker.main]\n", fmt.Sprintf("[broker.main]\ndriver = \"jetstream\"\nurl = \"nats://%s:%d\"\nuser = %q\ntls = \"disable\"\nstream = \"ETL\"\n", nats.host, nats.port, natsUser), 1)
		return toml + "\n[credentials]\nevents = \"BROKER_PASSWORD\"\nmain = \"BROKER_PASSWORD\"\ndest = \"DEST_DB_PASSWORD\"\ncheck = \"DEST_DB_PASSWORD\"\n"
	}
	write(t, filepath.Join(dir, "extractor.toml"), broker(eltText(t, "extractor.toml")))
	worker := eltText(t, "worker.toml")
	worker = strings.Replace(worker, "[sql.dest]\ndriver = \"sqlite\"\npath = \"warehouse.db\"", fmt.Sprintf("[sql.dest]\ndriver = %q\nhost = %q\nport = %d\ndatabase = %q\nuser = %q\ntls = \"disable\"", db.driver, db.host, db.port, db.database(), db.user()), 1)
	// The expressions are the ones of the database.
	dialect := eltDialects[db.driver]
	worker = strings.Replace(worker, `substr(document, -4)`, dialect.document, 1)
	worker = strings.Replace(worker, `CAST(round(total * 100) AS INTEGER)`, dialect.cents, 1)
	worker = strings.Replace(worker, `when = "trim(customer) = ''"`, `when = "`+dialect.empty+`"`, 1)
	write(t, filepath.Join(dir, "worker.toml"), broker(worker)+fmt.Sprintf(eltCheckStatements, db.driver, db.host, db.port, db.database(), db.user(), dialect.tail))
	write(t, filepath.Join(dir, "extractor.ag"), eltText(t, "extractor.ag"))
	write(t, filepath.Join(dir, "worker.ag"), eltText(t, "worker.ag"))
	write(t, filepath.Join(dir, "check.ag"), eltCheckAgent)
	for path, migration := range map[string]string{"source.db": "migrations/source.sql", "outbox.db": "migrations/outbox.sql"} {
		conn, err := sql.Open("sqlite", filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(eltText(t, migration)); err != nil {
			t.Fatalf("%s: %v", migration, err)
		}
		conn.Close()
	}
	return dir
}

func TestADescriptionMakesAnExtractorAndAWorkerThatCopyTheTableIntoEveryDatabase(t *testing.T) {
	for _, driver := range databases {
		t.Run(driver, func(t *testing.T) { copiesTheTable(t, driver) })
	}
}

func copiesTheTable(t *testing.T, driver string) {
	nats := startNATS(t)
	nats.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Storage: jetstream.MemoryStorage, Discard: jetstream.DiscardNew, MaxMsgs: 1000, Duplicates: time.Second})
	db := startWith(t, driver, seedFile(t, driver, eltText(t, "migrations/control."+driver+".sql")+"\n"+eltText(t, "migrations/orders."+driver+".sql")))
	dir := eltProject(t, nats, db)

	run := func(args ...string) string {
		t.Helper()
		out, err := worker(t, dir, args...)
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	if out := run("check", "extractor.ag", "--config", "extractor.toml"); !strings.Contains(out, "No problems found") {
		t.Errorf("check of the Extractor:\n%s", out)
	}
	run("trust", "extractor.ag", "--config", "extractor.toml", "--yes")
	run("trust", "check.ag", "--config", "worker.toml", "--yes")
	if out := run("run", "extractor.ag", "extract", "job=j1", "--config", "extractor.toml"); !strings.Contains(out, "job j1: 3 batches, 2500 rows") {
		t.Fatalf("the Extractor:\n%s", out)
	}
	consume := func(subject, message string) string {
		t.Helper()
		run("trust", "worker.ag", "--config", "worker.toml", "--from", "main", "--subject", subject, "--dead", "etl.dead", "--yes")
		return run("consume", "worker.ag", "--config", "worker.toml", "--from", "main", "--subject", subject, "--dead", "etl.dead",
			"--message", message, "--idle-exit", "3", "--durable", "it-"+message)
	}
	if out := consume("etl.*.batch", "batch"); !strings.Contains(out, "Taken 3: done 3, asked for again 0, dead letters 0") {
		t.Errorf("batches:\n%s", out)
	}
	if out := consume("etl.*.control", "control"); !strings.Contains(out, "Taken 1: done 1") {
		t.Errorf("control:\n%s", out)
	}
	if out := run("run", "check.ag", "count", "start=x", "--config", "worker.toml"); !strings.Contains(out, "final 2495; rejected 5; job done; batches done 3; order 1 7919 1150 j1") {
		t.Errorf("check:\n%s", out)
	}
	// Staging and the control tables are cleaned by the messages that the description gave the Worker.
	if out := run("run", "worker.ag", "purge", "days=0", "--config", "worker.toml"); !strings.Contains(out, "removed 2500 rows from staging") {
		t.Errorf("purge:\n%s", out)
	}
	if out := run("run", "worker.ag", "purge_control", "days=0", "--config", "worker.toml"); !strings.Contains(out, "removed 1 finished jobs from the control tables") {
		t.Errorf("purge_control:\n%s", out)
	}
}
