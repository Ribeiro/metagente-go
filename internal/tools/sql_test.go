//go:build !nosqlite

package tools

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/sqlscan"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// newDB makes a SQLite file with some orders and returns its path.
func newDB(t *testing.T, more ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "orders.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	statements := []string{
		"create table orders(id integer primary key, customer text, total real, note text)",
		"insert into orders values (1, 'Ana', 10.5, null), (2, 'Bob', 20, 'gift'), (3, 'Cléo', 30.25, null), (4, 'Dan', 40, 'gift'), (5, 'Eva', 50, null)",
	}
	for _, statement := range append(statements, more...) {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
	return path
}

func statement(t *testing.T, text, result string) *config.SQLStatement {
	t.Helper()
	parsed, err := sqlscan.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if result == "" {
		result = config.ResultRows
	}
	return &config.SQLStatement{Result: result, Parsed: parsed}
}

// standard are the statements most tests use.
func standard(t *testing.T) map[string]*config.SQLStatement {
	return map[string]*config.SQLStatement{
		"page":     statement(t, "select id, customer, total, note from orders where id > :after order by id limit :size", ""),
		"one":      statement(t, "select id, customer from orders where id = :id", config.ResultRow),
		"last":     statement(t, "select max(id) from orders where id > :after", config.ResultValue),
		"by_name":  statement(t, "select id from orders where customer = :name", ""),
		"unnoted":  statement(t, "select id from orders where note is :note order by id", ""),
		"everyone": statement(t, "select id, customer from orders order by id", ""),
	}
}

func sqlTool(t *testing.T, path string, statements map[string]*config.SQLStatement) *SQL {
	t.Helper()
	conn := &config.SQLConn{Name: "orders-db", Driver: "sqlite", Path: path, Statements: statements}
	for name, st := range statements {
		st.Name = name
	}
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
	tool, err := NewSQL(decl, SQLOptions{Conns: map[string]*config.SQLConn{"orders-db": conn}, Getenv: func(string) string { return "" }}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tool.Close() })
	return tool
}

func num(args ...any) Args {
	out := Args{}
	for i := 0; i+1 < len(args); i += 2 {
		switch v := args[i+1].(type) {
		case int:
			out[args[i].(string)] = value.Number(float64(v))
		case string:
			out[args[i].(string)] = value.Text(v)
		case value.Value:
			out[args[i].(string)] = v
		}
	}
	return out
}

func TestAStatementGivesItsRowsAsRecords(t *testing.T) {
	tool := sqlTool(t, newDB(t), standard(t))
	got, err := tool.Call(context.Background(), "page", num("after", 1, "size", 2))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if got.Kind != value.KindList || len(got.List) != 2 {
		t.Fatalf("got %s", got.Display())
	}
	first := got.List[0]
	id, _ := first.Field("id")
	customer, _ := first.Field("customer")
	total, _ := first.Field("total")
	note, _ := first.Field("note")
	if id.Number != 2 || customer.Text != "Bob" || total.Number != 20 || note.Text != "gift" {
		t.Errorf("first row = %s", first.Display())
	}
	third, _ := got.List[1].Field("note")
	if third.Kind != value.KindNothing {
		t.Errorf("a NULL must be nothing, got %s", third.Display())
	}
	if name, _ := got.List[1].Field("customer"); name.Text != "Cléo" {
		t.Errorf("text outside ASCII: %q", name.Text)
	}
}

func TestAnEmptyAnswerIsAnEmptyListOrNothing(t *testing.T) {
	tool := sqlTool(t, newDB(t), standard(t))
	ctx := context.Background()
	rows, err := tool.Call(ctx, "page", num("after", 99, "size", 5))
	if err != nil || rows.Kind != value.KindList || len(rows.List) != 0 {
		t.Errorf("rows: %s, %v", rows.Display(), err)
	}
	row, err := tool.Call(ctx, "one", num("id", 99))
	if err != nil || row.Kind != value.KindNothing {
		t.Errorf("row: %s, %v", row.Display(), err)
	}
	// max() of no rows is a row with a NULL: also nothing.
	last, err := tool.Call(ctx, "last", num("after", 99))
	if err != nil || last.Kind != value.KindNothing {
		t.Errorf("value: %s, %v", last.Display(), err)
	}
}

