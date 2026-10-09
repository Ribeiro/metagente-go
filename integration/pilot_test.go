//go:build integration && pilot

package integration

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

// The pilot of the asynchronous ELT (section 14 of docs/design-async-elt.md): the Extractor reads a PostgreSQL source of
// synthetic orders, one to four Workers land and transform the batches in a PostgreSQL destination, and the pilot
// measures each step and injects the failures of the design. It is not part of the tests of every change: it has its own
// build tag (`pilot`) and its own workflow (.github/workflows/pilot.yml). What it measures goes in a report.
//
// It is set by the environment:
//
//	METAGENTE_PILOT_ROWS       rows of the source (default 100000)
//	METAGENTE_PILOT_WORKERS    the numbers of Workers of the clean runs (default 1,2,4)
//	METAGENTE_PILOT_SCENARIOS  baseline, kill, pause-destination, stop-destination, pause-broker, bad-rows (default all)
//	METAGENTE_PILOT_BATCH_ROWS the most rows of a page (default 1000)
//	METAGENTE_PILOT_BATCH_BYTES the size a batch aims at (default 262144)
//	METAGENTE_PILOT_PAUSE      seconds that the destination and the broker are frozen or refuse (default 20)
//	METAGENTE_PILOT_ACK_WAIT   seconds the broker waits for a Worker to confirm a batch (default 20)
//	METAGENTE_PILOT_REPORT     where the report is written (default pilot-report.md)

func pilotNumber(name string, fallback int) int {
	if n, err := strconv.Atoi(os.Getenv(name)); err == nil && n > 0 {
		return n
	}
	return fallback
}

