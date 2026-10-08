//go:build !nosqlite

package consume

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/llm"
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
	for _, name := range []string{"extractor.ag", "worker.ag", "enricher.ag", "sweeper.ag"} {
		p.file[name] = filepath.Join(dir, name)
		text := sampleText(t, name)
		if name == "sweeper.ag" {
			text = strings.Replace(text, `allow "hooks.example.com"`, "allow private", 1) // the webhook of the tests is on this computer
		}
		if err := os.WriteFile(p.file[name], []byte(text), 0o644); err != nil {
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
	return p.workAs("worker.ag", message, subject)
}

// workAs runs one of the agents of the sample as a consumer of one kind of event.
func (p *pipeline) workAs(file, message, subject string) Stats {
	p.t.Helper()
	agents, err := runtime.LoadAgents(p.file[file])
	if err != nil {
		p.t.Fatal(err)
	}
	p.rt.Linker.Register(agents)
	if err := p.rt.AuthorizeWith(agents, nil, p.file[file], func([]trust.Item) bool { return true }); err != nil {
		p.t.Fatal(err)
	}
	cfg := Config{
		Runtime: p.rt, Agent: agents[0], Message: message, Broker: p.mem,
		Spec: broker.ConsumerSpec{Durable: file + "-" + message, Subject: subject, AckWait: 5 * time.Second},
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

func TestPurgeControlCleansOnlyTheJobsThatAreDoneOldAndOutOfStaging(t *testing.T) {
	p := newPipeline(t)
	for _, job := range []string{"c1", "c2", "c3"} {
		p.extract(job, "1000")
	}
	p.batches()
	p.controls()
	db := "dest/warehouse.db"
	for _, job := range []string{"c1", "c2", "c3"} {
		if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = '"+job+"'"); state != "done" {
			t.Fatalf("job %s is %s", job, state)
		}
	}
	// c1 ended long ago, c2 ended long ago but is not done (a person has to look at it), c3 ended just now.
	p.exec(db, "UPDATE etl_jobs SET finished_at = '2020-01-01 00:00:00' WHERE job_id IN ('c1', 'c2')")
	p.exec(db, "UPDATE etl_jobs SET state = 'mismatch' WHERE job_id = 'c2'")
	p.exec(db, "INSERT INTO etl_alerts (job_id, kind, ref) VALUES ('c1', 'BATCH_STUCK', '1'), ('c2', 'BATCH_STUCK', '1')")
	p.exec(db, "INSERT INTO etl_incidents (job_id, seq, code) VALUES ('c1', 1, 'X'), ('c2', 1, 'X')")
	p.exec(db, "INSERT INTO etl_resends (job_id, seq, requests) VALUES ('c1', 1, 1), ('c2', 1, 1)")
	finalRows := p.number(db, "SELECT count(*) FROM orders_final")

	// Staging still has the rows of c1: nothing of it goes while they are there.
	if got := p.message("purge_control", "days=365"); got != "removed 0 finished jobs from the control tables" {
		t.Fatalf("with rows in staging: %q", got)
	}
	p.exec(db, "UPDATE etl_batches SET done_at = '2020-01-01 00:00:00'")
	p.message("purge", "days=7")
	p.exec(db, "DELETE FROM stg_orders WHERE job_id IN ('c2', 'c3')") // the others are not the subject here
	if got := p.message("purge_control", "days=365"); got != "removed 1 finished jobs from the control tables" {
		t.Fatalf("purge_control: %q", got)
	}
	for table, want := range map[string]int{"etl_jobs": 2, "etl_alerts": 1, "etl_incidents": 1, "etl_resends": 1} {
		if n := p.number(db, "SELECT count(*) FROM "+table); n != want {
			t.Errorf("%s has %d rows, and %d were expected", table, n, want)
		}
	}
	for _, table := range []string{"etl_batches", "etl_rejects"} {
		if n := p.number(db, "SELECT count(*) FROM "+table+" WHERE job_id = 'c1'"); n != 0 {
			t.Errorf("%s still has %d rows of the job that was cleaned", table, n)
		}
	}
	if n := p.number(db, "SELECT count(*) FROM etl_batches WHERE job_id = 'c3'"); n == 0 {
		t.Error("etl_batches lost the rows of a job that ended just now")
	}
	if n := p.number(db, "SELECT count(*) FROM orders_final"); n != finalRows {
		t.Errorf("the final table changed: %d rows, and %d were expected", n, finalRows)
	}
}

// ---------- the model step ----------

// labeler is a language model that labels the notes it is given by a word in them, and keeps what it was asked.
type labeler struct {
	mu       sync.Mutex
	asked    []string
	failures []error // the first requests fail with these, in order
	garbage  bool    // answers with words that are not JSON
}

func (l *labeler) Complete(_ context.Context, req *llm.Request) (*llm.Response, error) {
	prompt := req.Messages[0].Parts[0].Text
	l.mu.Lock()
	l.asked = append(l.asked, prompt)
	if len(l.failures) > 0 {
		err := l.failures[0]
		l.failures = l.failures[1:]
		l.mu.Unlock()
		return nil, err
	}
	garbage := l.garbage
	l.mu.Unlock()
	answer := "Sure! I labelled them all, and they were lovely."
	if !garbage {
		var items []struct {
			ID   int    `json:"id"`
			Note string `json:"note"`
		}
		if err := json.Unmarshal([]byte(prompt[strings.Index(prompt, "["):]), &items); err != nil {
			return nil, fmt.Errorf("the labeler could not read its question: %w", err)
		}
		labels := []map[string]any{}
		for _, item := range items {
			category := "other"
			switch {
			case strings.Contains(item.Note, "gift"):
				category = "gift"
			case strings.Contains(item.Note, "neighbour"):
				category = "delivery"
			case strings.Contains(item.Note, "damaged"):
				category = "complaint"
			}
			labels = append(labels, map[string]any{"id": item.ID, "category": category})
		}
		raw, _ := json.Marshal(labels)
		answer = string(raw)
	}
	return &llm.Response{Parts: []llm.Part{{Kind: llm.PartText, Text: answer}}, Stop: llm.StopEnd, Reason: "end_turn", Usage: llm.Usage{Input: 100, Output: 50}}, nil
}

func (l *labeler) calls() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.asked)
}