func TestRowAndValueGiveWhatTheyAreAskedFor(t *testing.T) {
	tool := sqlTool(t, newDB(t), standard(t))
	ctx := context.Background()
	row, err := tool.Call(ctx, "one", num("id", 4))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if customer, _ := row.Field("customer"); customer.Text != "Dan" {
		t.Errorf("row = %s", row.Display())
	}
	last, err := tool.Call(ctx, "last", num("after", 0))
	if err != nil || last.Kind != value.KindNumber || last.Number != 5 {
		t.Errorf("value = %s, %v", last.Display(), err)
	}
}

func TestAValueIsHandedOverAsAParameterNeverWrittenIntoTheStatement(t *testing.T) {
	path := newDB(t)
	tool := sqlTool(t, path, standard(t))
	ctx := context.Background()
	for _, name := range []string{"x' OR '1'='1", "Ana'; DROP TABLE orders; --", `Ana" OR 1=1 --`, "Ana\x00"} {
		got, err := tool.Call(ctx, "by_name", num("name", name))
		if err != nil || len(got.List) != 0 {
			t.Errorf("%q: %s, %v", name, got.Display(), err)
		}
	}
	got, err := tool.Call(ctx, "by_name", num("name", "Ana"))
	if err != nil || len(got.List) != 1 {
		t.Errorf("Ana: %s, %v", got.Display(), err)
	}
	// The table is still there.
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var n int
	if err := db.QueryRow("select count(*) from orders").Scan(&n); err != nil || n != 5 {
		t.Errorf("count = %d, %v", n, err)
	}
}

func TestTheKindsOfValueThatAParameterTakes(t *testing.T) {
	tool := sqlTool(t, newDB(t), standard(t))
	ctx := context.Background()
	// nothing is NULL, and IS compares with NULL.
	got, err := tool.Call(ctx, "unnoted", Args{"note": value.Nothing})
	if err != nil || len(got.List) != 3 {
		t.Errorf("nothing: %s, %v", got.Display(), err)
	}
	got, err = tool.Call(ctx, "unnoted", num("note", "gift"))
	if err != nil || len(got.List) != 2 {
		t.Errorf("text: %s, %v", got.Display(), err)
	}
	// A list or a record is not a value for a parameter.
	for _, bad := range []value.Value{value.List([]value.Value{value.Number(1)}), value.Record(map[string]value.Value{})} {
		_, err = tool.Call(ctx, "unnoted", Args{"note": bad})
		mustContain(t, rendered(t, err), "`note` in `orders.unnoted`", "a text, a number, yes or no, or nothing")
	}
}

func TestAWholeNumberBeyondWhatANumberHoldsComesAsTextAndGoesBackAsAParameter(t *testing.T) {
	path := newDB(t, "insert into orders values (9007199254740993, 'Big', 1, null)")
	tool := sqlTool(t, path, standard(t))
	ctx := context.Background()
	got, err := tool.Call(ctx, "last", num("after", 5))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if got.Kind != value.KindText || got.Text != "9007199254740993" {
		t.Fatalf("got %s (%v), want the text with all its digits", got.Display(), got.Kind)
	}
	row, err := tool.Call(ctx, "one", Args{"id": got})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if customer, _ := row.Field("customer"); customer.Text != "Big" {
		t.Errorf("the id did not go back as it came: %s", row.Display())
	}
}

func TestTheCallsAreCheckedAgainstTheStatement(t *testing.T) {
	tool := sqlTool(t, newDB(t), standard(t))
	ctx := context.Background()
	_, err := tool.Call(ctx, "page", num("after", 1))
	mustContain(t, rendered(t, err), "`orders.page` needs a value for `size`", "orders.page size: ...")
	_, err = tool.Call(ctx, "page", num("after", 1, "size", 2, "extra", 3))
	mustContain(t, rendered(t, err), "takes no value called `extra`", "it takes: after, size")
	_, err = tool.Call(ctx, "pagee", num("after", 1, "size", 2))
	mustContain(t, rendered(t, err), "no action called `pagee`", "did you mean `orders.page`?")
	_, err = tool.Call(ctx, "everyone", num("x", 1))
	mustContain(t, rendered(t, err), "takes no value called `x`", "this statement takes no values")
}