func pilotList(name, fallback string) []string {
	value := os.Getenv(name)
	if value == "" {
		value = fallback
	}
	var out []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// psql asks the PostgreSQL server of a container something, and gives the rows as text, one line for each, with | between
// the columns.
func (s server) psql(sql string) (string, error) { return s.psqlOn(dbName, sql) }

// psqlOn is psql in another database of the server.
func (s server) psqlOn(database, sql string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	code, reader, err := s.ctr.Exec(ctx, []string{"psql", "-U", dbUser, "-d", database, "-v", "ON_ERROR_STOP=1", "-At", "-F", "|", "-c", sql}, tcexec.Multiplexed())
	if err != nil {
		return "", err
	}
	raw, _ := io.ReadAll(reader)
	if code != 0 {
		return "", fmt.Errorf("psql exited with %d: %s", code, strings.TrimSpace(string(raw)))
	}
	return strings.TrimSpace(string(raw)), nil
}

func (s server) mustPsql(t *testing.T, sql string) string {
	t.Helper()
	out, err := s.psql(sql)
	if err != nil {
		t.Fatalf("%v\n%s", err, sql)
	}
	return out
}

// freeze stops a container where it is, without closing its connections, and thaws it after a while.
func freeze(t *testing.T, ctr testcontainers.Container, d time.Duration) {
	t.Helper()
	id := ctr.GetContainerID()
	if out, err := exec.Command("docker", "pause", id).CombinedOutput(); err != nil {
		t.Errorf("freezing the container: %v\n%s", err, out)
		return
	}
	time.Sleep(d)
	// A container that stays frozen would stop the whole run, so the thaw is tried a few times.
	for attempt := 1; ; attempt++ {
		out, err := exec.Command("docker", "unpause", id).CombinedOutput()
		if err == nil {
			return
		}
		if attempt == 5 {
			t.Errorf("thawing the container: %v\n%s", err, out)
			return
		}
		time.Sleep(time.Second)
	}
}

// refuse makes the database turn every connection away, and ends the ones it has: what a server that is down does to those
// who talk to it, with errors and not with silence. The control goes through another database, which still takes them.
func (s server) refuse() error {
	if _, err := s.psqlOn("postgres", "ALTER DATABASE "+dbName+" ALLOW_CONNECTIONS false"); err != nil {
		return err
	}
	_, err := s.psqlOn("postgres", "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '"+dbName+"'")
	return err
}

// allow takes the database back.
func (s server) allow() error {
	_, err := s.psqlOn("postgres", "ALTER DATABASE "+dbName+" ALLOW_CONNECTIONS true")
	return err
}

// outage makes the destination turn away its callers for a while. A database that stays shut would stop the whole run, so
// taking it back is tried a few times.
func outage(t *testing.T, dest server, d time.Duration) {
	t.Helper()
	if err := dest.refuse(); err != nil {
		t.Errorf("shutting the database: %v", err)
	}
	time.Sleep(d)
	for attempt := 1; ; attempt++ {
		err := dest.allow()
		if err == nil {
			return
		}
		if attempt == 5 {
			t.Errorf("opening the database again: %v", err)
			return
		}
		time.Sleep(time.Second)
	}
}

// workerProc is a copy of `metagente consume` that runs while the test does other things.
type workerProc struct {
	cmd *exec.Cmd
	out bytes.Buffer
}

func startWorkerProc(t *testing.T, dir string, args ...string) *workerProc {
	t.Helper()
	w := &workerProc{cmd: exec.Command(binary, args...)}
	w.cmd.Dir = dir
	w.cmd.Env = append(os.Environ(), "DEST_DB_PASSWORD="+dbSecret, "BROKER_PASSWORD="+natsSecret, "ANTHROPIC_API_KEY=test-key", "METAGENTE_CONFIG_DIR="+approvals(t, dir))
	w.cmd.Stdout, w.cmd.Stderr = &w.out, &w.out
	w.cmd.WaitDelay = 10 * time.Second // a Worker that is killed must not keep the test waiting for its pipes
	if err := w.cmd.Start(); err != nil {
		t.Fatalf("starting a Worker: %v", err)
	}
	return w
}

// fleet is the Workers of a run: some are killed and others started while the run goes on.
type fleet struct {
	mu    sync.Mutex
	t     *testing.T
	dir   string
	args  []string
	procs []*workerProc
	all   []*workerProc // also the ones that were killed
}

func (f *fleet) add() {
	w := startWorkerProc(f.t, f.dir, f.args...)
	f.mu.Lock()
	f.procs = append(f.procs, w)
	f.all = append(f.all, w)
	f.mu.Unlock()
}

// killOne ends a Worker in the middle of what it does, with no time to say goodbye.
func (f *fleet) killOne() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.procs) > 0 {
		_ = f.procs[0].cmd.Process.Kill()
	}
}

// killAll ends the Workers that are running.
func (f *fleet) killAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, w := range f.procs {
		_ = w.cmd.Process.Kill()
	}
}

// wait waits until every Worker has left. When the run takes longer than it should, the Workers are killed and the answer
// is false.
func (f *fleet) wait(ctx context.Context) bool {
	f.mu.Lock()
	procs := append([]*workerProc(nil), f.procs...)
	f.mu.Unlock()
	done := make(chan struct{})
	go func() {
		for _, w := range procs {
			_ = w.cmd.Wait()
		}
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-ctx.Done():
		for _, w := range procs {
			_ = w.cmd.Process.Kill()
		}
		<-done
		return false
	}
}

