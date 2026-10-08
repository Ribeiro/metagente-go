//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
    labelled = dest.labelled
    asked = dest.asked
    reason = dest.reason
    reply "final {loaded}; rejected {rejected}; job {state}; batches done {done}; labelled {labelled}; asked {asked}; reason {reason}"
`

// worker runs the program the way the machine of the Worker has it: the password of the database and the one of
// the broker are two variables.
func worker(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "DEST_DB_PASSWORD="+dbSecret, "BROKER_PASSWORD="+natsSecret, "ANTHROPIC_API_KEY=test-key", "METAGENTE_CONFIG_DIR="+approvals(t, dir))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// fakeModel is a language model that speaks the chat format of Anthropic and labels the notes it is given.
func fakeModel(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var request struct {
			Messages []struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &request)
		prompt := request.Messages[0].Content[0].Text
		var items []struct {
			ID   int    `json:"id"`
			Note string `json:"note"`
		}
		_ = json.Unmarshal([]byte(prompt[strings.Index(prompt, "["):]), &items)
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
		answer, _ := json.Marshal(labels)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"content": []map[string]any{{"type": "text", "text": string(answer)}}, "stop_reason": "end_turn",
			"usage": map[string]int{"input_tokens": 100, "output_tokens": 50},
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func (s natsServer) postgresWorkerProject(t *testing.T, extractor string, pg server, model string) {
	t.Helper()
	toml := sampleText(t, "metagente.postgres.toml")
	toml = strings.Replace(toml, `host = "db.example.com"`, fmt.Sprintf("host = %q\nport = %d\ntls = \"disable\"", pg.host, pg.port), 1)
	toml = strings.Replace(toml, `database = "warehouse"`, fmt.Sprintf("database = %q", dbName), 1)
	toml = regexp.MustCompile(`(?m)^user = "worker"$`).ReplaceAllString(toml, fmt.Sprintf("user = %q", dbUser))
	toml = strings.Replace(toml, `url = "nats://127.0.0.1:4222"`, fmt.Sprintf("url = \"nats://%s:%d\"\nuser = %q\ntls = \"disable\"", s.host, s.port, natsUser), 1)
	toml = regexp.MustCompile(`(?m)^# events = .*\n`).ReplaceAllString(toml, "main = \"BROKER_PASSWORD\"\n")
	toml = strings.Replace(toml, `api_key_env = "ANTHROPIC_API_KEY"`, "api_key_env = \"ANTHROPIC_API_KEY\"\nbase_url = \""+model+"\"", 1)
	toml = strings.Replace(toml, "[sql.dest.transactions]", `loaded = { sql = "SELECT count(*) FROM orders_final", result = "value" }
rejected = { sql = "SELECT count(*) FROM etl_rejects", result = "value" }
state = { sql = "SELECT state FROM etl_jobs WHERE job_id = 'j1'", result = "value" }
reason = { sql = "SELECT coalesce(pause_reason, 'none') FROM etl_jobs WHERE job_id = 'j1'", result = "value" }
done_batches = { sql = "SELECT count(*) FROM etl_batches WHERE state = 'done'", result = "value" }
labelled = { sql = "SELECT count(*) FROM orders_final WHERE note_category IS NOT NULL", result = "value" }
asked = { sql = "SELECT CAST(coalesce(sum(model_calls), 0) AS BIGINT) FROM etl_batches", result = "value" }

[sql.dest.transactions]`, 1)
	write(t, filepath.Join(extractor, "worker.postgres.toml"), toml)
	write(t, filepath.Join(extractor, "worker.ag"), sampleText(t, "worker.ag"))
	write(t, filepath.Join(extractor, "enricher.ag"), sampleText(t, "enricher.ag"))
	write(t, filepath.Join(extractor, "check.ag"), checkAgent)
}

// pipelineInPostgreSQL starts the servers and writes the project of both agents: the Extractor with a SQLite source,
// and the Worker with the configuration for PostgreSQL.
func pipelineInPostgreSQL(t *testing.T) string {
	t.Helper()
	nats := startNATS(t)
	nats.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Storage: jetstream.MemoryStorage, Discard: jetstream.DiscardNew, MaxMsgs: 1000})
	pg := start(t, "postgres")
	dir := nats.extractorProject(t)
	nats.postgresWorkerProject(t, dir, pg, fakeModel(t).URL)
	if out, err := worker(t, dir, "trust", "worker.ag", "--config", "worker.postgres.toml", "--yes"); err != nil {
		t.Fatalf("trust worker: %v\n%s", err, out)
	}
	if out, err := worker(t, dir, "trust", "check.ag", "--config", "worker.postgres.toml", "--yes"); err != nil {
		t.Fatalf("trust check: %v\n%s", err, out)
	}
	if out, err := metagente(t, dir, natsSecret, "trust", "extractor.ag", "--yes"); err != nil {
		t.Fatalf("trust: %v\n%s", err, out)
	}
	return dir
}

