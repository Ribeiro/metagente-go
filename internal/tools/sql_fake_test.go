package tools

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/sqlscan"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// fakeDriver is a database that keeps a note of how it was used: what the tests of the network drivers
// need to know without a server (the integration tests use the real ones).
type fakeDriver struct {
	mu       sync.Mutex
	dsns     []string
	begun    []bool // read only, for each transaction
	prepared []string
	queried  []string
	executed []string
	// textNumber makes the fake database hand over its number as the text "7" of a column called NUMBER, as Oracle does.
	textNumber bool
}

var fake = &fakeDriver{}

func init() { sql.Register("fakesql", fake) }

func (f *fakeDriver) Open(dsn string) (driver.Conn, error) {
	f.mu.Lock()
	f.dsns = append(f.dsns, dsn)
	f.mu.Unlock()
	if strings.Contains(dsn, "refuse") {
		return nil, io.ErrUnexpectedEOF
	}
	return &fakeConn{f}, nil
}

func (f *fakeDriver) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dsns, f.begun, f.prepared, f.queried, f.executed = nil, nil, nil, nil, nil
}

type fakeConn struct{ f *fakeDriver }

func (c *fakeConn) Prepare(query string) (driver.Stmt, error) {
	c.f.mu.Lock()
	c.f.prepared = append(c.f.prepared, query)
	c.f.mu.Unlock()
	return &fakeStmt{c.f, query}, nil
}
func (c *fakeConn) Close() error { return nil }
func (c *fakeConn) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}
func (c *fakeConn) BeginTx(_ context.Context, opts driver.TxOptions) (driver.Tx, error) {
	c.f.mu.Lock()
	c.f.begun = append(c.f.begun, opts.ReadOnly)
	c.f.mu.Unlock()
	return fakeTx{}, nil
}
func (c *fakeConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	c.f.mu.Lock()
	c.f.queried = append(c.f.queried, query)
	c.f.mu.Unlock()
	return &fakeRows{}, nil
}

type fakeTx struct{}

func (fakeTx) Commit() error   { return nil }
func (fakeTx) Rollback() error { return nil }

type fakeStmt struct {
	f     *fakeDriver
	query string
}

func (s *fakeStmt) Close() error  { return nil }
func (s *fakeStmt) NumInput() int { return -1 }
func (s *fakeStmt) Exec([]driver.Value) (driver.Result, error) {
	s.f.mu.Lock()
	s.f.executed = append(s.f.executed, s.query)
	s.f.mu.Unlock()
	if strings.Contains(s.query, "deadlock") {
		return nil, io.ErrUnexpectedEOF
	}
	return fakeResult{}, nil
}

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error)               { return 0, nil }
func (fakeResult) RowsAffected() (int64, error)               { return 3, nil }
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error) { return &fakeRows{}, nil }

type fakeRows struct{ done bool }

func (r *fakeRows) Columns() []string { return []string{"id"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	if fake.textNumber {
		dest[0] = "7"
		return nil
	}
	dest[0] = int64(7)
	return nil
}

func (r *fakeRows) ColumnTypeDatabaseTypeName(int) string { return "NUMBER" }

// fakeTool is a tool on a network database that is the fake one.
func fakeTool(t *testing.T, readOnlyTx, prepare bool, password string, host string) *SQL {
	t.Helper()
	sqlDrivers["fake"] = sqlDriver{name: "fakesql", readOnlyTx: readOnlyTx, prepare: prepare,
		connect: func(conn *config.SQLConn, password, _ string) (string, error) {
			return conn.Host + "|" + password, nil
		}}
	placeholders["fake"] = func(int) string { return "?" }
	t.Cleanup(func() { delete(sqlDrivers, "fake"); delete(placeholders, "fake") })
	conn := networkConn("fake")
	conn.Host = host
	conn.Statements["one"] = statementFor(t, "select id from orders")
	decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "db"}
	tool, err := NewSQL(decl, SQLOptions{
		Conns:       map[string]*config.SQLConn{"db": conn},
		Credentials: map[string]string{"orders": "DB_PASSWORD"},
		Getenv: func(name string) string {
			if name == "DB_PASSWORD" {
				return password
			}
			return ""
		},
		Pool: NewSQLPool(),
	}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tool.Close() })
	return tool
}