// extractorRunBefore is extractorRun that gives up when the context ends.
func extractorRunBefore(ctx context.Context, t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = dir
	cmd.WaitDelay = 10 * time.Second
	cmd.Env = append(os.Environ(), "DB_PASSWORD="+dbSecret, "BROKER_PASSWORD="+natsSecret, "METAGENTE_CONFIG_DIR="+approvals(t, dir))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var (
	takenLine   = regexp.MustCompile(`Taken (\d+): done (\d+), asked for again (\d+), dead letters (\d+)`)
	breakerLine = regexp.MustCompile(`the destination failed \d+ times in a row`)
)

// tally adds what the Workers said they did: events asked for again, dead letters, and how many times the breaker opened.
// A Worker that was killed said nothing, so its part is not here.
func (f *fleet) tally() (again, dead, breaker int) {
	for _, w := range f.all {
		text := w.out.String()
		if m := takenLine.FindStringSubmatch(text); m != nil {
			a, _ := strconv.Atoi(m[3])
			d, _ := strconv.Atoi(m[4])
			again, dead = again+a, dead+d
		}
		breaker += len(breakerLine.FindAllString(text, -1))
	}
	return again, dead, breaker
}

// pilotRun is one run of the pilot.
type pilotRun struct {
	scenario     string
	workers      int
	badPercent   int  // of the rows, invalid in the source
	kill         bool // a Worker is killed in the middle of the run
	freezeDest   bool // the destination is frozen: its callers wait
	stopDest     bool // the destination turns its callers away, with errors
	freezeBroker bool
}

// harmful says whether the run does harm to a part of the pipeline while it goes on.
func (r pilotRun) harmful() bool { return r.kill || r.freezeDest || r.stopDest || r.freezeBroker }

func (r pilotRun) name() string {
	switch {
	case r.badPercent > 0:
		return fmt.Sprintf("bad-rows-%d", r.badPercent)
	case r.scenario == "baseline":
		return fmt.Sprintf("baseline-%d", r.workers)
	}
	return r.scenario
}

// pilotResult is what a run measured.
type pilotResult struct {
	run                          pilotRun
	extractSeconds               float64
	extractRestarts              int
	batches, done, failed, stuck int
	read, loaded, rejected       int
	p50, p95, p99, longest       float64
	spanSeconds                  float64
	attempts                     int
	again, dead, breaker         int
	final, duplicates            int
	job                          string
	harmed                       bool // the harm of the run (the kill or the freeze) was done while it went on
	problems                     []string
}

func (r *pilotResult) problem(format string, args ...any) {
	r.problems = append(r.problems, fmt.Sprintf(format, args...))
}

type pilot struct {
	rows, batchRows, batchBytes int
	pause, ackWait              time.Duration
	source                      server
	lastBad                     int
	results                     []*pilotResult
}

func TestThePilot(t *testing.T) {
	p := &pilot{
		rows:       pilotNumber("METAGENTE_PILOT_ROWS", 100000),
		batchRows:  pilotNumber("METAGENTE_PILOT_BATCH_ROWS", 1000),
		batchBytes: pilotNumber("METAGENTE_PILOT_BATCH_BYTES", 262144),
		pause:      time.Duration(pilotNumber("METAGENTE_PILOT_PAUSE", 20)) * time.Second,
		ackWait:    time.Duration(pilotNumber("METAGENTE_PILOT_ACK_WAIT", 20)) * time.Second,
		lastBad:    -1,
	}
	// The source: the demo orders of the sample, as many as the pilot wants, made once for all the runs.
	script := strings.Replace(sampleText(t, "migrations/demo-source.postgres.sql"), "generate_series(1, 2500)", fmt.Sprintf("generate_series(1, %d)", p.rows), 1)
	p.source = startWith(t, "postgres", seedFile(t, "postgres", script))

	var runs []pilotRun
	for _, scenario := range pilotList("METAGENTE_PILOT_SCENARIOS", "baseline,kill,pause-destination,stop-destination,pause-broker,bad-rows") {
		switch scenario {
		case "baseline":
			for _, w := range pilotList("METAGENTE_PILOT_WORKERS", "1,2,4") {
				n, _ := strconv.Atoi(w)
				runs = append(runs, pilotRun{scenario: "baseline", workers: n})
			}
		case "kill":
			runs = append(runs, pilotRun{scenario: "kill", workers: 2, kill: true})
		case "pause-destination":
			runs = append(runs, pilotRun{scenario: "pause-destination", workers: 2, freezeDest: true})
		case "stop-destination":
			runs = append(runs, pilotRun{scenario: "stop-destination", workers: 2, stopDest: true})
		case "pause-broker":
			runs = append(runs, pilotRun{scenario: "pause-broker", workers: 2, freezeBroker: true})
		case "bad-rows":
			for _, percent := range []int{1, 5, 25} {
				runs = append(runs, pilotRun{scenario: "bad-rows", workers: 2, badPercent: percent})
			}
		default:
			t.Fatalf("I do not know the scenario %q", scenario)
		}
	}
	defer func() { writePilotReport(t, p) }()
	for _, r := range runs {
		t.Run(r.name(), func(t *testing.T) {
			res := p.execute(t, r)
			p.results = append(p.results, res)
			for _, problem := range res.problems {
				t.Error(problem)
			}
		})
	}
}

// execute makes the servers of a run, runs the Extractor and the Workers, and reads what the destination kept.
func (p *pilot) execute(t *testing.T, r pilotRun) *pilotResult {
	res := &pilotResult{run: r}
	if r.badPercent != p.lastBad {
		p.source.mustPsql(t, fmt.Sprintf("UPDATE orders SET customer = CASE WHEN id %% 100 < %d THEN '' ELSE 'Customer ' || id END", r.badPercent))
		p.lastBad = r.badPercent
	}
	batches := (p.rows + p.batchRows - 1) / p.batchRows
	nats := startNATS(t)
	stream := nats.stream(t, jetstream.StreamConfig{Name: "ETL", Subjects: []string{"etl.>"}, Storage: jetstream.MemoryStorage, Discard: jetstream.DiscardNew, MaxMsgs: int64(batches*4 + 100)})
	dest := start(t, "postgres")
	dir := nats.extractorProject(t)
	databaseSource(t, dir, "postgres", p.source)
	raw, err := os.ReadFile(filepath.Join(dir, "metagente.toml"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "metagente.toml"), strings.Replace(string(raw), "timeout_seconds = 60", "timeout_seconds = 3600", 1)) // a big table has many pages
	nats.destinationProject(t, dir, dest, fakeModel(t).URL)
	for _, file := range []string{"worker.ag", "check.ag"} {
		if out, err := worker(t, dir, "trust", file, "--config", "worker.dest.toml", "--yes"); err != nil {
			t.Fatalf("trust %s: %v\n%s", file, err, out)
		}
	}
	if out, err := extractorRun(t, dir, "trust", "extractor.ag", "--yes"); err != nil {
		t.Fatalf("trust extractor: %v\n%s", err, out)
	}
	if out, err := worker(t, dir, "trust", "worker.ag", "--config", "worker.dest.toml", "--from", "main", "--subject", "etl.*.batch", "--dead", "etl.dead", "--yes"); err != nil {
		t.Fatalf("trust consume: %v\n%s", err, out)
	}

	// The Workers wait for the batches before the Extractor starts, the way they would in production. They leave when
	// nothing has come for a while.
	// A batch that a killed Worker held comes again after the time to confirm, and a breaker that opened waits 30 seconds
	// before it tries again, so the Workers wait longer than both.
	idle := int((p.ackWait + p.pause).Seconds()) + 60
	crew := &fleet{t: t, dir: dir, args: []string{"consume", "worker.ag", "--config", "worker.dest.toml", "--from", "main", "--subject", "etl.*.batch",
		"--dead", "etl.dead", "--message", "batch", "--durable", "pilot-batch", "--in-flight", "2", "--ack-wait", strconv.Itoa(int(p.ackWait.Seconds())), "--idle-exit", strconv.Itoa(idle)}}
	for i := 0; i < r.workers; i++ {
		crew.add()
	}

	// No run may take for ever: past this time the Extractor and the Workers are stopped and the run fails, with the rest
	// of the report still written.
	limit := 5*time.Minute + time.Duration(p.rows/100)*time.Second + 3*p.pause + 2*p.ackWait
	ctx, cancel := context.WithTimeout(context.Background(), limit)
	defer cancel()

	var helpers sync.WaitGroup
	if r.kill || r.freezeDest || r.stopDest || r.freezeBroker {
		helpers.Add(1)
		go func() {
			defer helpers.Done()
			p.disturb(ctx, t, res, &scene{dest: dest, nats: nats, stream: stream, crew: crew, batches: batches})
		}()
	}
	if r.badPercent > 20 {
		helpers.Add(1)
		go func() {
			defer helpers.Done()
			p.stopWhenPaused(ctx, dest, crew)
		}()
	}

	// The Extractor starts again where it stopped, the way an operator would, when the broker was away.
	started := time.Now()
	var out string
	for attempt := 1; attempt <= 6; attempt++ {
		out, err = extractorRunBefore(ctx, t, dir, "run", "extractor.ag", "extract", "job=j1", fmt.Sprintf("size=%d", p.batchRows), fmt.Sprintf("bytes=%d", p.batchBytes))
		if err == nil {
			break
		}
		res.extractRestarts++
		time.Sleep(2 * time.Second)
	}
	res.extractSeconds = time.Since(started).Seconds()
	if err != nil {
		cancel()
		helpers.Wait()
		res.problem("the Extractor did not finish (%d restarts): %v\n%s", res.extractRestarts, err, out)
		return res
	}
	if !strings.Contains(out, fmt.Sprintf("%d rows", p.rows)) {
		res.problem("the Extractor says %q, and the source has %d rows", strings.TrimSpace(out), p.rows)
	}
	// The harm of the run goes on while the Workers work, which is after the Extractor is done, and it is over before the
	// Workers are waited for: a Worker that the harm starts must be one of those that are waited for.
	helpers.Wait()
	if !crew.wait(ctx) {
		res.problem("the Workers were still working after %s: they were stopped", limit)
	}
	res.again, res.dead, res.breaker = crew.tally()
	p.close(t, dir)
	p.measure(t, dest, res)
	return res
}

// doneBatches says how many batches the destination has finished.
func doneBatches(dest server) (int, error) {
	out, err := dest.psql("SELECT count(*) FROM etl_batches WHERE state = 'done'")
	n, _ := strconv.Atoi(out)
	return n, err
}

// scene is what the harm of a run acts on.
type scene struct {
	dest    server
	nats    natsServer
	stream  jetstream.Stream
	crew    *fleet
	batches int
}

// published says how many messages the stream holds, which is how far the Extractor has gone.
func published(ctx context.Context, stream jetstream.Stream) (int, error) {
	info, err := stream.Info(ctx)
	if err != nil {
		return 0, err
	}
	return int(info.State.Msgs), nil
}

// waitForHarm waits until the run has gone far enough for its harm, and says false when the run ended before that. The
// harm of the broker has to come while the Extractor is still publishing, and the Extractor is much faster than the Workers,
// so for that harm what is measured is what the Extractor published and not what the Workers did.
func (p *pilot) waitForHarm(ctx context.Context, t *testing.T, r pilotRun, sc *scene) bool {
	enough, poll := sc.batches/4, 200*time.Millisecond
	switch {
	case r.kill:
		enough = sc.batches / 3
	case r.freezeBroker:
		enough, poll = sc.batches/10, 50*time.Millisecond
	}
	enough = max(enough, 1)
	for {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(poll):
		}
		var n, end int
		var err error
		if r.freezeBroker {
			n, err = published(ctx, sc.stream)
			end = sc.batches + 1 // and the control event
		} else {
			n, err = doneBatches(sc.dest)
			end = sc.batches
		}
		if err != nil {
			continue
		}
		if n >= end {
			t.Logf("the run was over (%d of %d) before the harm could be done", n, end)
			return false
		}
		if n >= enough {
			return true
		}
	}
}

