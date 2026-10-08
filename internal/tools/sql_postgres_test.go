//go:build !nopostgres

package tools

import (
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
