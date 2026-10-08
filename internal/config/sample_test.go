package config

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The Worker and the sweeper of samples/async-elt call the statements of the destination by name, so each database
// has to give the same names, the same values, the same kind of answer and the same transactions. Only the SQL changes.
func TestTheDestinationsOfTheSampleOfEveryDatabaseOfferTheSameStatements(t *testing.T) {
	want := loadSample(t, "metagente.postgres.toml")
	for name, driver := range map[string]string{"metagente.sqlserver.toml": "sqlserver", "metagente.oracle.toml": "oracle"} {
		got := loadSample(t, name)
		if got.Driver != driver {
			t.Errorf("%s: driver %q", name, got.Driver)
		}
		// A dialect with no upsert has the statement that updates what is there, besides the one that inserts what is not.
		if missing, extra := minus(want.Names(), got.Names()), minus(got.Names(), want.Names()); missing != "" || extra != "update_orders" {
			t.Errorf("%s: the statements differ\nmissing: %v\nextra: %v", name, missing, extra)
		}
		sameStatements(t, name, want, got)
		sameTransactions(t, name, want, got)
	}
}

func loadSample(t *testing.T, name string) *SQLConn {
	t.Helper()
	cfg, err := Load(t.TempDir(), filepath.Join("..", "..", "samples", "async-elt", name))
	if err != nil {
		t.Fatalf("%s: %s", name, problemText(t, err))
	}
	conn := cfg.SQL["dest"]
	if conn == nil || !conn.Writes() {
		t.Fatalf("%s has no connection [sql.dest] that writes", name)
	}
	return conn
}

// sameStatements checks, for the statements that both have, the values they take and the shape of their answer.
func sameStatements(t *testing.T, name string, want, got *SQLConn) {
	t.Helper()
	for _, statement := range want.Names() {
		a, b := want.Statements[statement], got.Statements[statement]
		if b == nil {
			continue
		}
		if !reflect.DeepEqual(sorted(a.Parsed.Params), sorted(b.Parsed.Params)) {
			t.Errorf("%s: %s takes %v, and %v in PostgreSQL", name, statement, b.Parsed.Params, a.Parsed.Params)
		}
		if a.Result != b.Result || a.Each != b.Each || !reflect.DeepEqual(a.Columns, b.Columns) || a.Parsed.Kind != b.Parsed.Kind {
			t.Errorf("%s: %s has another shape than in PostgreSQL", name, statement)
		}
	}
}

// sameTransactions checks that every transaction is there and takes the same values. It may have more steps: an upsert
// is two statements.
func sameTransactions(t *testing.T, name string, want, got *SQLConn) {
	t.Helper()
	for _, tx := range want.TransactionNames() {
		if got.Transactions[tx] == nil {
			t.Errorf("%s: no transaction %s", name, tx)
			continue
		}
		wantScalars, wantLists, _ := want.TransactionParams(want.Transactions[tx])
		scalars, lists, err := got.TransactionParams(got.Transactions[tx])
		if err != nil {
			t.Errorf("%s: %s: %v", name, tx, err)
		}
		if !reflect.DeepEqual(sorted(scalars), sorted(wantScalars)) || !reflect.DeepEqual(sorted(lists), sorted(wantLists)) {
			t.Errorf("%s: %s takes %v and %v, and %v and %v in PostgreSQL", name, tx, scalars, lists, wantScalars, wantLists)
		}
	}
}

func sorted(list []string) []string {
	out := append([]string{}, list...)
	sort.Strings(out)
	return out
}

func minus(a, b []string) string {
	in := map[string]bool{}
	for _, s := range b {
		in[s] = true
	}
	var out []string
	for _, s := range a {
		if !in[s] {
			out = append(out, s)
		}
	}
	return strings.Join(out, ", ")
}

// The Extractor calls `page` and `range` of its source by name. Each source of the sample, put in place of the
// SQLite one, has to give the same two statements with the same values.
func TestTheSourceOfTheSampleOfEveryDatabaseOffersThePageAndTheRange(t *testing.T) {
	dir := filepath.Join("..", "..", "samples", "async-elt")
	base := readSample(t, filepath.Join(dir, "metagente.toml"))
	want := sourceOf(t, base)
	for _, name := range []string{"postgres", "sqlserver", "oracle"} {
		fragment := readSample(t, filepath.Join(dir, "sources", "source."+name+".toml"))
		start, end := strings.Index(base, "[sql.source]"), strings.Index(base, "# ---- the outbox")
		text := base[:start] + fragment + "\n" + base[end:] + "\n[credentials]\nsource = \"SOURCE_DB_PASSWORD\"\n"
		got := sourceOf(t, text)
		if got.Driver != name || got.Writes() {
			t.Errorf("%s: driver %q, writes %v", name, got.Driver, got.Writes())
		}
		for _, statement := range []string{"page", "range"} {
			a, b := want.Statements[statement], got.Statements[statement]
			if b == nil {
				t.Errorf("%s: no statement %s", name, statement)
				continue
			}
			if !reflect.DeepEqual(sorted(a.Parsed.Params), sorted(b.Parsed.Params)) || a.Result != b.Result {
				t.Errorf("%s: %s takes %v, and %v in SQLite", name, statement, b.Parsed.Params, a.Parsed.Params)
			}
		}
		if len(got.Names()) != 2 {
			t.Errorf("%s: statements %v, and the Extractor calls two", name, got.Names())
		}
	}
}

func readSample(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func sourceOf(t *testing.T, text string) *SQLConn {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "metagente.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(dir, path)
	if err != nil {
		t.Fatal(problemText(t, err))
	}
	return cfg.SQL["source"]
}