// disturb waits until the run has gone far enough and then does the harm of the run.
func (p *pilot) disturb(ctx context.Context, t *testing.T, res *pilotResult, sc *scene) {
	r := res.run
	if !p.waitForHarm(ctx, t, r, sc) {
		return
	}
	switch {
	case r.kill:
		sc.crew.killOne()
		time.Sleep(3 * time.Second)
		sc.crew.add()
	case r.freezeDest:
		freeze(t, sc.dest.ctr, p.pause)
	case r.stopDest:
		outage(t, sc.dest, p.pause)
	case r.freezeBroker:
		freeze(t, sc.nats.ctr, p.pause)
	}
	res.harmed = true
}

// stopWhenPaused ends the Workers a few seconds after the job is paused: the batches that wait for the job to be resumed
// are asked for again in an hour, so the Workers would never be idle and would never leave.
func (p *pilot) stopWhenPaused(ctx context.Context, dest server, crew *fleet) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		if out, err := dest.psql("SELECT count(*) FROM etl_jobs WHERE state = 'paused'"); err == nil && out == "1" {
			select {
			case <-ctx.Done():
			case <-time.After(3 * time.Second):
			}
			crew.killAll()
			return
		}
	}
}

// close sends the control event to the Worker that closes the job, and waits for it to be taken.
func (p *pilot) close(t *testing.T, dir string) {
	t.Helper()
	if out, err := worker(t, dir, "trust", "worker.ag", "--config", "worker.dest.toml", "--from", "main", "--subject", "etl.*.control", "--dead", "etl.dead", "--yes"); err != nil {
		t.Errorf("trust control: %v\n%s", err, out)
		return
	}
	out, err := worker(t, dir, "consume", "worker.ag", "--config", "worker.dest.toml", "--from", "main", "--subject", "etl.*.control", "--dead", "etl.dead",
		"--message", "control", "--idle-exit", "5", "--durable", "pilot-control")
	if err != nil {
		t.Errorf("the control event: %v\n%s", err, out)
	}
}

