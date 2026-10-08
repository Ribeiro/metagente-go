//go:build !nomysql

package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
)

func TestMySQLAddressSaysHowToTalkToTheServer(t *testing.T) {
	conn := networkConn("mysql")
	conn.Port = 3306
	for mode, want := range map[string]string{config.TLSVerify: "tls=true", config.TLSRequire: "tls=skip-verify", config.TLSDisable: "tls=false"} {
		conn.TLS = mode
		dsn, err := mysqlDSN(conn, "pw", "/project")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(dsn, want) || !strings.Contains(dsn, "parseTime=true") || !strings.Contains(dsn, "@tcp(db.example.com:3306)/orders") {
			t.Errorf("%s: %s", mode, dsn)
		}
	}
}

func TestTheCertificatesOfAMySQLServerMustBeThere(t *testing.T) {
	conn := networkConn("mariadb")
	conn.CAFile = "missing.pem"
	if _, err := mysqlDSN(conn, "", t.TempDir()); err == nil || !strings.Contains(err.Error(), "could not read the certificates") {
		t.Errorf("a missing file: %v", err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	conn.CAFile = "ca.pem"
	if _, err := mysqlDSN(conn, "", dir); err == nil || !strings.Contains(err.Error(), "no certificate in PEM form") {
		t.Errorf("a file that is not PEM: %v", err)
	}
}