// message runs one of the messages of the Worker that a person sends: budget, resume, purge.
func (p *pipeline) message(message string, params ...string) string {
	p.t.Helper()
	opts := runtime.Options{File: p.file["worker.ag"], Message: message, Params: params, Confirm: func([]trust.Item) bool { return true }}
	got, err := runtime.RunFile(context.Background(), p.rt, opts)
	if err != nil {
		p.t.Fatalf("%s: %v", message, err)
	}
	return got.Text
}

func withModel(t *testing.T, model *labeler) *pipeline {
	t.Helper()
	p := newPipeline(t)
	p.rt.Model = model
	return p
}

func TestTheModelStepLabelsTheNotesOfAJobThatHasABudgetAndNothingElseGoesToTheModel(t *testing.T) {
	model := &labeler{}
	p := withModel(t, model)
	p.message("budget", "job=m1", "max_calls=1000", "max_tokens=1000000")
	p.extract("m1", "1000")
	if s := p.batches(); s.Done != 3 || s.Dead != 0 {
		t.Fatalf("batches: %+v", s)
	}
	p.controls()
	db := "dest/warehouse.db"
	for query, want := range map[string]int{
		"SELECT count(*) FROM orders_final WHERE note_category = 'gift'":      500,
		"SELECT count(*) FROM orders_final WHERE note_category = 'delivery'":  500,
		"SELECT count(*) FROM orders_final WHERE note_category = 'complaint'": 500,
		"SELECT count(*) FROM orders_final WHERE note_category IS NULL":       1000, // the notes that are empty were not asked
		"SELECT sum(model_calls) FROM etl_batches":                            75,   // 1500 notes, 20 at a time
		"SELECT sum(model_tokens) FROM etl_batches":                           75 * 150,
		"SELECT count(*) FROM etl_batches WHERE enrich_version = 'notes-v1'":  3,
	} {
		if got := p.number(db, query); got != want {
			t.Errorf("%s = %d, want %d", query, got, want)
		}
	}
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 'm1'"); state != "done" {
		t.Errorf("job = %s", state)
	}
	// What leaves for the model is the id and the note, and never the name or the document of a customer.
	if model.calls() != 75 {
		t.Fatalf("the model was asked %d times", model.calls())
	}
	for _, prompt := range model.asked {
		data := prompt[strings.Index(prompt, "["):] // the instructions come before the notes
		if strings.Contains(data, "Customer") || regexp.MustCompile(`\*\*\*\d{4}`).MatchString(data) || strings.Contains(data, "customer") {
			t.Fatalf("something besides the notes went to the model: %.200s", data)
		}
	}
}