// measure reads from the destination how long each batch took, and checks that the books close.
func (p *pilot) measure(t *testing.T, dest server, res *pilotResult) {
	t.Helper()
	took := "extract(epoch FROM done_at - landed_at)"
	row := dest.mustPsql(t, `SELECT count(*), count(*) FILTER (WHERE state = 'done'), count(*) FILTER (WHERE state = 'failed'), count(*) FILTER (WHERE state = 'landed'),
 coalesce(sum(rows_read), 0), coalesce(sum(rows_loaded), 0), coalesce(sum(rows_rejected), 0),
 coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY `+took+`), 0), coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY `+took+`), 0),
 coalesce(percentile_cont(0.99) WITHIN GROUP (ORDER BY `+took+`), 0), coalesce(max(`+took+`), 0),
 coalesce(extract(epoch FROM max(done_at) - min(landed_at)), 0), coalesce(sum(attempts), 0) FROM etl_batches`)
	cells := strings.Split(row, "|")
	if len(cells) != 13 {
		t.Fatalf("the destination answered %q", row)
	}
	whole := func(i int) int { n, _ := strconv.ParseFloat(cells[i], 64); return int(n) }
	decimal := func(i int) float64 { n, _ := strconv.ParseFloat(cells[i], 64); return n }
	res.batches, res.done, res.failed, res.stuck = whole(0), whole(1), whole(2), whole(3)
	res.read, res.loaded, res.rejected = whole(4), whole(5), whole(6)
	res.p50, res.p95, res.p99, res.longest, res.spanSeconds, res.attempts = decimal(7), decimal(8), decimal(9), decimal(10), decimal(11), whole(12)
	res.final, _ = strconv.Atoi(dest.mustPsql(t, "SELECT count(*) FROM orders_final"))
	res.duplicates, _ = strconv.Atoi(dest.mustPsql(t, "SELECT count(*) FROM (SELECT source_key FROM stg_orders GROUP BY source_key HAVING count(*) > 1) d"))
	res.job = dest.mustPsql(t, "SELECT state || '/' || coalesce(pause_reason, 'none') FROM etl_jobs WHERE job_id = 'j1'")
	unbalanced, _ := strconv.Atoi(dest.mustPsql(t, "SELECT count(*) FROM etl_batches WHERE state = 'done' AND rows_read <> coalesce(rows_loaded, 0) + coalesce(rows_rejected, 0)"))

	res.judge(p.rows, unbalanced)
}

