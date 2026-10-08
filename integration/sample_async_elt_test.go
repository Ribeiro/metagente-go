//go:build integration

package integration

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/nats-io/nats.go/jetstream"
	_ "modernc.org/sqlite"
)

// The Extractor of the asynchronous ELT (samples/async-elt) against a real JetStream server: the files of the
// sample as they are, a demo source and an outbox made with the migrations of the sample, and a stream that
// takes etl.>, which discards what would not fit.

func sampleText(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "samples", "async-elt", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func (s natsServer) extractorProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	toml := sampleText(t, "metagente.toml")
	for _, line := range []string{`(?m)^url = .*\n`, `(?m)^stream = .*\n`, `(?m)^driver = "jetstream"\n`} {
		toml = regexp.MustCompile(line).ReplaceAllString(toml, "")
	}
	toml = strings.Replace(toml, "[broker.main]\n", fmt.Sprintf("[broker.main]\ndriver = \"jetstream\"\nurl = \"nats://%s:%d\"\nuser = %q\ntls = \"disable\"\nstream = \"ETL\"\n", s.host, s.port, natsUser), 1)
	toml += "\n[credentials]\nevents = \"BROKER_PASSWORD\"\nmain = \"BROKER_PASSWORD\"\n" // to publish, and to consume
	write(t, filepath.Join(dir, "metagente.toml"), toml)
	write(t, filepath.Join(dir, "extractor.ag"), sampleText(t, "extractor.ag"))
	for path, migration := range map[string]string{"demo/source.db": "migrations/demo-source.sql", "state/outbox.db": "migrations/outbox.sql"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.Dir(path)), 0o755); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("sqlite", filepath.Join(dir, path))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(sampleText(t, migration)); err != nil {
			t.Fatalf("%s: %v", migration, err)
		}
		db.Close()
	}
	return dir
}

func TestTheExtractorFillsAStreamAndACopyOfTheRunAddsNothing(t *testing.T) {
	s := startNATS(t)
	stream := s.stream(t, jetstream.StreamConfig{
		Name: "ETL", Subjects: []string{"etl.>"}, Storage: jetstream.MemoryStorage,
		Discard: jetstream.DiscardNew, MaxMsgs: 1000, Duplicates: 0,
	})
	dir := s.extractorProject(t)
	if out, err := metagente(t, dir, natsSecret, "trust", "extractor.ag", "--yes"); err != nil {
		t.Fatalf("trust: %v\n%s", err, out)
	}
	run := func() (string, error) {
		return metagente(t, dir, natsSecret, "run", "extractor.ag", "extract", "job=j1", "size=1000", "bytes=5000000")
	}
	out, err := run()
	if err != nil || !strings.Contains(out, "job j1: 3 batches, 2500 rows") {
		t.Fatalf("run (err = %v):\n%s", err, out)
	}
	// Three batches and one control: the batches are the messages a Worker will take.
	if n := s.messages(t, stream); n != 4 {
		t.Fatalf("the stream has %d messages, and 4 were expected", n)
	}
	// The same job again finds everything done: the control is a copy and the server drops it.
	if out, err := run(); err != nil || !strings.Contains(out, "job j1: 3 batches, 2500 rows") {
		t.Fatalf("second run (err = %v):\n%s", err, out)
	}
	if n := s.messages(t, stream); n != 4 {
		t.Errorf("a second run left %d messages, and 4 were expected", n)
	}
	info, err := stream.Info(context.Background(), jetstream.WithSubjectFilter("etl.j1.batch"))
	if err != nil || info.State.Subjects["etl.j1.batch"] != 3 {
		t.Errorf("batches = %v, %v", info.State.Subjects, err)
	}
}