func TestAJobWithoutABudgetNeverAsksTheModel(t *testing.T) {
	model := &labeler{}
	p := withModel(t, model)
	p.extract("m2", "1000")
	p.batches()
	if model.calls() != 0 {
		t.Errorf("the model was asked %d times by a job with no budget", model.calls())
	}
	if n := p.number("dest/warehouse.db", "SELECT count(*) FROM orders_final WHERE note_category IS NOT NULL"); n != 0 {
		t.Errorf("%d rows have a category", n)
	}
}

func TestAnAnswerThatIsNotInTheShapeWantedLeavesTheNotesWithoutACategoryAndTheBatchGoesOn(t *testing.T) {
	model := &labeler{garbage: true}
	p := withModel(t, model)
	p.exec("demo/source.db", "DELETE FROM orders WHERE id > 100")
	p.message("budget", "job=m3", "max_calls=1000", "max_tokens=1000000")
	p.extract("m3", "1000")
	if s := p.batches(); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("batches: %+v", s)
	}
	// 60 notes, 3 groups, and each group is tried twice and then left alone.
	if model.calls() != 6 {
		t.Errorf("the model was asked %d times, and 6 were expected", model.calls())
	}
	if n := p.number("dest/warehouse.db", "SELECT count(*) FROM orders_final WHERE note_category IS NOT NULL"); n != 0 {
		t.Errorf("%d rows have a category", n)
	}
	if n := p.number("dest/warehouse.db", "SELECT count(*) FROM orders_final"); n != 100 {
		t.Errorf("orders_final has %d rows", n)
	}
}

func TestAFailureOfTheModelThatMayPassAsksForTheEventAgainAndNothingIsPaidTwice(t *testing.T) {
	model := &labeler{failures: []error{&llm.Error{Message: "answered 429", Status: 429, Retry: true}}}
	p := withModel(t, model)
	p.exec("demo/source.db", "DELETE FROM orders WHERE id > 100")
	p.message("budget", "job=m4", "max_calls=1000", "max_tokens=1000000")
	p.extract("m4", "1000")
	s := p.batches()
	if s.Retried != 1 || s.Done != 1 || s.Dead != 0 {
		t.Fatalf("batches: %+v", s)
	}
	db := "dest/warehouse.db"
	if n := p.number(db, "SELECT sum(model_calls) FROM etl_batches"); n != 3 {
		t.Errorf("the account has %d requests, and 3 groups were labelled", n)
	}
	if n := p.number(db, "SELECT count(*) FROM orders_final WHERE note_category IS NOT NULL"); n != 60 {
		t.Errorf("%d rows have a category", n)
	}
}

func TestWhenTheBudgetIsSpentTheJobIsPausedAndItGoesOnWhereItStoppedWhenTheBudgetIsRaised(t *testing.T) {
	model := &labeler{}
	p := withModel(t, model)
	p.message("budget", "job=m5", "max_calls=10", "max_tokens=1000000")
	p.extract("m5", "1000")
	s := p.batches()
	if s.Done != 0 || s.Dead != 0 || s.Retried != 3 {
		t.Fatalf("batches: %+v (the first is stopped by the budget, the others by the pause)", s)
	}
	db := "dest/warehouse.db"
	if got := p.text(db, "SELECT state || ' ' || pause_reason FROM etl_jobs WHERE job_id = 'm5'"); got != "paused MODEL_BUDGET" {
		t.Errorf("job = %s", got)
	}
	if model.calls() != 10 {
		t.Errorf("the model was asked %d times with a budget of 10", model.calls())
	}
	if n := p.number(db, "SELECT count(*) FROM etl_batches WHERE job_id = 'm5'"); n != 1 {
		t.Errorf("%d batches are landed, and only the first took a turn", n)
	}

	// The person raises the budget and lets the job go on; the events were asked for again later.
	p.message("budget", "job=m5", "max_calls=1000", "max_tokens=1000000")
	if got := p.message("resume", "job=m5"); !strings.Contains(got, "1 paused job is running again") {
		t.Errorf("resume = %q", got)
	}
	p.mem.Advance(2 * time.Hour)
	if s := p.batches(); s.Done != 3 || s.Dead != 0 {
		t.Fatalf("after the resume: %+v", s)
	}
	p.controls()
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 'm5'"); state != "done" {
		t.Errorf("job = %s", state)
	}
	// Nothing was asked twice: the 10 requests of the first try count, and the rest was only what was missing.
	if model.calls() != 75 || p.number(db, "SELECT sum(model_calls) FROM etl_batches") != 75 {
		t.Errorf("the model was asked %d times, and 75 groups exist", model.calls())
	}
}