// judge says what the pilot approves: the books close, there is no duplicate after a kill, and each failure ends as the
// design says.
func (r *pilotResult) judge(rows, unbalanced int) {
	if r.run.harmful() && !r.harmed {
		r.problem("the run ended before the harm was done, so it proves nothing: use more rows")
	}
	if r.duplicates != 0 {
		r.problem("%d rows came to staging twice", r.duplicates)
	}
	if unbalanced != 0 {
		r.problem("%d batches are done and read is not loaded plus rejected", unbalanced)
	}
	if r.final != r.loaded {
		r.problem("the final table has %d rows and the batches say %d were loaded", r.final, r.loaded)
	}
	if r.run.harmful() && r.dead != 0 {
		r.problem("%d dead letters: a failure that passes must not end a batch for good", r.dead)
	}
	if r.run.badPercent > 20 {
		// More invalid rows than the brake allows: the job has to stop, not to go on.
		if !strings.HasPrefix(r.job, "paused/") {
			r.problem("%d%% of the rows are invalid and the job is %q: the brake of quality should have paused it", r.run.badPercent, r.job)
		}
		return
	}
	r.judgeEnd(rows)
}

// judgeEnd is for the runs that have to end well: the job is done and every row is loaded or rejected.
func (r *pilotResult) judgeEnd(rows int) {
	if r.job != "done/none" {
		r.problem("the job is %q, and it should be done", r.job)
	}
	if r.read != rows || r.loaded+r.rejected != rows {
		r.problem("read %d, loaded %d, rejected %d, and the source has %d rows", r.read, r.loaded, r.rejected, rows)
	}
	want := r.run.badPercent * rows / 100
	if r.run.badPercent > 0 && (r.rejected < want*9/10 || r.rejected > want*11/10) {
		r.problem("%d rows were rejected, and about %d were invalid", r.rejected, want)
	}
}