func send(t *testing.T, dir string, message string, params ...string) string {
	t.Helper()
	args := append([]string{"run", "worker.ag", message}, params...)
	out, err := worker(t, dir, append(args, "--config", "worker.postgres.toml")...)
	if err != nil {
		t.Fatalf("%s: %v\n%s", message, err, out)
	}
	return out
}

func extract(t *testing.T, dir string) {
	t.Helper()
	if out, err := metagente(t, dir, natsSecret, "run", "extractor.ag", "extract", "job=j1", "size=1000", "bytes=5000000"); err != nil || !strings.Contains(out, "job j1: 3 batches, 2500 rows") {
		t.Fatalf("extractor (err = %v):\n%s", err, out)
	}
}

// consumeOn runs the Worker on one kind of event until the stream has nothing more for it.
func consumeOn(t *testing.T, dir, subject, message string, extra ...string) string {
	t.Helper()
	approve := []string{"trust", "worker.ag", "--config", "worker.postgres.toml", "--from", "main", "--subject", subject, "--dead", "etl.dead", "--yes"}
	if out, err := worker(t, dir, approve...); err != nil {
		t.Fatalf("trust: %v\n%s", err, out)
	}
	args := []string{"consume", "worker.ag", "--config", "worker.postgres.toml", "--from", "main",
		"--subject", subject, "--dead", "etl.dead", "--message", message, "--idle-exit", "3", "--durable", "it-" + message}
	out, err := worker(t, dir, append(args, extra...)...)
	if err != nil {
		t.Fatalf("consume %s: %v\n%s", message, err, out)
	}
	return out
}

func check(t *testing.T, dir string) string {
	t.Helper()
	out, err := worker(t, dir, "run", "check.ag", "count", "start=x", "--config", "worker.postgres.toml")
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	return out
}

func TestTheWorkerLandsTransformsAndClosesAJobInPostgreSQL(t *testing.T) {
	dir := pipelineInPostgreSQL(t)
	// The job has a budget for the model step.
	if out := send(t, dir, "budget", "job=j1", "max_calls=1000", "max_tokens=1000000"); !strings.Contains(out, "job j1 may use 1000 requests") {
		t.Fatalf("budget:\n%s", out)
	}
	extract(t, dir)
	// The batches first, then the control event.
	if out := consumeOn(t, dir, "etl.*.batch", "batch"); !strings.Contains(out, "Taken 3: done 3, asked for again 0, dead letters 0") {
		t.Errorf("batches:\n%s", out)
	}
	if out := consumeOn(t, dir, "etl.*.control", "control"); !strings.Contains(out, "Taken 1: done 1") {
		t.Errorf("control:\n%s", out)
	}
	if out := check(t, dir); !strings.Contains(out, "final 2500; rejected 0; job done; batches done 3; labelled 1500; asked 75; reason none") {
		t.Errorf("check:\n%s", out)
	}
}

// A budget that is spent pauses the job, with the reason; the events wait; raising the budget and resuming the job
// lets them go on, and nothing the model already labelled is asked again.
func TestAJobWhoseBudgetIsSpentIsPausedAndGoesOnWhenItIsResumedInPostgreSQL(t *testing.T) {
	dir := pipelineInPostgreSQL(t)
	send(t, dir, "budget", "job=j1", "max_calls=5", "max_tokens=1000000")
	extract(t, dir)
	out := consumeOn(t, dir, "etl.*.batch", "batch", "--max-deliver", "3", "--backoff", "200ms,200ms", "--breaker-after", "10")
	if !strings.Contains(out, "dead letters 0") || !strings.Contains(out, "asked for again 3") {
		t.Errorf("batches:\n%s", out)
	}
	if got := check(t, dir); !strings.Contains(got, "job paused") || !strings.Contains(got, "asked 5") || !strings.Contains(got, "reason MODEL_BUDGET") {
		t.Errorf("paused:\n%s", got)
	}
	// The budget is raised and the job resumed: the events that waited come again.
	send(t, dir, "budget", "job=j1", "max_calls=1000", "max_tokens=1000000")
	if got := send(t, dir, "resume", "job=j1"); !strings.Contains(got, "1 paused job is running again") {
		t.Errorf("resume:\n%s", got)
	}
}