func TestTheBudgetWarnsAt80PercentAndThePersonCanSeeIt(t *testing.T) {
	model := &labeler{}
	p := withModel(t, model)
	p.message("budget", "job=m6", "max_calls=90", "max_tokens=1000000") // 75 are needed: 83 percent
	p.extract("m6", "1000")
	if s := p.batches(); s.Done != 3 {
		t.Fatalf("batches: %+v", s)
	}
	db := "dest/warehouse.db"
	if got := p.text(db, "SELECT state || ' ' || budget_warned FROM etl_jobs WHERE job_id = 'm6'"); got != "running 1" {
		t.Errorf("job = %s", got)
	}
	// A job that stays far from its budget is not warned.
	p.message("budget", "job=m7", "max_calls=1000", "max_tokens=1000000")
	p.extract("m7", "1000")
	p.batches()
	if got := p.number(db, "SELECT budget_warned FROM etl_jobs WHERE job_id = 'm7'"); got != 0 {
		t.Errorf("budget_warned = %d", got)
	}
}

func TestSeveralBatchesInARowStoppedByTheBrakePauseTheWholeJob(t *testing.T) {
	p := newPipeline(t)
	p.exec("demo/source.db", "UPDATE orders SET total = -1 WHERE id <= 1800 AND id % 10 < 4")
	p.extract("q1", "600")
	s := p.batches()
	if s.Dead != 3 || s.Done != 0 || s.Retried != 2 {
		t.Fatalf("batches: %+v (three stopped by the brake, the other two held back by the pause)", s)
	}
	db := "dest/warehouse.db"
	if got := p.text(db, "SELECT state || ' ' || pause_reason FROM etl_jobs WHERE job_id = 'q1'"); got != "paused QUALITY" {
		t.Errorf("job = %s", got)
	}
	if n := p.number(db, "SELECT count(*) FROM etl_batches WHERE seq > 3"); n != 0 {
		t.Errorf("%d batches took a turn while the job was paused", n)
	}

	// The cause is solved (here, the rows are fixed in staging), the dead letters go again, and the job is resumed.
	p.exec(db, "UPDATE stg_orders SET total = 1 WHERE total < 0")
	for _, m := range p.deadLetters() {
		p.publish(m.Headers["Metagente-Dead-Subject"], m.Headers["Metagente-Dead-Event"]+":again", m.Data)
	}
	p.message("resume", "job=q1")
	p.mem.Advance(2 * time.Hour)
	if s := p.batches(); s.Done != 5 || s.Dead != 0 {
		t.Fatalf("after the resume: %+v", s)
	}
	p.controls()
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 'q1'"); state != "done" {
		t.Errorf("job = %s", state)
	}
	if n := p.number(db, "SELECT count(*) FROM orders_final"); n != 2500 {
		t.Errorf("orders_final has %d rows", n)
	}
}

// ---------- the sweeper ----------

// webhook is the channel of the team: it keeps what it was told.
type webhook struct {
	mu     sync.Mutex
	bodies []string
}

func newWebhook(t *testing.T) *webhook {
	t.Helper()
	w := &webhook{}
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		w.mu.Lock()
		w.bodies = append(w.bodies, string(raw))
		w.mu.Unlock()
	}))
	t.Cleanup(server.Close)
	t.Setenv("ALERT_URL", server.URL)
	return w
}

func (w *webhook) told() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.bodies...)
}

// sweep runs the sweeper once.
func (p *pipeline) sweep(minutes string) string {
	p.t.Helper()
	opts := runtime.Options{File: p.file["sweeper.ag"], Message: "sweep", Params: []string{"minutes=" + minutes}, Confirm: func([]trust.Item) bool { return true }}
	got, err := runtime.RunFile(context.Background(), p.rt, opts)
	if err != nil {
		p.t.Fatalf("sweep: %v", err)
	}
	return got.Text
}

func (p *pipeline) messagesOn(subject string) (out []broker.Message) {
	for _, m := range p.mem.Messages() {
		if m.Subject == subject {
			out = append(out, m)
		}
	}
	return out
}

const longAgo = "2020-01-01 00:00:00"

