//go:build !nosqlite

package tools

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// writer is a tool on a SQLite file that may change rows. The file has the table orders and an empty
// table marks(batch, state).
func writer(t *testing.T) (*SQL, string) {
	t.Helper()
	path := newDB(t, "create table marks(batch integer primary key, state text)",
		"create table landing(batch integer, id integer, customer text, primary key (batch, id))")
	statements := map[string]*config.SQLStatement{
		"mark":     writing(t, "insert into marks (batch, state) values (:batch, :state)", ""),
		"finish":   writing(t, "update marks set state = 'done' where batch = :batch", ""),
		"purge":    writing(t, "delete from landing where batch = :batch", ""),
		"land":     writing(t, "insert into landing (batch, id, customer) values (:batch, :id, :customer)", "rows", "id", "customer"),
		"count":    writing(t, "select count(*) from landing where batch = :batch", ""),
		"in_batch": writing(t, "select id, customer from landing where batch = :batch order by id", ""),
	}
	statements["count"].Result = config.ResultValue
	for name, st := range statements {
		st.Name = name
	}
	conn := &config.SQLConn{
		Name: "orders-db", Driver: "sqlite", Path: path, Mode: config.ModeWrite, Statements: statements,
		Transactions: map[string]*config.SQLTransaction{
			"land_batch": {Name: "land_batch", Steps: []string{"land", "mark"}},
		},
	}
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
	tool, err := NewSQL(decl, SQLOptions{Conns: map[string]*config.SQLConn{"orders-db": conn}, Getenv: func(string) string { return "" }}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tool.Close() })
	return tool, path
}

func rowsOf(items ...[]value.Value) value.Value {
	list := make([]value.Value, len(items))
	for i, item := range items {
		list[i] = value.List(item)
	}
	return value.List(list)
}

func TestAStatementThatChangesRowsGivesHowManyItChanged(t *testing.T) {
	tool, _ := writer(t)
	ctx := context.Background()
	got, err := tool.Call(ctx, "mark", num("batch", 1, "state", "landed"))
	if err != nil || got.Kind != value.KindNumber || got.Number != 1 {
		t.Fatalf("insert: %s, %v", got.Display(), rendered(t, err))
	}
	got, err = tool.Call(ctx, "finish", num("batch", 1))
	if err != nil || got.Number != 1 {
		t.Fatalf("update: %s, %v", got.Display(), rendered(t, err))
	}
	got, err = tool.Call(ctx, "finish", num("batch", 99))
	if err != nil || got.Number != 0 {
		t.Fatalf("update of nothing: %s, %v", got.Display(), rendered(t, err))
	}
}

func TestAStatementRunsOnceForEachItemOfAList(t *testing.T) {
	tool, _ := writer(t)
	ctx := context.Background()
	rows := rowsOf([]value.Value{value.Number(1), value.Text("Ana")}, []value.Value{value.Number(2), value.Text("Bob")})
	got, err := tool.Call(ctx, "land", num("batch", 7, "rows", rows))
	if err != nil || got.Number != 2 {
		t.Fatalf("got %s, %v", got.Display(), rendered(t, err))
	}
	// An item may also be a record.
	record := value.List([]value.Value{value.Record(map[string]value.Value{"id": value.Number(3), "customer": value.Text("Cléo")})})
	if _, err := tool.Call(ctx, "land", num("batch", 7, "rows", record)); err != nil {
		t.Fatal(rendered(t, err))
	}
	count, err := tool.Call(ctx, "count", num("batch", 7))
	if err != nil || count.Number != 3 {
		t.Fatalf("count = %s, %v", count.Display(), err)
	}
}

func TestAListThatFailsHalfwayLeavesNothing(t *testing.T) {
	tool, _ := writer(t)
	ctx := context.Background()
	rows := rowsOf(
		[]value.Value{value.Number(1), value.Text("Ana")},
		[]value.Value{value.Number(2), value.Text("Bob")},
		[]value.Value{value.Number(1), value.Text("a copy of the first")}, // the key (batch, id) is taken
	)
	_, err := tool.Call(ctx, "land", num("batch", 8, "rows", rows))
	if err == nil {
		t.Fatal("the copy of a key must be refused")
	}
	shown := rendered(t, err)
	if !strings.Contains(shown, "item 3") || strings.Contains(shown, "a copy of the first") || strings.Contains(shown, "Ana") {
		t.Errorf("the problem must say where, and never the content:\n%s", shown)
	}
	count, _ := tool.Call(ctx, "count", num("batch", 8))
	if count.Number != 0 {
		t.Errorf("rows left behind: %s", count.Display())
	}
}

