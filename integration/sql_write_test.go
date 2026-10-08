//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeProject is a folder whose connection changes rows: it lands the orders in another table, the way
// the Worker of the asynchronous ELT lands a batch.
func (s server) writeProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	config := fmt.Sprintf(`[credentials]
dest = "DB_PASSWORD"

[sql.db]
driver = %q
host = %q
port = %d
database = %q
user = %q
tls = "disable"
mode = "write"

[sql.db.statements]
page = %q
land = { sql = "INSERT INTO landing (job, id, customer) VALUES (:job, :id, :customer)", each = "rows", columns = ["id", "customer"] }
mark = "INSERT INTO marks (job, state) VALUES (:job, :state)"
finish = "UPDATE marks SET state = 'done' WHERE job = :job"
purge = "DELETE FROM landing WHERE job = :job"
landed = { sql = "SELECT count(*) FROM landing WHERE job = :job", result = "value" }
state = { sql = "SELECT state FROM marks WHERE job = :job", result = "value" }

[sql.db.transactions]
land_batch = ["land", "mark"]
`, s.driver, s.host, s.port, s.database(), s.user(), s.page())
	agent := `agent Lander
  goal "Land the orders"
  tool dest from sql "db"
  accepts land start
  on land
    page = dest.page after: 0 size: 10
    done = dest.land_batch job: 1 rows: page state: "landed"
    reply "landed {done.land} marked {done.mark}"
  accepts again start
  on again
    page = dest.page after: 0 size: 10
    dest.land_batch job: 1 rows: page state: "landed"
    reply "this must not happen"
  accepts finish start
  on finish
    changed = dest.finish job: 1
    state = dest.state job: 1
    reply "finished {changed}: {state}"
  accepts count start
  on count
    n = dest.landed job: 1
    reply "landed rows {n}"
  accepts purge start
  on purge
    n = dest.purge job: 1
    reply "purged {n}"
`
	write(t, filepath.Join(dir, "metagente.toml"), config)
	write(t, filepath.Join(dir, "lander.ag"), agent)
	return dir
}

// A batch is landed with its mark in one transaction, a copy of it is refused without leaving anything
// behind and without telling what the rows held, and the other statements change what they say.
func TestAnAgentLandsABatchInOneTransaction(t *testing.T) {
	for _, driver := range []string{"postgres", "mariadb", "mysql", "sqlserver", "oracle"} {
		t.Run(driver, func(t *testing.T) {
			s := start(t, driver)
			dir := s.writeProject(t)
			out, err := metagente(t, dir, dbSecret, "trust", "lander.ag", "--yes")
			if err != nil {
				t.Fatalf("trust: %v\n%s", err, out)
			}
			run := func(message string) (string, error) {
				return metagente(t, dir, dbSecret, "run", "lander.ag", message, "start=x")
			}
			expect(t, run, "land", "landed 10 marked 1")
			refuseACopy(t, run)
			expect(t, run, "count", "landed rows 10")
			expect(t, run, "finish", "finished 1: done")
			expect(t, run, "purge", "purged 10")
		})
	}
}

// expect runs a message and checks that it worked and said what is wanted.
func expect(t *testing.T, run func(string) (string, error), message, want string) {
	t.Helper()
	if out, err := run(message); err != nil || !strings.Contains(out, want) {
		t.Errorf("%s (err = %v):\n%s", message, err, out)
	}
}

// refuseACopy lands the same batch again. The mark is a copy, so the rows of this try must not stay, and
// the problem says nothing about what the rows hold.
func refuseACopy(t *testing.T, run func(string) (string, error)) {
	t.Helper()
	out, err := run("again")
	if err == nil || strings.Contains(out, "this must not happen") {
		t.Fatalf("a copy was accepted (err = %v):\n%s", err, out)
	}
	if strings.Contains(out, "Customer") || strings.Contains(out, dbSecret) {
		t.Errorf("the problem shows what the rows hold:\n%s", out)
	}
}

// merge is an upsert of a mark, written with MERGE in the way each database wants it.
func (s server) merge() string {
	const tail = " ON (m.job = s.job) WHEN MATCHED THEN UPDATE SET state = s.state WHEN NOT MATCHED THEN INSERT (job, state) VALUES (s.job, s.state)"
	switch s.driver {
	case "oracle":
		return "MERGE INTO marks m USING (SELECT :job AS job, :state AS state FROM dual) s" + tail
	case "sqlserver":
		return "MERGE INTO marks m USING (SELECT :job AS job, :state AS state) s" + tail
	}
	return "MERGE INTO marks m USING (SELECT CAST(:job AS bigint) AS job, CAST(:state AS varchar(20)) AS state) s" + tail
}

// A MERGE inserts the row the first time and changes it the second, and counts one row each time.
func TestAnAgentUpsertsWithMerge(t *testing.T) {
	for _, driver := range []string{"postgres", "sqlserver", "oracle"} {
		t.Run(driver, func(t *testing.T) {
			s := start(t, driver)
			dir := s.writeProject(t)
			raw, err := os.ReadFile(filepath.Join(dir, "metagente.toml"))
			if err != nil {
				t.Fatal(err)
			}
			config := string(raw)
			config = strings.Replace(config, "[sql.db.statements]\n", "[sql.db.statements]\nupsert = "+fmt.Sprintf("%q", s.merge())+"\n", 1)
			write(t, filepath.Join(dir, "metagente.toml"), config)
			write(t, filepath.Join(dir, "merger.ag"), `agent Merger
  goal "Upsert a mark"
  tool dest from sql "db"
  accepts put start
  on put
    first = dest.upsert job: 7 state: "new"
    second = dest.upsert job: 7 state: "done"
    state = dest.state job: 7
    reply "merged {first} {second}: {state}"
`)
			if out, err := metagente(t, dir, dbSecret, "trust", "merger.ag", "--yes"); err != nil {
				t.Fatalf("trust: %v\n%s", err, out)
			}
			run := func(message string) (string, error) {
				return metagente(t, dir, dbSecret, "run", "merger.ag", message, "start=x")
			}
			expect(t, run, "put", "merged 1 1: done")
		})
	}
}