func TestTheSweeperAsksForAStuckBatchToBeTransformedAgainAndTellsTheTeamOnlyOnce(t *testing.T) {
	hook := newWebhook(t)
	p := newPipeline(t)
	p.extract("s1", "1000")
	p.batches()
	db := "dest/warehouse.db"
	// Batch 2 stayed landed: its event was lost after the first transaction, a long time ago.
	p.exec(db, "UPDATE etl_batches SET state = 'landed', rows_loaded = NULL, rows_rejected = NULL, done_at = NULL, landed_at = ? WHERE seq = 2", longAgo)
	p.exec(db, "DELETE FROM orders_final WHERE id > 1000 AND id <= 2000")

	if got := p.sweep("5"); got != "1 causes looked at, 1 batches stuck, 0 jobs with batches missing" {
		t.Errorf("sweep = %q", got)
	}
	notices := p.messagesOn("etl.s1.retransform")
	if len(notices) != 1 || notices[0].ID != "s1:2:retransform:0" {
		t.Fatalf("notices = %+v", notices)
	}
	if told := hook.told(); len(told) != 1 || !strings.Contains(told[0], "ETL BATCH_STUCK job s1 2") {
		t.Fatalf("the team was told %v", told)
	}
	// A second sweep, before the Worker did anything: another notice (it counts the tries), and the team is not told again.
	p.sweep("5")
	if len(hook.told()) != 1 {
		t.Errorf("the team was told again: %v", hook.told())
	}
	if n := len(p.messagesOn("etl.s1.retransform")); n != 2 {
		t.Errorf("%d notices after two sweeps", n)
	}

	// The Worker takes the notices: the batch is transformed again, and the copy of the notice only confirms.
	if s := p.work("retransform", "etl.*.retransform"); s.Done != 2 || s.Dead != 0 {
		t.Fatalf("retransform: %+v", s)
	}
	if state := p.text(db, "SELECT state FROM etl_batches WHERE seq = 2"); state != "done" {
		t.Errorf("batch 2 = %s", state)
	}
	if n := p.number(db, "SELECT count(*) FROM orders_final"); n != 2500 {
		t.Errorf("orders_final has %d rows", n)
	}
	// Nothing is stuck now, and nothing new is told.
	if got := p.sweep("5"); !strings.Contains(got, "0 batches stuck") || len(hook.told()) != 1 {
		t.Errorf("after: %q, told %v", got, hook.told())
	}
}

func TestTheSweeperAsksTheExtractorForABatchThatNeverArrivedAndTheJobCloses(t *testing.T) {
	newWebhook(t)
	p := newPipeline(t)
	p.extract("s2", "1000")
	p.mem.Lose("s2:2") // the broker lost an event
	if s := p.batches(); s.Done != 2 {
		t.Fatalf("batches: %+v", s)
	}
	p.controls()
	db := "dest/warehouse.db"
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 's2'"); state != "running" {
		t.Fatalf("job = %s with a batch missing", state)
	}
	// Not yet: the totals were announced just now, and the batch may still be on its way.
	if got := p.sweep("5"); !strings.Contains(got, "0 jobs with batches missing") {
		t.Errorf("sweep = %q", got)
	}
	p.exec(db, "UPDATE etl_jobs SET totals_at = ?", longAgo)
	if got := p.sweep("5"); !strings.Contains(got, "1 jobs with batches missing") {
		t.Errorf("sweep = %q", got)
	}
	asks := p.messagesOn("etl.s2.resend")
	if len(asks) != 1 || asks[0].ID != "s2:2:resend:1" {
		t.Fatalf("requests = %+v", asks)
	}

	// The window of copies of the broker has passed; the Extractor builds batch 2 again from its outbox and sends it.
	p.mem.Advance(10 * time.Minute)
	if s := p.workAs("extractor.ag", "resend", "etl.*.resend"); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("resend: %+v", s)
	}
	if s := p.batches(); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("batch 2 again: %+v", s)
	}
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 's2'"); state != "done" {
		t.Errorf("job = %s", state)
	}
	if n := p.number(db, "SELECT count(*) FROM orders_final"); n != 2500 {
		t.Errorf("orders_final has %d rows", n)
	}
}

func TestAskingForABatchAgainHasALimitAfterWhichAPersonLooks(t *testing.T) {
	newWebhook(t)
	p := newPipeline(t)
	p.extract("s3", "1000")
	p.mem.Lose("s3:3")
	p.batches()
	p.controls()
	p.exec("dest/warehouse.db", "UPDATE etl_jobs SET totals_at = ?", longAgo)
	for i := 0; i < 8; i++ {
		p.sweep("5")
	}
	if n := len(p.messagesOn("etl.s3.resend")); n != 5 {
		t.Errorf("%d requests for a batch that does not come, and 5 are the limit", n)
	}
	ids := map[string]bool{}
	for _, m := range p.messagesOn("etl.s3.resend") {
		ids[m.ID] = true
	}
	if len(ids) != 5 {
		t.Errorf("the requests share ids: %v", ids)
	}
}

