//go:build !nosqlite

package consume

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/runtime"
	"github.com/Ribeiro/metagente-go/internal/trust"
)

// The Extractor and the Worker of the asynchronous ELT (samples/async-elt), run offline with the real files of
// the sample: a source, an outbox and a destination in SQLite, and a broker in memory in place of JetStream.

type pipeline struct {
	t    *testing.T
	dir  string
	rt   *runtime.Runtime
	mem  *broker.Memory
	file map[string]string
}

func sampleText(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", "async-elt", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func newPipeline(t *testing.T) *pipeline {
	t.Helper()
	dir := t.TempDir()
	toml := sampleText(t, "metagente.toml")
	for _, line := range []string{`(?m)^url = .*\n`, `(?m)^stream = .*\n`} {
		toml = regexp.MustCompile(line).ReplaceAllString(toml, "")
	}
	toml = strings.Replace(toml, `driver = "jetstream"`, `driver = "memory"`, 1)
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &pipeline{t: t, dir: dir, file: map[string]string{}}
	for path, migration := range map[string]string{
		"demo/source.db": "migrations/demo-source.sql", "state/outbox.db": "migrations/outbox.sql", "dest/warehouse.db": "migrations/destination.sqlite.sql",
	} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		db := p.open(path)
		if _, err := db.Exec(sampleText(t, migration)); err != nil {
			t.Fatalf("%s: %v", migration, err)
		}
		db.Close()
	}
	for _, name := range []string{"extractor.ag", "worker.ag"} {
		p.file[name] = filepath.Join(dir, name)
		if err := os.WriteFile(p.file[name], []byte(sampleText(t, name)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	p.rt = runtime.New(cfg)
	p.rt.Trust = trust.NewRegistry(filepath.Join(t.TempDir(), "approvals"))
	t.Cleanup(func() { _ = p.rt.Close() })
	p.mem = p.rt.Broker.Memory("main")
	return p
}

func (p *pipeline) open(path string) *sql.DB {
	p.t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(p.dir, path))
	if err != nil {
		p.t.Fatal(err)
	}
	return db
}

// exec changes a database of the pipeline behind its back, as a person or another program would.
func (p *pipeline) exec(path, statement string, args ...any) {
	p.t.Helper()
	db := p.open(path)
	defer db.Close()
	if _, err := db.Exec(statement, args...); err != nil {
		p.t.Fatalf("%s: %v", statement, err)
	}
}

func (p *pipeline) number(path, query string, args ...any) (n int) {
	p.t.Helper()
	db := p.open(path)
	defer db.Close()
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		p.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (p *pipeline) text(path, query string, args ...any) (s string) {
	p.t.Helper()
	db := p.open(path)
	defer db.Close()
	if err := db.QueryRow(query, args...).Scan(&s); err != nil {
		p.t.Fatalf("%s: %v", query, err)
	}
	return s
}

func (p *pipeline) extract(job, size string) {
	p.t.Helper()
	opts := runtime.Options{File: p.file["extractor.ag"], Message: "extract", Params: []string{"job=" + job, "size=" + size, "bytes=5000000"},
		Confirm: func([]trust.Item) bool { return true }}
	if _, err := runtime.RunFile(context.Background(), p.rt, opts); err != nil {
		p.t.Fatal(err)
	}
}

// work runs the Worker on one kind of event until the broker has nothing more for it.
func (p *pipeline) work(message, subject string) Stats {
	p.t.Helper()
	agents, err := runtime.LoadAgents(p.file["worker.ag"])
	if err != nil {
		p.t.Fatal(err)
	}
	p.rt.Linker.Register(agents)
	if err := p.rt.AuthorizeWith(agents, nil, p.file["worker.ag"], func([]trust.Item) bool { return true }); err != nil {
		p.t.Fatal(err)
	}
	cfg := Config{
		Runtime: p.rt, Agent: agents[0], Message: message, Broker: p.mem,
		Spec: broker.ConsumerSpec{Durable: "worker-" + message, Subject: subject, AckWait: 5 * time.Second},
		Dead: "etl.dead", Backoff: []time.Duration{5 * time.Millisecond}, MaxDeliver: 3, BreakerAfter: 100,
		FetchWait: 20 * time.Millisecond, IdleExit: 200 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stats, err := Run(ctx, cfg)
	if err != nil {
		p.t.Fatal(err)
	}
	return stats
}

func (p *pipeline) batches() Stats  { return p.work("batch", "etl.*.batch") }
func (p *pipeline) controls() Stats { return p.work("control", "etl.*.control") }

func (p *pipeline) deadLetters() (out []broker.Message) {
	for _, m := range p.mem.Messages() {
		if m.Subject == "etl.dead" {
			out = append(out, m)
		}
	}
	return out
}

func (p *pipeline) publish(subject, id string, data []byte) {
	p.t.Helper()
	if _, err := p.mem.Publish(context.Background(), broker.Message{Subject: subject, ID: id, Data: data}); err != nil {
		p.t.Fatal(err)
	}
}

func TestTheWorkerLandsTransformsAndClosesAJobThatTheExtractorSent(t *testing.T) {
	p := newPipeline(t)
	p.extract("j1", "1000")
	if s := p.batches(); s.Taken != 3 || s.Done != 3 || s.Dead != 0 {
		t.Fatalf("batches: %+v", s)
	}
	if state := p.text("dest/warehouse.db", "SELECT state FROM etl_jobs WHERE job_id = 'j1'"); state != "running" {
		t.Errorf("the job is %s before the totals are known", state)
	}
	if s := p.controls(); s.Done != 1 {
		t.Fatalf("control: %+v", s)
	}
	db := "dest/warehouse.db"
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 'j1'"); state != "done" {
		t.Errorf("job = %s", state)
	}
	for query, want := range map[string]int{
		"SELECT count(*) FROM orders_final":                                 2500,
		"SELECT count(*) FROM stg_orders":                                   2500,
		"SELECT count(*) FROM etl_batches WHERE state = 'done'":             3,
		"SELECT sum(rows_loaded) FROM etl_batches":                          2500,
		"SELECT coalesce(sum(rows_rejected), 0) FROM etl_batches":           0,
		"SELECT count(*) FROM etl_rejects":                                  0,
		"SELECT total_batches FROM etl_jobs WHERE job_id = 'j1'":            3,
		"SELECT total_rows FROM etl_jobs WHERE job_id = 'j1'":               2500,
		"SELECT total_cents FROM orders_final WHERE id = 100":               13700,
		"SELECT count(*) FROM orders_final WHERE length(document_tail) = 4": 2500,
	} {
		if got := p.number(db, query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
}

func TestRowsThatBreakARuleAreRejectedWithTheirKeyAndACodeAndTheJobGoesOn(t *testing.T) {
	p := newPipeline(t)
	p.exec("demo/source.db", "UPDATE orders SET total = -1 WHERE id <= 10")
	p.exec("demo/source.db", "UPDATE orders SET customer = '  ' WHERE id BETWEEN 11 AND 15")
	p.extract("j2", "1000")
	if s := p.batches(); s.Done != 3 || s.Dead != 0 {
		t.Fatalf("batches: %+v", s)
	}
	p.controls()
	db := "dest/warehouse.db"
	for query, want := range map[string]int{
		"SELECT count(*) FROM orders_final": 2485,
		"SELECT count(*) FROM etl_rejects":  15,
		"SELECT count(*) FROM etl_rejects WHERE reason_code = 'TOTAL_NEGATIVE' AND source_key <= 10": 10,
		"SELECT count(*) FROM etl_rejects WHERE reason_code = 'CUSTOMER_EMPTY' AND source_key > 10":  5,
		"SELECT sum(rows_rejected) FROM etl_batches":                                                 15,
		"SELECT sum(rows_loaded) FROM etl_batches":                                                   2485,
	} {
		if got := p.number(db, query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 'j2'"); state != "done" {
		t.Errorf("job = %s: the rows read, loaded and rejected add up", state)
	}
	// A rejected row leaves its key and a code, and nothing else.
	cols := p.text(db, "SELECT group_concat(name) FROM pragma_table_info('etl_rejects')")
	if cols != "job_id,seq,source_key,reason_code" {
		t.Errorf("etl_rejects has %s", cols)
	}
}

func TestABatchWithTooManyRejectedRowsIsStoppedAndCanBeTransformedAgain(t *testing.T) {
	p := newPipeline(t)
	p.exec("demo/source.db", "UPDATE orders SET total = -1 WHERE id <= 300")
	p.extract("j3", "1000")
	s := p.batches()
	if s.Done != 2 || s.Dead != 1 {
		t.Fatalf("batches: %+v", s)
	}
	db := "dest/warehouse.db"
	if state := p.text(db, "SELECT state || ' ' || last_error_code FROM etl_batches WHERE seq = 1"); state != "failed TOO_MANY_REJECTS" {
		t.Errorf("batch 1 = %s", state)
	}
	if n := p.number(db, "SELECT count(*) FROM etl_rejects WHERE seq = 1"); n != 0 {
		t.Errorf("the brake stopped the batch, and %d rejects were written", n)
	}
	dead := p.deadLetters()
	if len(dead) != 1 || dead[0].Headers["Metagente-Dead-Reason"] == "" || !strings.Contains(dead[0].Headers["Metagente-Dead-Reason"], "more than 20 percent") {
		t.Fatalf("dead letters = %+v", dead)
	}
	if strings.Contains(dead[0].Headers["Metagente-Dead-Reason"], "Customer") {
		t.Error("the reason of the dead letter shows content")
	}
	p.controls()
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 'j3'"); state != "running" {
		t.Errorf("a job with a batch that failed is %s, and it should wait", state)
	}

	// A person fixes the rows in staging, and sends the event of the dead letter again: the data is already there.
	p.exec(db, "UPDATE stg_orders SET total = 1 WHERE seq = 1 AND total < 0")
	p.publish("etl.j3.batch", "j3:1:again", dead[0].Data)
	if s := p.batches(); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("again: %+v", s)
	}
	p.controls()
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 'j3'"); state != "done" {
		t.Errorf("job = %s after the batch was transformed again", state)
	}
	if n := p.number(db, "SELECT count(*) FROM orders_final"); n != 2500 {
		t.Errorf("orders_final has %d rows", n)
	}
}

func TestACopyOfAnEventAddsNothingAndADamagedEventLandsNothing(t *testing.T) {
	p := newPipeline(t)
	p.extract("j4", "1000")
	p.batches()
	db := "dest/warehouse.db"
	before := p.number(db, "SELECT count(*) FROM orders_final")

	// The same event, under another id, so that the broker does not drop it: the batch is done, so it is confirmed.
	first := p.mem.Messages()[0]
	p.publish(first.Subject, "j4:1:copy", first.Data)
	if s := p.batches(); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("copy: %+v", s)
	}
	if after := p.number(db, "SELECT count(*) FROM orders_final"); after != before || p.number(db, "SELECT count(*) FROM stg_orders") != 2500 {
		t.Errorf("a copy changed the destination: %d rows, was %d", after, before)
	}

	// A batch that was never landed, with a hash that is not the one of its rows: a final failure, nothing lands.
	var damaged map[string]any
	if err := json.Unmarshal(first.Data, &damaged); err != nil {
		t.Fatal(err)
	}
	damaged["job_id"], damaged["seq"], damaged["sha256"] = "j5", 1, strings.Repeat("0", 64)
	raw, _ := json.Marshal(damaged)
	p.publish("etl.j5.batch", "j5:1", raw)
	if s := p.batches(); s.Done != 0 || s.Dead != 1 {
		t.Fatalf("damaged: %+v", s)
	}
	if n := p.number(db, "SELECT count(*) FROM etl_batches WHERE job_id = 'j5'") + p.number(db, "SELECT count(*) FROM stg_orders WHERE job_id = 'j5'"); n != 0 {
		t.Errorf("%d rows of a damaged event landed", n)
	}
	if reason := p.deadLetters()[0].Headers["Metagente-Dead-Reason"]; !strings.Contains(reason, "does not match") {
		t.Errorf("reason = %q", reason)
	}
}

func TestABatchThatWasLandedGoesStraightToTheTransformationWithoutTheDataOfTheEvent(t *testing.T) {
	p := newPipeline(t)
	p.extract("j6", "1000")
	p.batches()
	db := "dest/warehouse.db"
	// Go back to the state of a Worker that stopped between the two transactions: landed, not transformed.
	p.exec(db, "DELETE FROM orders_final")
	p.exec(db, "DELETE FROM etl_rejects")
	p.exec(db, "UPDATE etl_batches SET state = 'landed', rows_loaded = NULL, rows_rejected = NULL, done_at = NULL WHERE seq = 2")
	// The event comes again, and its data is damaged: the Worker does not need it, the batch is landed already.
	var event map[string]any
	if err := json.Unmarshal(p.mem.Messages()[1].Data, &event); err != nil {
		t.Fatal(err)
	}
	event["sha256"], event["payload"] = strings.Repeat("0", 64), "not the data"
	raw, _ := json.Marshal(event)
	p.publish("etl.j6.batch", "j6:2:again", raw)
	if s := p.batches(); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("again: %+v", s)
	}
	if got := p.text(db, "SELECT state FROM etl_batches WHERE seq = 2"); got != "done" {
		t.Errorf("batch 2 = %s", got)
	}
	if n := p.number(db, "SELECT count(*) FROM orders_final"); n != 1000 {
		t.Errorf("orders_final has %d rows, and the 1000 of batch 2 were expected", n)
	}
}

