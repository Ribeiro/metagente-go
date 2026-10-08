package tools

import (
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/sqlscan"
)

func statementFor(t *testing.T, text string) *config.SQLStatement {
	t.Helper()
	parsed, err := sqlscan.Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	return &config.SQLStatement{Name: "one", Result: config.ResultRows, Parsed: parsed}
}

func networkConn(driver string) *config.SQLConn {
	return &config.SQLConn{Name: "db", Driver: driver, Host: "db.example.com", Port: 5432, Database: "orders",
		User: "ana@corp", TLS: config.TLSVerify, Statements: map[string]*config.SQLStatement{}}
}

func TestTheSpecOfANetworkDatabaseSaysWhereItIsAndChangesWithIt(t *testing.T) {
	statement := func(text string) *config.SQLStatement { return statementFor(t, text) }
	build := func(mutate func(*config.SQLConn)) SQLSpec {
		conn := networkConn("postgres")
		conn.Statements["one"] = statement("select 1")
		mutate(conn)
		decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "db"}
		spec, err := SQLSpecOf(decl, map[string]*config.SQLConn{"db": conn}, map[string]string{"orders": "DB_PASSWORD"})
		if err != nil {
			t.Fatal(err)
		}
		return spec
	}
	base := build(func(*config.SQLConn) {})
	if base.Target != "ana@corp@db.example.com:5432/orders (tls verify)" || base.Credential != "DB_PASSWORD" {
		t.Errorf("spec = %+v", base)
	}
	for name, mutate := range map[string]func(*config.SQLConn){
		"host":     func(c *config.SQLConn) { c.Host = "other.example.com" },
		"port":     func(c *config.SQLConn) { c.Port = 5433 },
		"database": func(c *config.SQLConn) { c.Database = "other" },
		"user":     func(c *config.SQLConn) { c.User = "bob" },
		"tls":      func(c *config.SQLConn) { c.TLS = config.TLSDisable },
		"ca":       func(c *config.SQLConn) { c.CAFile = "ca.pem" },
	} {
		if build(mutate).Fingerprint == base.Fingerprint {
			t.Errorf("changing the %s did not change what is approved", name)
		}
	}
}

func TestPlacesAreWrittenTheWayEachDatabaseWantsThem(t *testing.T) {
	if got := placeholders["postgres"](3); got != "$3" {
		t.Errorf("postgres = %q", got)
	}
	for _, driver := range []string{"sqlite", "mysql", "mariadb"} {
		if got := placeholders[driver](3); got != "?" {
			t.Errorf("%s = %q", driver, got)
		}
	}
	for driver, want := range map[string]string{"sqlite": "nosqlite", "postgres": "nopostgres", "mysql": "nomysql", "mariadb": "nomysql"} {
		if got := omitTag(driver); got != want {
			t.Errorf("tag of %s = %s", driver, got)
		}
	}
}

func TestADriverLeftOutOfTheBuildIsSaidWithItsTag(t *testing.T) {
	for _, driver := range []string{"postgres", "mariadb"} {
		saved, had := sqlDrivers[driver]
		delete(sqlDrivers, driver)
		conn := networkConn(driver)
		conn.Statements["one"] = statementFor(t, "select 1")
		decl := &lang.ToolDecl{Name: "orders", Kind: lang.ToolSQL, Command: "db"}
		tool, err := NewSQL(decl, SQLOptions{Conns: map[string]*config.SQLConn{"db": conn}, Getenv: func(string) string { return "" }}, config.Default().Limits)
		if err != nil {
			t.Fatal(err)
		}
		_, err = tool.Call(t.Context(), "one", Args{})
		if had {
			sqlDrivers[driver] = saved
		}
		_ = tool.Close()
		want := "tag " + omitTag(driver)
		if err == nil || !strings.Contains(rendered(t, err), want) {
			t.Errorf("%s: %v", driver, err)
		}
	}
}
