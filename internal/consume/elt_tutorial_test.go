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

// The Extractor and the Worker that samples/elt-tutorial describes instead of writing: the agents are two
// lines each, and everything they do comes from the description in the settings. They run offline, as
// the ones of samples/async-elt do, with a broker in memory in place of JetStream.

type tutorial struct {
	t         *testing.T
	dir       string
	extractor *runtime.Runtime
	worker    *runtime.Runtime
}

func tutorialText(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", "elt-tutorial", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// edit is a change to a file of the tutorial before the test uses it.
type edit struct{ file, old, new string }

func newTutorial(t *testing.T, edits ...edit) *tutorial {
	t.Helper()
	dir := t.TempDir()
	tut := &tutorial{t: t, dir: dir}
	toMemory := func(text string) string {
		for _, line := range []string{`(?m)^url = .*\n`, `(?m)^stream = .*\n`} {
			text = regexp.MustCompile(line).ReplaceAllString(text, "")
		}
		return strings.Replace(text, `driver = "jetstream"`, `driver = "memory"`, 1)
	}
	for name, text := range map[string]string{
		"extractor.toml": toMemory(tutorialText(t, "extractor.toml")), "worker.toml": toMemory(tutorialText(t, "worker.toml")),
		"extractor.ag": tutorialText(t, "extractor.ag"), "worker.ag": tutorialText(t, "worker.ag"),
	} {
		for _, e := range edits {
			if e.file == name {
				if !strings.Contains(text, e.old) {
					t.Fatalf("%s has no %q to change", name, e.old)
				}
				text = strings.Replace(text, e.old, e.new, 1)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for db, migrations := range map[string][]string{
		"source.db": {"source.sql"}, "outbox.db": {"outbox.sql"}, "warehouse.db": {"control.sql", "orders.sql"},
	} {
		conn, err := sql.Open("sqlite", filepath.Join(dir, db))
		if err != nil {
			t.Fatal(err)
		}
		for _, migration := range migrations {
			if _, err := conn.Exec(tutorialText(t, filepath.Join("migrations", migration))); err != nil {
				t.Fatalf("%s: %v", migration, err)
			}
		}
		conn.Close()
	}
	tut.extractor = tut.runtime("extractor.toml")
	tut.worker = tut.runtime("worker.toml")
	return tut
}

func (tut *tutorial) runtime(settings string) *runtime.Runtime {
	tut.t.Helper()
	cfg, err := config.Load(tut.dir, filepath.Join(tut.dir, settings))
	if err != nil {
		tut.t.Fatal(err)
	}
	rt := runtime.New(cfg)
	rt.Trust = trust.NewRegistry(filepath.Join(tut.t.TempDir(), "approvals"))
	tut.t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func (tut *tutorial) db(name string) *sql.DB {
	tut.t.Helper()
	conn, err := sql.Open("sqlite", filepath.Join(tut.dir, name))
	if err != nil {
		tut.t.Fatal(err)
	}
	tut.t.Cleanup(func() { conn.Close() })
	return conn
}

func (tut *tutorial) number(name, query string, args ...any) (n int) {
	tut.t.Helper()
	if err := tut.db(name).QueryRow(query, args...).Scan(&n); err != nil {
		tut.t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (tut *tutorial) text(name, query string, args ...any) (s string) {
	tut.t.Helper()
	if err := tut.db(name).QueryRow(query, args...).Scan(&s); err != nil {
		tut.t.Fatalf("%s: %v", query, err)
	}
	return s
}

func (tut *tutorial) extract(job string) string {
	tut.t.Helper()
	opts := runtime.Options{File: filepath.Join(tut.dir, "extractor.ag"), Message: "extract", Params: []string{"job=" + job},
		Confirm: func([]trust.Item) bool { return true }}
	reply, err := runtime.RunFile(context.Background(), tut.extractor, opts)
	if err != nil {
		tut.t.Fatal(err)
	}
	return reply.Display()
}

// carry hands the events that the Extractor published to the broker of the Worker, as the network would.
func (tut *tutorial) carry() {
	tut.t.Helper()
	to := tut.worker.Broker.Memory("main")
	for _, m := range tut.extractor.Broker.Memory("main").Messages() {
		if _, err := to.Publish(context.Background(), broker.Message{Subject: m.Subject, ID: m.ID, Data: m.Data}); err != nil {
			tut.t.Fatal(err)
		}
	}
}

// work runs the Worker on one kind of event until the broker has nothing more for it.
func (tut *tutorial) work(message, subject string) Stats {
	tut.t.Helper()
	agents, err := tut.worker.LoadAgents(filepath.Join(tut.dir, "worker.ag"))
	if err != nil {
		tut.t.Fatal(err)
	}
	tut.worker.Linker.Register(agents)
	if err := tut.worker.AuthorizeWith(agents, nil, "worker.ag", func([]trust.Item) bool { return true }); err != nil {
		tut.t.Fatal(err)
	}
	cfg := Config{
		Runtime: tut.worker, Agent: agents[0], Message: message, Broker: tut.worker.Broker.Memory("main"),
		Spec: broker.ConsumerSpec{Durable: "worker-" + message, Subject: subject, AckWait: 5 * time.Second},
		Dead: "etl.dead", Backoff: []time.Duration{5 * time.Millisecond}, MaxDeliver: 3, BreakerAfter: 100,
		FetchWait: 20 * time.Millisecond, IdleExit: 200 * time.Millisecond,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	stats, err := Run(ctx, cfg)
	if err != nil {
		tut.t.Fatal(err)
	}
	return stats
}

func TestTheTutorialCopiesTheTableAndTheBooksClose(t *testing.T) {
	tut := newTutorial(t)
	if got := tut.extract("demo"); got != "job demo: 3 batches, 2500 rows" {
		t.Fatalf("the Extractor said %q", got)
	}
	tut.carry()
	if s := tut.work("batch", "etl.*.batch"); s.Taken != 3 || s.Done != 3 || s.Dead != 0 {
		t.Fatalf("batches: %+v", s)
	}
	if s := tut.work("control", "etl.*.control"); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("control: %+v", s)
	}
	if state := tut.text("warehouse.db", "SELECT state FROM etl_jobs WHERE job_id = 'demo'"); state != "done" {
		t.Errorf("the job is %s", state)
	}
	if n := tut.number("warehouse.db", "SELECT count(*) FROM orders_final"); n != 2495 {
		t.Errorf("%d rows were loaded, want 2495", n)
	}
	if n := tut.number("warehouse.db", "SELECT count(*) FROM etl_rejects WHERE reason_code = 'TOTAL_NEGATIVE'"); n != 5 {
		t.Errorf("%d rows were rejected, want 5", n)
	}
	// Only the last four digits left the source, and the transformation turned the total into cents.
	if got := tut.text("warehouse.db", "SELECT document_tail || ' ' || total_cents || ' ' || loaded_job FROM orders_final WHERE id = 1"); got != "7919 1150 demo" {
		t.Errorf("order 1 is %q", got)
	}
	if n := tut.number("warehouse.db", "SELECT count(*) FROM stg_orders WHERE document LIKE '%0%' AND document NOT LIKE '***%'"); n != 0 {
		t.Errorf("%d documents reached staging unmasked", n)
	}
	// Running it again finds everything done: the same events, and nothing new to load.
	if got := tut.extract("demo"); got != "job demo: 3 batches, 2500 rows" {
		t.Errorf("the second run said %q", got)
	}
}

func (tut *tutorial) deadLetters() (out []broker.Message) {
	for _, m := range tut.worker.Broker.Memory("main").Messages() {
		if m.Subject == "etl.dead" {
			out = append(out, m)
		}
	}
	return out
}

func (tut *tutorial) exec(name, statement string) {
	tut.t.Helper()
	if _, err := tut.db(name).Exec(statement); err != nil {
		tut.t.Fatalf("%s: %v", statement, err)
	}
}

// message sends the Worker one of its messages, as `metagente run worker.ag MESSAGE key=value` would.
func (tut *tutorial) message(message string, params ...string) {
	tut.t.Helper()
	opts := runtime.Options{File: filepath.Join(tut.dir, "worker.ag"), Message: message, Params: params, Confirm: func([]trust.Item) bool { return true }}
	if _, err := runtime.RunFile(context.Background(), tut.worker, opts); err != nil {
		tut.t.Fatal(err)
	}
}

func (tut *tutorial) publish(subject, id string, data []byte) {
	tut.t.Helper()
	if _, err := tut.worker.Broker.Memory("main").Publish(context.Background(), broker.Message{Subject: subject, ID: id, Data: data}); err != nil {
		tut.t.Fatal(err)
	}
}

func TestABatchWithTooManyRejectedRowsIsStoppedAndCanBeTransformedAgainWithTheTutorial(t *testing.T) {
	tut := newTutorial(t)
	tut.exec("source.db", "UPDATE orders SET total = -1 WHERE id <= 300")
	tut.extract("j3")
	tut.carry()
	if s := tut.work("batch", "etl.*.batch"); s.Done != 2 || s.Dead != 1 {
		t.Fatalf("batches: %+v", s)
	}
	if state := tut.text("warehouse.db", "SELECT state || ' ' || last_error_code FROM etl_batches WHERE seq = 1"); state != "failed TOO_MANY_REJECTS" {
		t.Errorf("batch 1 = %s", state)
	}
	dead := tut.deadLetters()
	if len(dead) != 1 || !strings.Contains(dead[0].Headers["Metagente-Dead-Reason"], "more than 20 percent") {
		t.Fatalf("dead letters = %+v", dead)
	}
	if strings.Contains(dead[0].Headers["Metagente-Dead-Reason"], "Customer") {
		t.Error("the reason of the dead letter shows content")
	}
	// A person fixes the rows in staging, and sends the event of the dead letter again: the data is already there.
	tut.exec("warehouse.db", "UPDATE stg_orders SET total = 1 WHERE seq = 1 AND total < 0")
	tut.publish("etl.j3.batch", "j3:1:again", dead[0].Data)
	if s := tut.work("batch", "etl.*.batch"); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("again: %+v", s)
	}
	tut.work("control", "etl.*.control")
	if state := tut.text("warehouse.db", "SELECT state FROM etl_jobs WHERE job_id = 'j3'"); state != "done" {
		t.Errorf("job = %s", state)
	}
	// The other batches reject their own negative totals (orders 1500, 2000 and 2500) as they always do.
	if n := tut.number("warehouse.db", "SELECT count(*) FROM orders_final"); n != 2497 {
		t.Errorf("orders_final has %d rows", n)
	}
}

func TestSeveralBatchesInARowStoppedByTheBrakePauseTheJobAndItGoesOnWhenItIsResumedWithTheTutorial(t *testing.T) {
	tut := newTutorial(t, edit{"extractor.toml", `table = "orders"`, "table = \"orders\"\nrows = 600"})
	tut.exec("source.db", "UPDATE orders SET total = -1 WHERE id <= 1800 AND id % 10 < 4")
	tut.extract("q1")
	tut.carry()
	if s := tut.work("batch", "etl.*.batch"); s.Dead != 3 || s.Done != 0 || s.Retried != 2 {
		t.Fatalf("batches: %+v (three stopped by the brake, the other two held back by the pause)", s)
	}
	if got := tut.text("warehouse.db", "SELECT state || ' ' || pause_reason FROM etl_jobs WHERE job_id = 'q1'"); got != "paused QUALITY" {
		t.Errorf("job = %s", got)
	}
	tut.exec("warehouse.db", "UPDATE stg_orders SET total = 1 WHERE total < 0")
	for _, m := range tut.deadLetters() {
		tut.publish(m.Headers["Metagente-Dead-Subject"], m.Headers["Metagente-Dead-Event"]+":again", m.Data)
	}
	tut.message("resume", "job=q1")
	tut.worker.Broker.Memory("main").Advance(2 * time.Hour)
	if s := tut.work("batch", "etl.*.batch"); s.Done != 5 || s.Dead != 0 {
		t.Fatalf("after the resume: %+v", s)
	}
	tut.work("control", "etl.*.control")
	if state := tut.text("warehouse.db", "SELECT state FROM etl_jobs WHERE job_id = 'q1'"); state != "done" {
		t.Errorf("job = %s", state)
	}
}

func TestACopyOfAnEventAddsNothingAndADamagedEventLandsNothingWithTheTutorial(t *testing.T) {
	tut := newTutorial(t)
	tut.extract("j4")
	tut.carry()
	tut.work("batch", "etl.*.batch")
	before := tut.number("warehouse.db", "SELECT count(*) FROM orders_final")
	first := tut.worker.Broker.Memory("main").Messages()[0]
	tut.publish(first.Subject, "j4:1:copy", first.Data)
	if s := tut.work("batch", "etl.*.batch"); s.Done != 1 || s.Dead != 0 {
		t.Fatalf("copy: %+v", s)
	}
	if after := tut.number("warehouse.db", "SELECT count(*) FROM orders_final"); after != before {
		t.Errorf("a copy changed the destination: %d rows, was %d", after, before)
	}
	var damaged map[string]any
	if err := json.Unmarshal(first.Data, &damaged); err != nil {
		t.Fatal(err)
	}
	damaged["job_id"], damaged["seq"], damaged["sha256"] = "j5", 1, strings.Repeat("0", 64)
	raw, _ := json.Marshal(damaged)
	tut.publish("etl.j5.batch", "j5:1", raw)
	if s := tut.work("batch", "etl.*.batch"); s.Done != 0 || s.Dead != 1 {
		t.Fatalf("damaged: %+v", s)
	}
	if n := tut.number("warehouse.db", "SELECT count(*) FROM stg_orders WHERE job_id = 'j5'"); n != 0 {
		t.Errorf("%d rows of a damaged event landed", n)
	}
	if got := tut.text("warehouse.db", "SELECT code FROM etl_incidents WHERE job_id = 'j5'"); got != "HASH_MISMATCH" {
		t.Errorf("incident = %s", got)
	}
}

func TestTheExtractorOfTheTutorialSendsABatchAgainWhenTheDestinationAsksForIt(t *testing.T) {
	tut := newTutorial(t)
	tut.extract("j6")
	opts := runtime.Options{File: filepath.Join(tut.dir, "extractor.ag"), Message: "resend", Params: []string{"v=1", "job_id=j6", "seq=2"},
		Confirm: func([]trust.Item) bool { return true }}
	reply, err := runtime.RunFile(context.Background(), tut.extractor, opts)
	if err != nil {
		t.Fatal(err)
	}
	if got := reply.Display(); got != "batch 2 of j6 sent again with 1000 rows" {
		t.Errorf("the Extractor said %q", got)
	}
}