func TestTheDatabaseIsOpenedToBeReadOnly(t *testing.T) {
	path := newDB(t)
	// A statement that writes cannot be written in metagente.toml; this one is put in by hand, to show that the
	// database refuses it too.
	writes := map[string]*config.SQLStatement{
		"wipe": {Result: config.ResultRows, Parsed: &sqlscan.Statement{Text: "delete from orders"}},
	}
	tool := sqlTool(t, path, writes)
	_, err := tool.Call(context.Background(), "wipe", Args{})
	mustContain(t, rendered(t, err), "the database could not run `orders.wipe`")
	db, _ := sql.Open("sqlite", path)
	defer db.Close()
	var n int
	if err := db.QueryRow("select count(*) from orders").Scan(&n); err != nil || n != 5 {
		t.Errorf("the rows are %d, %v", n, err)
	}
}

func TestAnAnswerHasACeilingOfRowsAndOfBytes(t *testing.T) {
	path := newDB(t)
	statements := standard(t)
	limits := config.Default().Limits
	limits.MaxSQLRows = 3
	tool := sqlTool(t, path, statements)
	tool.limits = limits
	_, err := tool.Call(context.Background(), "everyone", Args{})
	mustContain(t, rendered(t, err), "more than the 3 rows an answer may have", "LIMIT", "max_sql_rows")
	// Up to the ceiling is fine, and one more is not.
	got, err := tool.Call(context.Background(), "page", num("after", 0, "size", 3))
	if err != nil || len(got.List) != 3 {
		t.Errorf("at the ceiling: %s, %v", got.Display(), err)
	}
	_, err = tool.Call(context.Background(), "page", num("after", 0, "size", 4))
	mustContain(t, rendered(t, err), "more than the 3 rows an answer may have")

	limits = config.Default().Limits
	limits.MaxSQLBytes = 40
	tool.limits = limits
	_, err = tool.Call(context.Background(), "everyone", Args{})
	mustContain(t, rendered(t, err), "larger than the 40 bytes", "max_sql_bytes")
}

func TestRowAndValueRefuseSeveralRowsOrColumns(t *testing.T) {
	statements := map[string]*config.SQLStatement{
		"two_rows":    statement(t, "select id from orders", config.ResultRow),
		"two_rows_v":  statement(t, "select id from orders", config.ResultValue),
		"two_columns": statement(t, "select id, customer from orders limit 1", config.ResultValue),
	}
	tool := sqlTool(t, newDB(t), statements)
	ctx := context.Background()
	for _, name := range []string{"two_rows", "two_rows_v"} {
		_, err := tool.Call(ctx, name, Args{})
		mustContain(t, rendered(t, err), "gives more than one row", "LIMIT 1")
	}
	_, err := tool.Call(ctx, "two_columns", Args{})
	mustContain(t, rendered(t, err), "gives 2 columns", "use result = \"row\"")
}

func TestAColumnMustBeNamedLikeAFieldOfARecord(t *testing.T) {
	statements := map[string]*config.SQLStatement{
		"count":     statement(t, "select count(*) from orders", ""),
		"twice":     statement(t, "select id as x, customer as x from orders", ""),
		"dashed":    statement(t, `select id as "order-id", customer as _who from orders limit 1`, ""),
		"digit":     statement(t, `select id as "1st" from orders`, ""),
		"with_name": statement(t, "select count(*) as total from orders", config.ResultValue),
	}
	tool := sqlTool(t, newDB(t), statements)
	ctx := context.Background()
	_, err := tool.Call(ctx, "count", Args{})
	mustContain(t, rendered(t, err), "a column called `count(*)`", "count(*) AS total")
	_, err = tool.Call(ctx, "twice", Args{})
	mustContain(t, rendered(t, err), "two columns called `x`")
	_, err = tool.Call(ctx, "digit", Args{})
	mustContain(t, rendered(t, err), "a column called `1st`")
	got, err := tool.Call(ctx, "dashed", Args{})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if _, ok := got.List[0].Field("order-id"); !ok {
		t.Errorf("a dash is part of a name: %s", got.Display())
	}
	if total, err := tool.Call(ctx, "with_name", Args{}); err != nil || total.Number != 5 {
		t.Errorf("total = %s, %v", total.Display(), err)
	}
}