// harmText says whether the harm of a run was done: "-" for the runs that have none.
func harmText(r *pilotResult) string {
	switch {
	case !r.run.harmful():
		return "-"
	case r.harmed:
		return "yes"
	}
	return "no"
}

func rate(rows int, seconds float64) string {
	if seconds <= 0 {
		return "-"
	}
	return fmt.Sprintf("%.0f", float64(rows)/seconds)
}

// writePilotReport writes what the pilot measured, and the starting values of the brakes beside what it suggests.
func writePilotReport(t *testing.T, p *pilot) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Pilot of the asynchronous ELT\n\n%d rows, batches of at most %d rows aiming at %d bytes, PostgreSQL source and destination, a freeze or an outage of %s, time to confirm a batch %s.\n\n", p.rows, p.batchRows, p.batchBytes, p.pause, p.ackWait)
	b.WriteString("| Run | Workers | Extractor (s) | rows/s read | Batches | Done | p50 (s) | p95 (s) | p99 (s) | Longest (s) | Workers span (s) | rows/s landed | Tries | Asked again | Dead | Rejected | Harm | Breaker | Job | Result |\n")
	b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
	for _, r := range p.results {
		verdict := "approved"
		if len(r.problems) > 0 {
			verdict = "**failed**"
		}
		fmt.Fprintf(&b, "| %s | %d | %.1f | %s | %d | %d | %.2f | %.2f | %.2f | %.2f | %.1f | %s | %d | %d | %d | %d | %s | %d | %s | %s |\n",
			r.run.name(), r.run.workers, r.extractSeconds, rate(p.rows, r.extractSeconds), r.batches, r.done, r.p50, r.p95, r.p99, r.longest,
			r.spanSeconds, rate(r.loaded+r.rejected, r.spanSeconds), r.attempts, r.again, r.dead, r.rejected, harmText(r), r.breaker, r.job, verdict)
	}
	b.WriteString("\n")
	for _, r := range p.results {
		for _, problem := range r.problems {
			fmt.Fprintf(&b, "- `%s`: %s\n", r.run.name(), problem)
		}
	}
	b.WriteString(brakesTable(p))
	path := os.Getenv("METAGENTE_PILOT_REPORT")
	if path == "" {
		path = "pilot-report.md"
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Errorf("writing the report: %v", err)
	}
	t.Log("\n" + b.String())
}

