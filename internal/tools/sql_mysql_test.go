//go:build !nomysql

package tools

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestTheCertificatesOfAServerAreRegisteredOnceAndUsed(t *testing.T) {
	dir := t.TempDir()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	pemText := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), pemText, 0o600); err != nil {
		t.Fatal(err)
	}
	conn := networkConn("mysql")
	conn.CAFile = "ca.pem"
	first, err := mysqlDSN(conn, "pw", dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := mysqlDSN(conn, "pw", dir)
	if err != nil || first != second || !strings.Contains(first, "tls=metagente-") {
		t.Errorf("first = %s, second = %s, err = %v", first, second, err)
	}
	conn.Host = "other.example.com"
	other, _ := mysqlDSN(conn, "pw", dir)
	if other == first {
		t.Error("the certificates of two hosts got the same name")
	}
}
