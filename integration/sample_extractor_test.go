//go:build integration

package integration

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
)

// The Extractor of the asynchronous ELT (samples/async-elt) with a source that is not SQLite: the configuration of
// sources/source.<database>.toml in place of the SQLite one, a table made by migrations/demo-source.<database>.sql,
// and a real JetStream server.

// databaseSource puts the source of the sample for a database in place of the SQLite one, pointing it at the server.
func databaseSource(t *testing.T, dir, driver string, db server) {
	t.Helper()
	path := dir + "/metagente.toml"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	base := string(raw)
	fragment := sampleText(t, "sources/source."+driver+".toml")
	fragment = strings.Replace(fragment, `host = "db.example.com"`, fmt.Sprintf("host = %q\nport = %d\ntls = \"disable\"", db.host, db.port), 1)
	fragment = regexp.MustCompile(`(?m)^database = ".*"`).ReplaceAllString(fragment, fmt.Sprintf("database = %q", db.database()))
	fragment = regexp.MustCompile(`(?m)^user = "extractor"`).ReplaceAllString(fragment, fmt.Sprintf("user = %q", db.user()))
	start, end := strings.Index(base, "[sql.source]"), strings.Index(base, "# ---- the outbox")
	base = base[:start] + fragment + "\n" + base[end:]
	base = strings.Replace(base, "\n[credentials]\nevents =", "\n[credentials]\nsource = \"DB_PASSWORD\"\nevents =", 1) // the table that extractorProject adds, not the comment
	write(t, path, base)
}

func TestTheExtractorReadsASourceOfEveryDatabase(t *testing.T) {
	for _, driver := range append([]string{"mysql", "mariadb"}, databases...) {
		t.Run(driver, func(t *testing.T) {
			db := startWith(t, driver, seedFile(t, driver, sampleText(t, "migrations/demo-source."+strings.Replace(driver, "mariadb", "mysql", 1)+".sql")))
			s := startNATS(t)
			stream := s.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Storage: jetstream.MemoryStorage, Discard: jetstream.DiscardNew, MaxMsgs: 1000})
			dir := s.extractorProject(t)
			databaseSource(t, dir, driver, db)
			if out, err := metagente(t, dir, natsSecret, "trust", "extractor.ag", "--yes"); err != nil {
				t.Fatalf("trust: %v\n%s", err, out)
			}
			out, err := metagente(t, dir, natsSecret, "run", "extractor.ag", "extract", "job=j1", "size=1000", "bytes=5000000")
			if err != nil || !strings.Contains(out, "job j1: 3 batches, 2500 rows") {
				t.Fatalf("extractor (err = %v):\n%s", err, out)
			}
			if n := s.messages(t, stream); n != 4 {
				t.Errorf("the stream has %d messages, and 4 were expected", n)
			}
			checkFirstRows(t, stream)
		})
	}
}

// checkFirstRows reads the last batch of the stream and looks at what the source gave: the document masked at
// the source.
func checkFirstRows(t *testing.T, stream jetstream.Stream) {
	t.Helper()
	msg, err := stream.GetLastMsgForSubject(context.Background(), "etl.j1.batch")
	if err != nil {
		t.Fatal(err)
	}
	var event struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &event); err != nil {
		t.Fatal(err)
	}
	packed, err := base64.StdEncoding.DecodeString(event.Payload)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(packed))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(zr)
	text := string(body)
	if !strings.Contains(text, `"***`) || regexp.MustCompile(`"\d{11}"`).MatchString(text) {
		t.Errorf("the document is not masked at the source:\n%.300s", text)
	}
}
