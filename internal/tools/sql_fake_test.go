package tools

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
)

// fakeDriver is a database that keeps a note of how it was used: what the tests of the network drivers
// need to know without a server (the integration tests use the real ones).
type fakeDriver struct {
	mu       sync.Mutex
	dsns     []string
	begun    []bool // read only, for each transaction
	prepared []string
	queried  []string
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
	f.dsns, f.begun, f.prepared, f.queried = nil, nil, nil, nil
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
	return nil, io.ErrUnexpectedEOF
}
func (s *fakeStmt) Query([]driver.Value) (driver.Rows, error) { return &fakeRows{}, nil }

type fakeRows struct{ done bool }

func (r *fakeRows) Columns() []string { return []string{"id"} }
func (r *fakeRows) Close() error      { return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.done {
		return io.EOF
	}
	r.done = true
	dest[0] = int64(7)
	return nil
}

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
		fake.mu.Lock()
		begun, prepared, queried := len(fake.begun), len(fake.prepared), len(fake.queried)
		readOnly := len(fake.begun) == 1 && fake.begun[0]
		fake.mu.Unlock()
		if c.readOnlyTx && (!readOnly || begun != 1) {
			t.Errorf("%s: transactions = %d, read only = %v", name, begun, readOnly)
		}
		if !c.readOnlyTx && begun != 0 {
			t.Errorf("%s: a transaction was begun", name)
		}
		if c.prepare && prepared != 1 || !c.prepare && prepared != 0 {
			t.Errorf("%s: prepared = %d", name, prepared)
		}
		if !c.prepare && queried != 1 {
			t.Errorf("%s: queried = %d", name, queried)
		}
	}
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
