//go:build !nosqlite

package runtime

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/trust"
)

const ordersToml = `
[sql.orders-db]
driver = "sqlite"
path = "orders.db"

[sql.orders-db.statements]
next_page = "SELECT id, customer FROM orders WHERE id > :after ORDER BY id LIMIT :size"
last_id = { sql = "SELECT id FROM (SELECT id FROM orders WHERE id > :after ORDER BY id LIMIT :size) ORDER BY id DESC LIMIT 1", result = "value" }
one = { sql = "SELECT id, customer FROM orders WHERE id = :id", result = "row" }
`

const pagerSource = `agent Pager
  goal "Read the orders in pages"
  tool orders from sql "orders-db"
  accepts all
  on all
    after = 0
    last = 0
    repeat while after is not nothing
      last = after
      page = orders.next_page after: after size: 10
      after = orders.last_id after: after size: 10
    one = orders.one id: 7
    reply "last id {last}; order 7 is {one.customer}"
`

// sqlProject is a folder with a database of 25 orders, a metagente.toml, and an agent that reads them.
func sqlProject(t *testing.T, toml string) (rt *Runtime, dir, approvals string) {
	t.Helper()
	dir = t.TempDir()
	approvals = filepath.Join(t.TempDir(), "approvals")
	db, err := sql.Open("sqlite", filepath.Join(dir, "orders.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("create table orders(id integer primary key, customer text)"); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 25; i++ {
		if _, err := db.Exec("insert into orders values (?, ?)", i, "Customer "+string(rune('0'+i/10))+string(rune('0'+i%10))); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	return sqlRuntime(t, dir, toml, approvals), dir, approvals
}

func sqlRuntime(t *testing.T, dir, toml, approvals string) *Runtime {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dir, "")
	if err != nil {
		t.Fatal(errText(t, err))
	}
	rt := New(cfg)
	rt.Trust = trust.NewRegistry(approvals)
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func approveAll(missing []trust.Item) bool { return true }

func TestAnAgentReadsADatabaseInPagesWithRepeatWhile(t *testing.T) {
	rt, dir, _ := sqlProject(t, ordersToml)
	file := filepath.Join(dir, "pager.ag")
	if err := os.WriteFile(file, []byte(pagerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := RunFile(t.Context(), rt, Options{File: file, Message: "all", Confirm: approveAll})
	if err != nil || got.Text != "last id 25; order 7 is Customer 07" {
		t.Fatalf("got %q, %v", got.Display(), err)
	}
	if err := rt.Close(); err != nil {
		t.Errorf("closing: %v", err)
	}
}

func TestADatabaseNobodyApprovedIsNotOpened(t *testing.T) {
	rt, dir, _ := sqlProject(t, ordersToml)
	file := filepath.Join(dir, "pager.ag")
	if err := os.WriteFile(file, []byte(pagerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RunFile(t.Context(), rt, Options{File: file, Message: "all", Confirm: func(missing []trust.Item) bool {
		if len(missing) != 1 || missing[0].Kind != trust.KindSQL {
			t.Errorf("the person was asked about %v", missing)
		}
		return false
	}})
	mustContain(t, errText(t, err), "reads the database: sqlite orders.db", "connection orders-db", "statements last_id, next_page, one", "metagente trust")
}

func TestTheDatabaseIsCheckedAgainOnTheCallForWhatAppearedAfterTheStart(t *testing.T) {
	rt, _, _ := sqlProject(t, ordersToml)
	agents, err := lang.ParseFile("pager.ag", "", pagerSource)
	if err != nil {
		t.Fatal(err)
	}
	agent, err := NewAgent(rt, agents[0], "c")
	if err != nil {
		t.Fatal(errText(t, err))
	}
	_, err = ask(agent, "all")
	mustContain(t, errText(t, err), "the database of `orders` has not been approved for this project", "metagente trust")
}

func TestChangingAStatementAsksForTheApprovalAgain(t *testing.T) {
	rt, dir, approvals := sqlProject(t, ordersToml)
	agents, err := lang.ParseFile("pager.ag", "", pagerSource)
	if err != nil {
		t.Fatal(err)
	}
	if err := rt.Trust.Approve(rt.Config.Root, rt.Needs(agents)); err != nil {
		t.Fatal(err)
	}
	if missing, _ := rt.Trust.Missing(rt.Config.Root, rt.Needs(agents)); len(missing) != 0 {
		t.Fatalf("missing right after approving: %v", missing)
	}

	// The same project, with the text of one statement changed in metagente.toml.
	edited := strings.Replace(ordersToml, "ORDER BY id LIMIT :size\"\nlast_id", "ORDER BY id DESC LIMIT :size\"\nlast_id", 1)
	if edited == ordersToml {
		t.Fatal("the test did not change the statement")
	}
	changed := sqlRuntime(t, dir, edited, approvals)
	missing, err := changed.Trust.Missing(changed.Config.Root, changed.Needs(agents))
	if err != nil || len(missing) != 1 {
		t.Fatalf("after the change: %v, %v", missing, err)
	}

	// A change that does not touch the statements is not asked again.
	same := sqlRuntime(t, dir, ordersToml+"\n[runtime]\ntimeout_seconds = 12\n", approvals)
	if missing, _ := same.Trust.Missing(same.Config.Root, same.Needs(agents)); len(missing) != 0 {
		t.Errorf("asked again for nothing: %v", missing)
	}
}

func TestAConnectionThatIsNotInTheConfigurationStopsTheAgentWithAnExplanation(t *testing.T) {
	rt, dir, _ := sqlProject(t, "[runtime]\ntimeout_seconds = 30\n")
	file := filepath.Join(dir, "pager.ag")
	if err := os.WriteFile(file, []byte(pagerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RunFile(t.Context(), rt, Options{File: file, Message: "all", Confirm: approveAll})
	mustContain(t, errText(t, err), "the tool `orders` uses the connection `orders-db`", "no section [sql.orders-db]")
}

func TestAStatementThatHasNoSuchActionIsExplainedWithTheOnesThereAre(t *testing.T) {
	rt, dir, _ := sqlProject(t, ordersToml)
	source := "agent A\n  goal \"x\"\n  tool orders from sql \"orders-db\"\n  accepts go\n  on go\n    reply orders.nextpage after: 0 size: 1\n"
	file := filepath.Join(dir, "a.ag")
	if err := os.WriteFile(file, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := RunFile(t.Context(), rt, Options{File: file, Message: "go", Confirm: approveAll})
	mustContain(t, errText(t, err), "`orders` has no action called `nextpage`", "did you mean `orders.next_page`?")
}

const landingToml = `
[sql.dest]
driver = "sqlite"
path = "orders.db"
mode = "write"

[sql.dest.statements]
land = { sql = "INSERT INTO landing (job, id, customer) VALUES (:job, :id, :customer)", each = "rows", columns = ["id", "customer"] }
mark = "INSERT INTO marks (job, state) VALUES (:job, :state)"
done = "UPDATE marks SET state = 'done' WHERE job = :job"
total = { sql = "SELECT count(*) FROM landing WHERE job = :job", result = "value" }
first = "SELECT id, customer FROM orders ORDER BY id LIMIT 2"

[sql.dest.transactions]
land_batch = ["land", "mark"]
`

const landerSource = `agent Lander
  goal "Land a batch"
  tool dest from sql "dest"
  accepts land job
  on land
    rows = dest.first
    done = dest.land_batch job: job rows: rows state: "landed"
    dest.done job: job
    n = dest.total job: job
    reply "landed {n} rows, {done.land} by the transaction"
`

func TestAnAgentChangesADatabaseThatWasApprovedAsOneThatIsChanged(t *testing.T) {
	rt, dir, _ := sqlProject(t, landingToml)
	db, err := sql.Open("sqlite", filepath.Join(dir, "orders.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"create table landing(job integer, id integer, customer text, primary key (job, id))",
		"create table marks(job integer primary key, state text)",
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	file := filepath.Join(dir, "lander.ag")
	if err := os.WriteFile(file, []byte(landerSource), 0o644); err != nil {
		t.Fatal(err)
	}
	asked := false
	got, err := RunFile(t.Context(), rt, Options{File: file, Message: "land", Params: []string{"job=7"}, Confirm: func(missing []trust.Item) bool {
		asked = true
		if len(missing) != 1 || !missing[0].Writes {
			t.Errorf("the person must be told that the database is changed: %v", missing)
		}
		return true
	}})
	if err != nil || got.Text != "landed 2 rows, 2 by the transaction" {
		t.Fatalf("got %q, %v", got.Display(), err)
	}
	if !asked {
		t.Error("nobody was asked")
	}
	check, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(dir, "orders.db"))+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var state string
	if err := check.QueryRow("select state from marks where job = 7").Scan(&state); err != nil || state != "done" {
		t.Errorf("state = %q, %v", state, err)
	}
}
