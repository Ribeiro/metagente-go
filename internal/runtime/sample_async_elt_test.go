//go:build !nosqlite

package runtime

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/broker"
	"github.com/Ribeiro/metagente-go/internal/diag"
)

// The Extractor of the asynchronous ELT (samples/async-elt), run offline with the real files of the sample:
// a demo source of 2500 rows and a real outbox, both SQLite, and a broker in memory in place of JetStream.

func sampleFile(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "samples", "async-elt", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// stageExtractor copies the sample into a folder of its own, makes the two databases the way the README
// says (with the migrations of the sample), and swaps JetStream for a broker in memory.
func stageExtractor(t *testing.T) (rt *Runtime, file string) {
	t.Helper()
	dir := t.TempDir()
	toml := sampleFile(t, "metagente.toml")
	for _, line := range []string{`(?m)^url = .*\n`, `(?m)^stream = .*\n`} {
		toml = regexp.MustCompile(line).ReplaceAllString(toml, "")
	}
	toml = strings.Replace(toml, `driver = "jetstream"`, `driver = "memory"`, 1)
	for path, migration := range map[string]string{"demo/source.db": "migrations/demo-source.sql", "state/outbox.db": "migrations/outbox.sql"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(sampleFile(t, migration)); err != nil {
			t.Fatalf("%s: %v", migration, err)
		}
		db.Close()
	}
	file = filepath.Join(dir, "extractor.ag")
	if err := os.WriteFile(file, []byte(sampleFile(t, "extractor.ag")), 0o644); err != nil {
		t.Fatal(err)
	}
	return sqlRuntime(t, dir, toml, filepath.Join(t.TempDir(), "approvals")), file
}

func extract(rt *Runtime, file, job, size, bytes string) (string, error) {
	got, err := RunFile(context.Background(), rt, Options{File: file, Message: "extract", Params: []string{"job=" + job, "size=" + size, "bytes=" + bytes}, Confirm: approveAll})
	return got.Text, err
}

// event is a batch as the Worker reads it.
type event struct {
	V        int      `json:"v"`
	EventID  string   `json:"event_id"`
	JobID    string   `json:"job_id"`
	Seq      int      `json:"seq"`
	After    int      `json:"after"`
	Upto     int      `json:"upto"`
	Columns  []string `json:"columns"`
	RowCount int      `json:"row_count"`
	Encoding string   `json:"encoding"`
	Payload  string   `json:"payload"`
	SHA256   string   `json:"sha256"`
}

// unpack checks the hash and gives the rows of an event, the way the Worker does.
func unpack(t *testing.T, msg broker.Message) (event, [][]any, string) {
	t.Helper()
	var e event
	if err := json.Unmarshal(msg.Data, &e); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if sum := sha256.Sum256(body); hex.EncodeToString(sum[:]) != e.SHA256 {
		t.Errorf("batch %d: the hash does not match", e.Seq)
	}
	var rows [][]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatal(err)
	}
	return e, rows, string(body)
}

// batches are the messages of the subject of batches, and control the one of the control.
func split(msgs []broker.Message) (batches []broker.Message, control []broker.Message) {
	for _, m := range msgs {
		if strings.HasSuffix(m.Subject, ".batch") {
			batches = append(batches, m)
		} else {
			control = append(control, m)
		}
	}
	return
}

// covers checks that the batches, in order, hold every key from 1 to total once, with no gap and no copy.
func covers(t *testing.T, batches []broker.Message, total int) (rows int) {
	t.Helper()
	next := 1
	for i, msg := range batches {
		n := checkBatch(t, i, msg, next)
		next += n
		rows += n
	}
	if next-1 != total {
		t.Errorf("the batches end at key %d, and the table has %d", next-1, total)
	}
	return rows
}

// checkBatch checks one batch, which has to begin at the key first, and gives how many rows it holds.
func checkBatch(t *testing.T, i int, msg broker.Message, first int) int {
	t.Helper()
	e, table, body := unpack(t, msg)
	if e.Seq != i+1 || msg.ID != fmt.Sprintf("%s:%d", e.JobID, e.Seq) || e.V != 1 || e.Encoding != "json+gzip" || e.RowCount != len(table) {
		t.Fatalf("batch %d: %+v (id %s)", i+1, e, msg.ID)
	}
	if strings.Join(e.Columns, ",") != "id,customer,document,total,note" {
		t.Errorf("columns = %v", e.Columns)
	}
	if e.After != first-1 {
		t.Errorf("batch %d starts after %d, and %d was expected: a gap or a copy", e.Seq, e.After, first-1)
	}
	for k, row := range table {
		if int(row[0].(float64)) != first+k {
			t.Fatalf("batch %d: key %v where %d was expected", e.Seq, row[0], first+k)
		}
		if doc := row[2].(string); !strings.HasPrefix(doc, "***") || len(doc) != 7 {
			t.Errorf("the document was not masked at the source: %q", doc)
		}
	}
	if e.Upto != first+len(table)-1 {
		t.Errorf("batch %d says it ends at %d, and it ends at %d", e.Seq, e.Upto, first+len(table)-1)
	}
	if matched, _ := regexp.MatchString(`\d{11}`, body); matched {
		t.Errorf("batch %d carries a whole document number", e.Seq)
	}
	return len(table)
}