func TestATransactionIsAllOrNone(t *testing.T) {
	tool, _ := writer(t)
	ctx := context.Background()
	rows := rowsOf([]value.Value{value.Number(1), value.Text("Ana")})
	got, err := tool.Call(ctx, "land_batch", num("batch", 5, "rows", rows, "state", "landed"))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if land, _ := got.Field("land"); land.Number != 1 {
		t.Errorf("answer = %s", got.Display())
	}
	if mark, _ := got.Field("mark"); mark.Number != 1 {
		t.Errorf("answer = %s", got.Display())
	}
	// The same batch again: the mark is refused, so the rows of this try must not stay.
	more := rowsOf([]value.Value{value.Number(2), value.Text("Bob")})
	if _, err := tool.Call(ctx, "land_batch", num("batch", 5, "rows", more, "state", "landed")); err == nil {
		t.Fatal("a batch marked twice must be refused")
	}
	count, _ := tool.Call(ctx, "count", num("batch", 5))
	if count.Number != 1 {
		t.Errorf("the rows of the refused transaction stayed: %s", count.Display())
	}
}

func TestACallOfAWriteGivesEveryValueAndNoOther(t *testing.T) {
	tool, _ := writer(t)
	ctx := context.Background()
	for name, c := range map[string]struct {
		action string
		args   Args
		want   string
	}{
		"missing":      {"mark", num("batch", 1), "needs a value for `state`"},
		"extra":        {"mark", num("batch", 1, "state", "x", "other", 1), "takes no value called `other`"},
		"not a list":   {"land", num("batch", 1, "rows", 5), "has to be a list"},
		"a bad item":   {"land", num("batch", 1, "rows", value.List([]value.Value{value.Number(1)})), "has to be a record or a list"},
		"a short item": {"land", num("batch", 1, "rows", rowsOf([]value.Value{value.Number(1)})), "has 1 values, and 2 are needed"},
		"no field":     {"land", num("batch", 1, "rows", value.List([]value.Value{value.Record(map[string]value.Value{"id": value.Number(1)})})), "no value called `customer`"},
		"transaction":  {"land_batch", num("batch", 1), "needs a value for"},
	} {
		_, err := tool.Call(ctx, c.action, c.args)
		if err == nil || !strings.Contains(rendered(t, err), c.want) {
			t.Errorf("%s: want %q, got %v", name, c.want, err)
		}
	}
}

func TestTheListOfACallHasAnEnd(t *testing.T) {
	tool, _ := writer(t)
	tool.limits.MaxSQLWriteRows = 2
	rows := rowsOf(
		[]value.Value{value.Number(1), value.Text("a")}, []value.Value{value.Number(2), value.Text("b")}, []value.Value{value.Number(3), value.Text("c")})
	_, err := tool.Call(context.Background(), "land", num("batch", 1, "rows", rows))
	if err == nil || !strings.Contains(rendered(t, err), "has 3 items, and 2 is the most") {
		t.Errorf("err = %v", err)
	}
}

func TestADeleteWithAWhereRunsAndTheReadsStillWork(t *testing.T) {
	tool, _ := writer(t)
	ctx := context.Background()
	rows := rowsOf([]value.Value{value.Number(1), value.Text("Ana")}, []value.Value{value.Number(2), value.Text("Bob")})
	if _, err := tool.Call(ctx, "land", num("batch", 3, "rows", rows)); err != nil {
		t.Fatal(rendered(t, err))
	}
	list, err := tool.Call(ctx, "in_batch", num("batch", 3))
	if err != nil || len(list.List) != 2 {
		t.Fatalf("read: %s, %v", list.Display(), err)
	}
	if got, err := tool.Call(ctx, "purge", num("batch", 3)); err != nil || got.Number != 2 {
		t.Fatalf("purge: %s, %v", got.Display(), err)
	}
}

func TestTheActionsOfAConnectionThatWritesAreItsStatementsAndTransactions(t *testing.T) {
	tool, _ := writer(t)
	actions, err := tool.Actions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	params := map[string]string{}
	for _, a := range actions {
		var names []string
		for _, p := range a.Params {
			names = append(names, p.Name)
		}
		params[a.Name] = strings.Join(names, ",")
	}
	if params["land"] != "batch,rows" || params["land_batch"] != "batch,state,rows" || params["finish"] != "batch" {
		t.Errorf("params = %v", params)
	}
}

func TestAConnectionThatOnlyReadsCannotChangeTheFile(t *testing.T) {
	path := newDB(t)
	tool := sqlTool(t, path, standard(t))
	if _, err := tool.Call(context.Background(), "everyone", Args{}); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path+"?mode=ro&_pragma=query_only(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("insert into orders (id) values (99)"); err == nil {
		t.Error("the helper must not be able to write either")
	}
}

func TestTheMessageOfADriverLosesWhatIsBetweenQuotes(t *testing.T) {
	got := scrubValues(`Duplicate entry 'ana@example.org' for key "orders.email" and ` + "`x`" + `: invalid input "abc"`)
	if strings.ContainsAny(got, "@") || strings.Contains(got, "abc") || strings.Contains(got, "orders.email") {
		t.Errorf("scrubbed = %s", got)
	}
	if !strings.Contains(got, "Duplicate entry") {
		t.Errorf("the rest of the message must stay: %s", got)
	}
}