func TestBinaryDataIsRefusedAndTextInABlobIsText(t *testing.T) {
	path := newDB(t,
		"create table files(id integer primary key, body blob)",
		"insert into files values (1, X'48656C6C6F'), (2, X'FF00FE')",
	)
	statements := map[string]*config.SQLStatement{
		"text":   statement(t, "select body from files where id = 1", config.ResultValue),
		"binary": statement(t, "select body from files where id = 2", config.ResultValue),
	}
	tool := sqlTool(t, path, statements)
	got, err := tool.Call(context.Background(), "text", Args{})
	if err != nil || got.Text != "Hello" {
		t.Errorf("text blob: %s, %v", got.Display(), err)
	}
	_, err = tool.Call(context.Background(), "binary", Args{})
	mustContain(t, rendered(t, err), "the column `body` holds binary data", "CAST or hex")
}

func TestTheDatabaseFileIsFoundFromTheFolderOfTheProject(t *testing.T) {
	path := newDB(t)
	root, name := filepath.Dir(path), filepath.Base(path)
	conn := &config.SQLConn{Name: "orders-db", Driver: "sqlite", Path: name, Statements: standard(t)}
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
	tool, err := NewSQL(decl, SQLOptions{Conns: map[string]*config.SQLConn{"orders-db": conn}, Root: root}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close()
	if got, err := tool.Call(context.Background(), "everyone", Args{}); err != nil || len(got.List) != 5 {
		t.Errorf("relative path: %s, %v", got.Display(), err)
	}
}

func TestADatabaseThatIsNotThereIsNotCreated(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.db")
	tool := sqlTool(t, missing, standard(t))
	_, err := tool.Call(context.Background(), "everyone", Args{})
	mustContain(t, rendered(t, err), "the database file of `orders` does not exist", "[sql.orders-db]")
	if _, err := os.Stat(missing); err == nil {
		t.Error("the file was created")
	}
	_, err = sqlTool(t, dir, standard(t)).Call(context.Background(), "everyone", Args{})
	mustContain(t, rendered(t, err), "is a folder, not a file")
	notDatabase := filepath.Join(dir, "text.db")
	if err := os.WriteFile(notDatabase, []byte("this is not a database, just a long enough text to look like a file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = sqlTool(t, notDatabase, standard(t)).Call(context.Background(), "everyone", Args{})
	text := rendered(t, err)
	mustContain(t, text, "I could not open the database of `orders`")
	if strings.Contains(text, dir) {
		t.Errorf("the path of the file is in the message:\n%s", text)
	}
}

func TestAConnectionWithoutAPathSaysSo(t *testing.T) {
	tool := sqlTool(t, "", standard(t))
	_, err := tool.Call(context.Background(), "everyone", Args{})
	mustContain(t, rendered(t, err), "[sql.orders-db] has no path", `path = "data/orders.db"`)
}

func TestACredentialTakesThePlaceOfThePathAndIsNeverShown(t *testing.T) {
	path := newDB(t)
	conn := &config.SQLConn{Name: "orders-db", Driver: "sqlite", Statements: standard(t)}
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
	env := map[string]string{}
	opts := SQLOptions{
		Conns:       map[string]*config.SQLConn{"orders-db": conn},
		Credentials: map[string]string{"orders": "ORDERS_DB"},
		Getenv:      func(name string) string { return env[name] },
	}
	tool, err := NewSQL(decl, opts, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close()
	_, err = tool.Call(context.Background(), "everyone", Args{})
	mustContain(t, rendered(t, err), "the variable ORDERS_DB is empty", "export ORDERS_DB=")

	env["ORDERS_DB"] = path
	if got, err := tool.Call(context.Background(), "everyone", Args{}); err != nil || len(got.List) != 5 {
		t.Errorf("with the variable: %s, %v", got.Display(), err)
	}
	if tool.spec.Credential != "ORDERS_DB" || !strings.Contains(tool.spec.Target, "ORDERS_DB") || strings.Contains(tool.spec.Target, path) {
		t.Errorf("spec = %+v", tool.spec)
	}

	// A secret in an error of the driver is hidden.
	secretPath := filepath.Join(t.TempDir(), "secret-password-123.db")
	if err := os.WriteFile(secretPath, []byte("not a database, but long enough to be read as a file by the driver\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env["ORDERS_DB"] = secretPath
	other, err := NewSQL(decl, opts, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.Call(context.Background(), "everyone", Args{})
	if text := rendered(t, err); strings.Contains(text, "secret-password-123") {
		t.Errorf("the secret is in the message:\n%s", text)
	}
}

func TestTheApprovalIsAskedBeforeTheDatabaseIsOpened(t *testing.T) {
	path := newDB(t)
	var asked []SQLSpec
	deny := true
	conn := &config.SQLConn{Name: "orders-db", Driver: "sqlite", Path: path, Statements: standard(t)}
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
	tool, err := NewSQL(decl, SQLOptions{
		Conns: map[string]*config.SQLConn{"orders-db": conn},
		Allow: func(spec SQLSpec) error {
			asked = append(asked, spec)
			if deny {
				return fmt.Errorf("not approved")
			}
			return nil
		},
	}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close()
	if _, err := tool.Call(context.Background(), "everyone", Args{}); err == nil || !strings.Contains(err.Error(), "not approved") {
		t.Fatalf("err = %v", err)
	}
	if len(tool.opts.Pool.dbs) != 0 {
		t.Error("the database was opened without approval")
	}
	deny = false
	if _, err := tool.Call(context.Background(), "everyone", Args{}); err != nil {
		t.Fatal(rendered(t, err))
	}
	spec := asked[len(asked)-1]
	if spec.Tool != "orders" || spec.Connection != "orders-db" || spec.Driver != "sqlite" || spec.Target != path ||
		strings.Join(spec.Statements, " ") != "by_name everyone last one page unnoted" || len(spec.Fingerprint) != 12 {
		t.Errorf("spec = %+v", spec)
	}
}

func TestTheFingerprintChangesWithTheTextOfAStatementOrTheDatabase(t *testing.T) {
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
	fingerprint := func(path, text, result string) string {
		conn := &config.SQLConn{Name: "orders-db", Driver: "sqlite", Path: path,
			Statements: map[string]*config.SQLStatement{"all": statement(t, text, result)}}
		spec, err := SQLSpecOf(decl, map[string]*config.SQLConn{"orders-db": conn}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return spec.Fingerprint
	}
	base := fingerprint("a.db", "select id from orders", "")
	if fingerprint("a.db", "select id from orders", "") != base {
		t.Error("the same connection gave another fingerprint")
	}
	for name, other := range map[string]string{
		"text":   fingerprint("a.db", "select id, customer from orders", ""),
		"result": fingerprint("a.db", "select id from orders", config.ResultRow),
		"path":   fingerprint("b.db", "select id from orders", ""),
	} {
		if other == base {
			t.Errorf("a change of the %s kept the fingerprint", name)
		}
	}
}

func TestAConnectionThatIsNotInTheConfigurationIsExplained(t *testing.T) {
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
	_, err := NewSQL(decl, SQLOptions{}, config.Default().Limits)
	mustContain(t, rendered(t, err), "the tool `orders` uses the connection `orders-db`", "no section [sql.orders-db]")
}

func TestTheStatementsAreTheActionsOfTheTool(t *testing.T) {
	statements := standard(t)
	statements["page"].Description = "A page of orders"
	tool := sqlTool(t, newDB(t), statements)
	actions, err := tool.Actions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, a := range actions {
		names = append(names, a.Name)
		if a.Mutates {
			t.Errorf("%s changes things", a.Name)
		}
		if a.Name == "page" {
			if a.Description != "A page of orders" || len(a.Params) != 2 || a.Params[0].Name != "after" || !a.Params[1].Required {
				t.Errorf("page = %+v", a)
			}
		}
	}
	if strings.Join(names, " ") != "by_name everyone last one page unnoted" {
		t.Errorf("actions = %v", names)
	}
	for _, a := range actions {
		if a.Name == "last" && !strings.Contains(a.Description, "its value") {
			t.Errorf("the default description says nothing of the shape: %q", a.Description)
		}
	}
}

func TestACancelledCallStopsWithTheReasonOfTheCancel(t *testing.T) {
	tool := sqlTool(t, newDB(t), standard(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := tool.Call(ctx, "everyone", Args{})
	if err == nil || ctx.Err() == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("err = %v", err)
	}
}

func TestSeveralCallsAtTheSameTimeShareOneDatabase(t *testing.T) {
	tool := sqlTool(t, newDB(t), standard(t))
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := tool.Call(context.Background(), "everyone", Args{})
			if err == nil && len(got.List) != 5 {
				err = fmt.Errorf("got %s", got.Display())
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if len(tool.opts.Pool.dbs) != 1 {
		t.Errorf("%d databases were opened", len(tool.opts.Pool.dbs))
	}
}

func TestTwoToolsOfOnePoolOpenTheDatabaseOnce(t *testing.T) {
	path := newDB(t)
	pool := NewSQLPool()
	defer pool.Close()
	for i := 0; i < 2; i++ {
		conn := &config.SQLConn{Name: "orders-db", Driver: "sqlite", Path: path, Statements: standard(t)}
		decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
		tool, err := NewSQL(decl, SQLOptions{Conns: map[string]*config.SQLConn{"orders-db": conn}, Pool: pool}, config.Default().Limits)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tool.Call(context.Background(), "everyone", Args{}); err != nil {
			t.Fatal(rendered(t, err))
		}
		if tool.owned || tool.Close() != nil {
			t.Error("a tool of a shared pool closed it")
		}
	}
	if len(pool.dbs) != 1 {
		t.Errorf("%d databases in the pool", len(pool.dbs))
	}
}

func TestBuildMakesTheToolOfASQLDeclaration(t *testing.T) {
	path := newDB(t)
	agents, err := lang.ParseFile("a.ag", "", "agent A\n  goal \"x\"\n  tool orders from sql \"orders-db\"\n  accepts go\n  on go\n    reply 1\n")
	if err != nil {
		t.Fatal(err)
	}
	conn := &config.SQLConn{Name: "orders-db", Driver: "sqlite", Path: path, Statements: standard(t)}
	registry, err := Build(agents[0], Options{Root: t.TempDir(), Limits: config.Default().Limits,
		SQL: SQLOptions{Conns: map[string]*config.SQLConn{"orders-db": conn}}})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	tool, ok := registry["orders"].(*SQL)
	if !ok {
		t.Fatalf("the tool is %T", registry["orders"])
	}
	defer tool.Close()
	_, err = Build(agents[0], Options{Root: t.TempDir(), Limits: config.Default().Limits})
	mustContain(t, rendered(t, err), "no section [sql.orders-db]")
}

func TestWhatTheDriverSaysIsShortAndHasNoSecret(t *testing.T) {
	conn := &config.SQLConn{Name: "orders-db", Driver: "sqlite", Statements: standard(t)}
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "orders-db"}
	tool, err := NewSQL(decl, SQLOptions{
		Conns:       map[string]*config.SQLConn{"orders-db": conn},
		Credentials: map[string]string{"orders": "ORDERS_DB"},
		Getenv:      func(string) string { return "/srv/secret-password-123/orders.db" },
	}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	defer tool.Close()
	said := tool.clean(fmt.Errorf("cannot open /srv/secret-password-123/orders.db\nbecause %s", strings.Repeat("of a long reason ", 100)))
	if strings.Contains(said, "secret-password-123") || strings.Contains(said, "\n") || len(said) > 310 {
		t.Errorf("clean = %q", said)
	}
	opened := tool.openFailure(fmt.Errorf("cannot open /srv/secret-password-123/orders.db"), "/srv/secret-password-123/orders.db")
	mustContain(t, rendered(t, opened), "I could not open the database of `orders`")
	if strings.Contains(rendered(t, opened), "secret-password-123") {
		t.Errorf("the secret is in:\n%s", rendered(t, opened))
	}
}

func TestThePathOfTheFileIsNotInTheMessageOfAFailureToOpenIt(t *testing.T) {
	// No credential here: the path is not a secret, and it is still left out, since it says
	// where the files of the computer are.
	tool := sqlTool(t, "/srv/data/orders.db", standard(t))
	opened := rendered(t, tool.openFailure(fmt.Errorf("cannot open /srv/data/orders.db: bad"), "/srv/data/orders.db"))
	mustContain(t, opened, "I could not open the database of `orders`: cannot open the database: bad")
	if strings.Contains(opened, "/srv/data") {
		t.Errorf("the path is in:\n%s", opened)
	}
}
