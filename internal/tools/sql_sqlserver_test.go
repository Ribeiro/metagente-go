//go:build !nosqlserver

package tools

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/Ribeiro/metagente-go/internal/config"
)

func TestSQLServerAddressKeepsSpecialSignsOfThePasswordInPlace(t *testing.T) {
	conn := networkConn("sqlserver")
	conn.Port = 1433
	dsn, err := sqlserverDSN(conn, "p@ss/w:rd#1", "/project")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	password, _ := u.User.Password()
	if u.Scheme != "sqlserver" || u.Host != "db.example.com:1433" || u.User.Username() != "ana@corp" || password != "p@ss/w:rd#1" {
		t.Errorf("dsn = %s", dsn)
	}
	q := u.Query()
	if q.Get("database") != "orders" || q.Get("encrypt") != "true" || q.Get("TrustServerCertificate") != "false" || q.Get("ApplicationIntent") != "ReadOnly" {
		t.Errorf("query = %v", q)
	}
}

func TestSQLServerAddressFollowsTheTLSModeAndTheMode(t *testing.T) {
	conn := networkConn("sqlserver")
	for mode, want := range map[string][2]string{
		config.TLSRequire: {"true", "true"}, config.TLSDisable: {"disable", ""}, config.TLSVerify: {"true", "false"},
	} {
		conn.TLS = mode
		dsn, _ := sqlserverDSN(conn, "", "/project")
		q := mustQuery(t, dsn)
		if q.Get("encrypt") != want[0] || q.Get("TrustServerCertificate") != want[1] || q.Get("certificate") != "" {
			t.Errorf("%s: %v", mode, q)
		}
	}
	conn.TLS, conn.CAFile = config.TLSVerify, "certs/ca.pem"
	if got := mustQuery(t, mustDSN(t, conn)).Get("certificate"); got != filepath.Join("/project", "certs", "ca.pem") {
		t.Errorf("the certificates are not where the project is: %s", got)
	}
	conn.Mode = config.ModeWrite
	if dsn := mustDSN(t, conn); strings.Contains(dsn, "ApplicationIntent") {
		t.Errorf("a connection that writes was told to be read only: %s", dsn)
	}
	if dsn := mustDSN(t, conn); !strings.HasPrefix(dsn, "sqlserver://ana%40corp@db.example.com:5432?") {
		t.Errorf("without a password: %s", dsn)
	}
}

func mustDSN(t *testing.T, conn *config.SQLConn) string {
	t.Helper()
	dsn, err := sqlserverDSN(conn, "", "/project")
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func TestSQLServerErrorsThatMayPassAreTold(t *testing.T) {
	for number, want := range map[int32]bool{1205: true, 1222: true, 922: true, 40613: true, 2627: false, 102: false, 208: false, 18456: false} {
		if got := sqlserverTransient(mssql.Error{Number: number}); got != want {
			t.Errorf("%d: %v", number, got)
		}
	}
	if sqlserverTransient(errPlain) {
		t.Error("a plain error was taken for one that may pass")
	}
}