func TestEveryStatementRunsInAReadOnlyTransactionWhenTheDriverHasThem(t *testing.T) {
	for name, c := range map[string]struct{ readOnlyTx, prepare bool }{
		"transaction":              {true, false},
		"transaction and prepared": {true, true},
		"prepared":                 {false, true},
		"plain":                    {false, false},
	} {
		fake.reset()
		tool := fakeTool(t, c.readOnlyTx, c.prepare, "pw", "h")
		got, err := tool.Call(context.Background(), "one", Args{})
		if err != nil {
			t.Fatalf("%s: %v", name, rendered(t, err))
		}
		if got.List[0].Record["id"].Number != 7 {
			t.Errorf("%s: got %s", name, got.Display())
		}
		begun, readOnly, prepared, queried := fake.counts()
		want := map[string]int{"transactions": 0, "prepared": 0, "queried": 1}
		if c.readOnlyTx {
			want["transactions"] = 1
		}
		if c.prepare {
			want["prepared"], want["queried"] = 1, 0
		}
		have := map[string]int{"transactions": begun, "prepared": prepared, "queried": queried}
		for what, n := range want {
			if have[what] != n {
				t.Errorf("%s: %s = %d, want %d", name, what, have[what], n)
			}
		}
		if c.readOnlyTx && !readOnly {
			t.Errorf("%s: the transaction was not read only", name)
		}
	}
}

// counts says how many transactions were begun, whether the first was read only, and how many statements
// were prepared and queried directly.
func (f *fakeDriver) counts() (begun int, readOnly bool, prepared, queried int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.begun), len(f.begun) > 0 && f.begun[0], len(f.prepared), len(f.queried)
}

func TestThePasswordGoesToTheConnectionAndNeverIntoAProblem(t *testing.T) {
	fake.reset()
	tool := fakeTool(t, true, false, "hunter2", "refuse-host")
	_, err := tool.Call(context.Background(), "one", Args{})
	if err == nil {
		t.Fatal("a connection that was refused worked")
	}
	shown := rendered(t, err)
	if !strings.Contains(shown, "I could not reach the database") || strings.Contains(shown, "hunter2") {
		t.Errorf("problem = %s", shown)
	}
	if _, ok := diag.RetryOf(err); !ok {
		t.Error("a connection that was refused is a failure that may pass")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.dsns) == 0 || fake.dsns[0] != "refuse-host|hunter2" {
		t.Errorf("dsns = %v", fake.dsns)
	}
}

func TestAMissingPasswordIsToldBeforeAnythingIsOpened(t *testing.T) {
	fake.reset()
	tool := fakeTool(t, true, false, "", "h")
	_, err := tool.Call(context.Background(), "one", Args{})
	if err == nil || !strings.Contains(rendered(t, err), "the variable DB_PASSWORD is empty") {
		t.Errorf("error = %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.dsns) != 0 {
		t.Errorf("a connection was opened: %v", fake.dsns)
	}
}

func TestAPathOfAFileStartsAtTheProjectUnlessItIsAbsolute(t *testing.T) {
	root := t.TempDir()
	if got, want := absolute(root, filepath.Join("certs", "ca.pem")), filepath.Join(root, "certs", "ca.pem"); got != want {
		t.Errorf("relative = %s, want %s", got, want)
	}
	abs := filepath.Join(t.TempDir(), "ca.pem")
	if got := absolute(root, abs); got != abs {
		t.Errorf("absolute = %s, want %s", got, abs)
	}
}

func TestAnErrorOfTheNetworkOrOfTheConnectionMayPassAndAnotherDoesNot(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want bool
	}{
		"refused":   {&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}, true},
		"reset":     {fmt.Errorf("read: %w", syscall.ECONNRESET), true},
		"eof":       {io.EOF, true},
		"bad conn":  {driver.ErrBadConn, true},
		"time":      {context.DeadlineExceeded, true},
		"a syntax":  {errors.New("syntax error at or near FROM"), false},
		"cancelled": {context.Canceled, false},
	} {
		if got := sqlMayPass(c.err); got != c.want {
			t.Errorf("%s: %v", name, got)
		}
	}
}

// fakeWriter is a tool on the fake network database whose connection may change rows.
func fakeWriter(t *testing.T, statements map[string]string) *SQL {
	t.Helper()
	sqlDrivers["fake"] = sqlDriver{name: "fakesql", readOnlyTx: true, prepare: true,
		connect: func(conn *config.SQLConn, password, _ string) (string, error) { return conn.Host + "|" + password, nil }}
	placeholders["fake"] = func(int) string { return "?" }
	t.Cleanup(func() { delete(sqlDrivers, "fake"); delete(placeholders, "fake") })
	conn := networkConn("fake")
	conn.Mode = config.ModeWrite
	for name, text := range statements {
		st := writing(t, text, "")
		st.Name = name
		conn.Statements[name] = st
	}
	decl := &lang.ToolDecl{Name: "dest", Kind: lang.ToolSQL, Command: "db"}
	tool, err := NewSQL(decl, SQLOptions{Conns: map[string]*config.SQLConn{"db": conn}, Getenv: func(string) string { return "" }, Pool: NewSQLPool()}, config.Default().Limits)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tool.Close() })
	return tool
}