// killCost says what a dead Worker cost in time: the batches it held came back only after the time to confirm.
func killCost(p *pilot) string {
	var kill, clean *pilotResult
	for _, r := range p.results {
		switch {
		case r.run.scenario == "kill" && len(r.problems) == 0:
			kill = r
		case r.run.scenario == "baseline" && r.run.workers == 2 && len(r.problems) == 0:
			clean = r
		}
	}
	if kill == nil || clean == nil {
		return "the cost of a dead Worker is not measured"
	}
	return fmt.Sprintf("a dead Worker added %.1f s to the run, with %s to confirm", kill.spanSeconds-clean.spanSeconds, p.ackWait)
}

// brakesTable puts the starting value of each brake (section 14 of the design) beside what the clean runs suggest.
func brakesTable(p *pilot) string {
	var longest99, best float64
	var bestWorkers int
	var lines []string
	for _, r := range p.results {
		if r.run.scenario != "baseline" || len(r.problems) > 0 {
			continue
		}
		if r.p99 > longest99 {
			longest99 = r.p99
		}
		if speed := float64(r.loaded+r.rejected) / r.spanSeconds; r.spanSeconds > 0 && speed > best {
			best, bestWorkers = speed, r.run.workers
		}
		lines = append(lines, fmt.Sprintf("%d Workers: p95 %.2f s", r.run.workers, r.p95))
	}
	var b strings.Builder
	b.WriteString("\n## The brakes\n\n| Brake | Starting value | What the pilot says |\n|---|---|---|\n")
	if longest99 > 0 {
		fmt.Fprintf(&b, "| Time to confirm a batch | 3 times the p99 of the time of a batch | p99 of the clean runs is %.2f s, so %.1f s; %s |\n", longest99, 3*longest99, killCost(p))
		fmt.Fprintf(&b, "| Batches in flight | 2 times the number of Workers | raise it until the p95 gets worse: %s |\n", strings.Join(lines, "; "))
		fmt.Fprintf(&b, "| Workers | as many as the destination allows | the best throughput of the clean runs is %.0f rows/s, with %d Workers |\n", best, bestWorkers)
	}
	b.WriteString("| Rejected rows in a batch | 20% | the runs with 1% and 5% of invalid rows close the job; the one with 25% must pause it |\n")
	b.WriteString("| Pause the job | 3 batches in a row above the limit | see the run with 25% |\n")
	b.WriteString("| Destination down | opens after 3 failures in a row; tries again every 30 s | see `stop-destination`: the Breaker column says how many times it opened; `pause-destination` is a destination that does not answer, which makes the callers wait and does not open it |\n")
	b.WriteString("| Maximum deliveries | 5, waiting 10 s, 1, 5 and 15 min | see `kill`, the pauses and the outage: dead letters must be 0 |\n")
	b.WriteString("| Size of a batch | try 64, 128, 256 and 512 KiB | run the pilot again with other values of `METAGENTE_PILOT_BATCH_BYTES` |\n")
	return b.String()
}