func TestEveryCauseIsToldOnceWithCodesAndCountsAndNeverWithRows(t *testing.T) {
	hook := newWebhook(t)
	p := newPipeline(t)
	db := "dest/warehouse.db"
	p.extract("a1", "1000")
	p.batches() // so that the staging table has rows, and there is something to leak
	p.exec(db, "INSERT INTO etl_jobs (job_id, state, pause_reason) VALUES ('a2', 'paused', 'QUALITY')")
	p.exec(db, "INSERT INTO etl_jobs (job_id, state) VALUES ('a3', 'mismatch')")
	p.exec(db, "UPDATE etl_jobs SET budget_warned = 1 WHERE job_id = 'a1'")
	p.exec(db, "INSERT INTO etl_batches (job_id, seq, state, rows_read) VALUES ('a2', 7, 'failed', 10)")
	p.exec(db, "INSERT INTO etl_incidents (job_id, seq, code) VALUES ('a1', 9, 'HASH_MISMATCH')")

	p.sweep("5")
	told := hook.told()
	want := []string{"ETL BUDGET_80_PERCENT job a1", "ETL EVENT_REFUSED job a1 9 HASH_MISMATCH", "ETL BATCH_FAILED job a2 7", "ETL JOB_PAUSED job a2 QUALITY", "ETL JOB_MISMATCH job a3"}
	if len(told) != len(want) {
		t.Fatalf("the team was told %d things: %v", len(told), told)
	}
	for _, w := range want {
		found := false
		for _, body := range told {
			found = found || strings.Contains(body, w)
		}
		if !found {
			t.Errorf("not told: %q in %v", w, told)
		}
	}
	for _, body := range told {
		if strings.Contains(body, "Customer") || regexp.MustCompile(`\*\*\*\d{4}`).MatchString(body) {
			t.Errorf("an alert carries rows: %s", body)
		}
	}
	p.sweep("5")
	if len(hook.told()) != len(want) {
		t.Errorf("the second sweep told again: %d", len(hook.told()))
	}

	// A person resumes the job that was paused; when it is paused again, that is a new cause and the team is told.
	p.message("resume", "job=a2")
	p.exec(db, "UPDATE etl_jobs SET state = 'paused', pause_reason = 'MODEL_BUDGET' WHERE job_id = 'a2'")
	p.sweep("5")
	if told := hook.told(); len(told) != len(want)+2 { // the pause, and the batch that is still failed, are told again after a resume
		t.Errorf("after the resume: %d things told: %v", len(told), told)
	}
}

func TestADamagedEventIsToldAndTheBatchComesBackByTheSweeper(t *testing.T) {
	hook := newWebhook(t)
	p := newPipeline(t)
	p.extract("s4", "1000")
	// The first event is lost, and a damaged copy of it reaches the Worker under another id.
	first := p.mem.Messages()[0]
	var damaged map[string]any
	if err := json.Unmarshal(first.Data, &damaged); err != nil {
		t.Fatal(err)
	}
	damaged["sha256"] = strings.Repeat("0", 64)
	raw, _ := json.Marshal(damaged)
	p.mem.Lose("s4:1")
	p.publish("etl.s4.batch", "s4:1:damaged", raw)
	if s := p.batches(); s.Done != 2 || s.Dead != 1 {
		t.Fatalf("batches: %+v", s)
	}
	p.controls()
	db := "dest/warehouse.db"
	p.exec(db, "UPDATE etl_jobs SET totals_at = ?", longAgo)
	p.sweep("5")
	if told := hook.told(); len(told) != 1 || !strings.Contains(told[0], "ETL EVENT_REFUSED job s4 1 HASH_MISMATCH") {
		t.Fatalf("the team was told %v", told)
	}
	p.mem.Advance(10 * time.Minute)
	p.workAs("extractor.ag", "resend", "etl.*.resend")
	if s := p.batches(); s.Done != 1 {
		t.Fatalf("the batch again: %+v", s)
	}
	if state := p.text(db, "SELECT state FROM etl_jobs WHERE job_id = 's4'"); state != "done" {
		t.Errorf("job = %s", state)
	}
}
