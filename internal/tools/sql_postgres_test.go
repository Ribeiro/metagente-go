//go:build !nopostgres

package tools

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
)

func TestPostgresAddressKeepsSpecialSignsOfThePasswordInPlace(t *testing.T) {
	conn := networkConn("postgres")
	dsn, err := postgresDSN(conn, "p@ss/w:rd#1", "/project")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Host != "db.example.com:5432" || u.User.Username() != "ana@corp" || password != "p@ss/w:rd#1" || u.Path != "/orders" {
		t.Errorf("dsn = %s", dsn)
	}
	q := u.Query()
	if q.Get("sslmode") != "verify-full" || q.Get("default_transaction_read_only") != "on" {
		t.Errorf("query = %v", q)
	}
	for mode, want := range map[string]string{config.TLSRequire: "require", config.TLSDisable: "disable"} {
		conn.TLS = mode
		dsn, _ = postgresDSN(conn, "", "/project")
		if !strings.Contains(dsn, "sslmode="+want) || strings.Contains(dsn, "sslrootcert") {
			t.Errorf("%s: %s", mode, dsn)
		}
	}
	conn.TLS, conn.CAFile = config.TLSVerify, "certs/ca.pem"
	dsn, _ = postgresDSN(conn, "", "/project")
	if want := url.QueryEscape(filepath.Join("/project", "certs", "ca.pem")); !strings.Contains(dsn, "sslrootcert="+want) {
		t.Errorf("the certificates are not where the project is: %s", dsn)
	}
	dsn, _ = postgresDSN(conn, "", "/project")
	if !strings.HasPrefix(dsn, "postgres://ana%40corp@db.example.com:5432/orders?") {
		t.Errorf("without a password: %s", dsn)
	}
}

func TestPostgresErrorsThatMayPassAreTold(t *testing.T) {
	for code, want := range map[string]bool{
		"08006": true, "08001": true, "53300": true, "57P01": true, "57P03": true, "40001": true, "40P01": true,
		"23505": false, "42601": false, "42P01": false, "25006": false,
	} {
		if got := postgresTransient(&pgconn.PgError{Code: code}); got != want {
			t.Errorf("%s: %v", code, got)
		}
	}
	if postgresTransient(errors.New("plain")) {
		t.Error("a plain error was taken for one that may pass")
	}
}

// A database that is closed to connections for a while says 55000, which also means errors that never pass.
func TestPostgresTakesADatabaseClosedToConnectionsForAnErrorThatMayPass(t *testing.T) {
	closed := &pgconn.PgError{Code: "55000", Message: `database "orders" is not currently accepting connections`}
	if !postgresTransient(closed) {
		t.Error("a database closed to connections was taken for a final error")
	}
	if !postgresTransient(fmt.Errorf("failed to connect: %w", closed)) {
		t.Error("the same error, wrapped by the driver, was taken for a final error")
	}
	other := &pgconn.PgError{Code: "55000", Message: `cannot execute nextval() in a read-only transaction`}
	if postgresTransient(other) {
		t.Error("another error of the same code was taken for one that may pass")
	}
}

func TestPostgresOnlyOpensASessionThatChangesRowsWhenTheConnectionWrites(t *testing.T) {
	conn := networkConn("postgres")
	conn.Mode = config.ModeWrite
	dsn, err := postgresDSN(conn, "", "/project")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(dsn, "default_transaction_read_only") {
		t.Errorf("a connection that writes was told to be read only: %s", dsn)
	}
	conn.Mode = config.ModeRead
	dsn, _ = postgresDSN(conn, "", "/project")
	if !strings.Contains(dsn, "default_transaction_read_only=on") {
		t.Errorf("a connection that reads must be told to be read only: %s", dsn)
	}
}