func TestTheWorkerRefusesAControlTablesOfAnotherVersionAndPurgesWhatIsDone(t *testing.T) {
	p := newPipeline(t)
	p.extract("j7", "1000")
	p.batches()
	db := "dest/warehouse.db"
	p.exec(db, "UPDATE etl_batches SET done_at = '2020-01-01 00:00:00' WHERE seq <= 2")
	opts := runtime.Options{File: p.file["worker.ag"], Message: "purge", Params: []string{"days=7"}, Confirm: func([]trust.Item) bool { return true }}
	got, err := runtime.RunFile(context.Background(), p.rt, opts)
	if err != nil || got.Text != "removed 2000 rows from staging" {
		t.Fatalf("purge: %q, %v", got.Text, err)
	}
	if n := p.number(db, "SELECT count(*) FROM stg_orders"); n != 500 {
		t.Errorf("staging has %d rows, and the 500 of the batch that is not old were expected", n)
	}
	if n := p.number(db, "SELECT count(*) FROM orders_final"); n != 2500 {
		t.Errorf("the purge touched the final table: %d", n)
	}

	p.exec(db, "UPDATE etl_meta SET schema_version = 2")
	p.extract("j8", "1000")
	if s := p.batches(); s.Done != 0 || s.Dead != 3 {
		t.Fatalf("version 2: %+v", s)
	}
	if reason := p.deadLetters()[0].Headers["Metagente-Dead-Reason"]; !strings.Contains(reason, "version 2 of the control tables") {
		t.Errorf("reason = %q", reason)
	}
}