func TestAStatementThatChangesRowsRunsInAWritableTransactionOfTheNetworkDriver(t *testing.T) {
	fake.reset()
	tool := fakeWriter(t, map[string]string{"purge": "delete from stg where job = :job"})
	got, err := tool.Call(context.Background(), "purge", Args{"job": value.Number(1)})
	if err != nil || got.Number != 3 {
		t.Fatalf("got %s, %v", got.Display(), rendered(t, err))
	}
	begun, readOnly, _, _ := fake.counts()
	if begun != 1 || readOnly {
		t.Errorf("transactions = %d, read only = %v", begun, readOnly)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.executed) != 1 {
		t.Errorf("executed = %v", fake.executed)
	}
}

func TestAFailureToChangeRowsThatMayPassIsMarkedSoAndShowsNoValue(t *testing.T) {
	fake.reset()
	tool := fakeWriter(t, map[string]string{"deadlock": "delete from stg where job = :job -- deadlock"})
	_, err := tool.Call(context.Background(), "deadlock", Args{"job": value.Number(1)})
	if err == nil {
		t.Fatal("the statement must fail")
	}
	if _, ok := diag.RetryOf(err); !ok {
		t.Error("a connection that dropped in the middle of a write is a failure that may pass")
	}
}

func writing(t *testing.T, text string, each string, columns ...string) *config.SQLStatement {
	t.Helper()
	parsed, err := sqlscan.ParseWrite(text)
	if err != nil {
		t.Fatal(err)
	}
	result := config.ResultRows
	if parsed.Kind.Writes() {
		result = config.ResultCount
	}
	return &config.SQLStatement{Result: result, Parsed: parsed, Each: each, Columns: columns}
}

var errPlain = errors.New("plain")

func TestADriverThatCannotAskForAReadOnlyTransactionIsToldFirstInsideOne(t *testing.T) {
	fake.reset()
	tool := fakeTool(t, false, false, "pw", "h")
	driver := sqlDrivers["fake"]
	driver.readOnlyStart = "SET TRANSACTION READ ONLY"
	sqlDrivers["fake"] = driver
	if _, err := tool.Call(context.Background(), "one", Args{}); err != nil {
		t.Fatal(rendered(t, err))
	}
	begun, readOnly, prepared, queried := fake.counts()
	if begun != 1 || readOnly || prepared != 1 || queried != 1 {
		t.Errorf("transactions %d (read only %v), prepared %d, queried %d", begun, readOnly, prepared, queried)
	}
	if fake.prepared[0] != "SET TRANSACTION READ ONLY" {
		t.Errorf("the first thing said was %q", fake.prepared[0])
	}
}

func TestTheRowsOfADriverThatHandsOverNumbersAsTextHaveNumbers(t *testing.T) {
	fake.reset()
	fake.textNumber = true
	t.Cleanup(func() { fake.textNumber = false })
	tool := fakeTool(t, true, false, "pw", "h")
	driver := sqlDrivers["fake"]
	driver.numberType = "NUMBER"
	sqlDrivers["fake"] = driver
	got, err := tool.Call(context.Background(), "one", Args{})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if id := got.List[0].Record["id"]; id.Kind != value.KindNumber || id.Number != 7 {
		t.Errorf("id = %s (%v)", id.Display(), id.Kind)
	}
}

func TestOnlySqlServerGetsASemicolonAfterAMerge(t *testing.T) {
	text := "merge into t using s on t.id = s.id when matched then update set a = :a"
	for driver, want := range map[string]string{"sqlserver": ";", "oracle": "", "postgres": ""} {
		conn := networkConn(driver)
		conn.Mode = config.ModeWrite
		st := writing(t, text, "")
		st.Name = "put"
		conn.Statements["put"] = st
		decl := &lang.ToolDecl{Name: "dest", Kind: lang.ToolSQL, Command: "db"}
		tool, err := NewSQL(decl, SQLOptions{Conns: map[string]*config.SQLConn{"db": conn}, Getenv: func(string) string { return "" }, Pool: NewSQLPool()}, config.Default().Limits)
		if err != nil {
			t.Fatal(driver, err)
		}
		if strings.HasSuffix(tool.stmts["put"].query, ";") != (want == ";") {
			t.Errorf("%s: query = %q", driver, tool.stmts["put"].query)
		}
		_ = tool.Close()
	}
}