func TestTheExtractorSendsTheWholeTableInBatchesThatCarryNoWholeDocument(t *testing.T) {
	rt, file := stageExtractor(t)
	answer, err := extract(rt, file, "j1", "1000", "5000000")
	if err != nil || answer != "job j1: 3 batches, 2500 rows" {
		t.Fatalf("got %q, %v", answer, errText(t, err))
	}
	batches, control := split(rt.Broker.Memory("main").Messages())
	if len(batches) != 3 || len(control) != 1 || batches[0].Subject != "etl.j1.batch" || control[0].Subject != "etl.j1.control" || control[0].ID != "j1:control" {
		t.Fatalf("messages = %d batches, %d control", len(batches), len(control))
	}
	if rows := covers(t, batches, 2500); rows != 2500 {
		t.Errorf("rows = %d", rows)
	}
	var totals map[string]any
	if err := json.Unmarshal(control[0].Data, &totals); err != nil || totals["batches"] != float64(3) || totals["rows"] != float64(2500) || totals["job_id"] != "j1" {
		t.Errorf("control = %s, %v", control[0].Data, err)
	}
}

func TestABatchThatIsTooBigIsHalvedUntilItFits(t *testing.T) {
	rt, file := stageExtractor(t)
	answer, err := extract(rt, file, "j2", "2000", "20000")
	if err != nil {
		t.Fatal(errText(t, err))
	}
	batches, _ := split(rt.Broker.Memory("main").Messages())
	if len(batches) < 6 || !strings.HasPrefix(answer, fmt.Sprintf("job j2: %d batches, 2500 rows", len(batches))) {
		t.Errorf("answer = %q, %d batches", answer, len(batches))
	}
	covers(t, batches, 2500)
	for _, msg := range batches {
		if e, _, body := unpack(t, msg); len(body) > 20000 {
			t.Errorf("batch %d carries %d bytes before it is packed, over the 20000 asked", e.Seq, len(body))
		}
	}
}

func TestAfterAStopTheExtractorSendsWhatWasPlannedAndNothingTwice(t *testing.T) {
	rt, file := stageExtractor(t)
	rt.Broker.Memory("main").SetDown(true)
	_, err := extract(rt, file, "j3", "1000", "5000000")
	if err == nil {
		t.Fatal("the broker was down and the run went on")
	}
	if _, may := diag.RetryOf(err); !may {
		t.Errorf("a broker that is down is a failure that may pass: %v", errText(t, err))
	}
	if n := len(rt.Broker.Memory("main").Messages()); n != 0 {
		t.Fatalf("%d messages went out while the broker was down", n)
	}
	rt.Broker.Memory("main").SetDown(false)

	// The same job again: the batch that was planned goes out, with the edges it was planned with.
	answer, err := extract(rt, file, "j3", "1000", "5000000")
	if err != nil || answer != "job j3: 3 batches, 2500 rows" {
		t.Fatalf("got %q, %v", answer, errText(t, err))
	}
	msgs := rt.Broker.Memory("main").Messages()
	batches, control := split(msgs)
	covers(t, batches, 2500)
	if len(batches) != 3 || len(control) != 1 {
		t.Fatalf("%d batches and %d control", len(batches), len(control))
	}

	// And once more: everything is done, so nothing new goes out, and the copy of the control is dropped.
	if answer, err := extract(rt, file, "j3", "1000", "5000000"); err != nil || answer != "job j3: 3 batches, 2500 rows" {
		t.Fatalf("again: %q, %v", answer, errText(t, err))
	}
	if n := len(rt.Broker.Memory("main").Messages()); n != len(msgs) {
		t.Errorf("a third run left %d messages where there were %d", n, len(msgs))
	}
}

func TestAJobStartedAgainWithAnotherSizeKeepsTheEdgesItPlanned(t *testing.T) {
	rt, file := stageExtractor(t)
	rt.Broker.Memory("main").SetDown(true)
	if _, err := extract(rt, file, "j4", "700", "5000000"); err == nil {
		t.Fatal("the run must stop")
	}
	rt.Broker.Memory("main").SetDown(false)
	// The size is not the one of the first try: the batch that was planned still ends where it was planned.
	if _, err := extract(rt, file, "j4", "1100", "5000000"); err != nil {
		t.Fatal(errText(t, err))
	}
	batches, _ := split(rt.Broker.Memory("main").Messages())
	covers(t, batches, 2500)
	if first, _, _ := unpack(t, batches[0]); first.Upto != 700 {
		t.Errorf("the first batch ends at %d, and it was planned to end at 700", first.Upto)
	}
}

func TestTheExtractorRefusesAnOutboxOfAnotherVersion(t *testing.T) {
	rt, file := stageExtractor(t)
	db, err := sql.Open("sqlite", filepath.Join(rt.Config.Root, "state", "outbox.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("UPDATE outbox_meta SET schema_version = 2"); err != nil {
		t.Fatal(err)
	}
	db.Close()
	_, err = extract(rt, file, "j5", "1000", "5000000")
	mustContain(t, errText(t, err), "the outbox has version 2", "works with version 1")
	if n := len(rt.Broker.Memory("main").Messages()); n != 0 {
		t.Errorf("%d messages went out", n)
	}
}
