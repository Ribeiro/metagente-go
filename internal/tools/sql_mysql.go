//go:build !nomysql

package tools

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/Ribeiro/metagente-go/internal/config"
)

func init() {
	driver := sqlDriver{name: "mysql", connect: mysqlDSN, readOnlyTx: true, prepare: true}
	sqlDrivers["mysql"] = driver
	sqlDrivers["mariadb"] = driver
}

var registered sync.Map

// mysqlDSN builds the address of the database. Each statement is prepared, which is how the driver gives
// numbers as numbers, and runs in a transaction that is read only.
func mysqlDSN(conn *config.SQLConn, password, root string) (string, error) {
	c := mysql.NewConfig()
	c.User = conn.User
	c.Passwd = password
	c.Net = "tcp"
	c.Addr = net.JoinHostPort(conn.Host, strconv.Itoa(conn.Port))
	c.DBName = conn.Database
	c.ParseTime = true
	c.Loc = time.UTC
	c.Timeout = 10 * time.Second
	switch conn.TLS {
	case config.TLSDisable:
		c.TLSConfig = "false"
	case config.TLSRequire:
		c.TLSConfig = "skip-verify"
	case config.TLSVerify:
		c.TLSConfig = "true"
		if conn.CAFile != "" {
			name, err := registerCA(absolute(root, conn.CAFile), conn.Host)
			if err != nil {
				return "", err
			}
			c.TLSConfig = name
		}
	}
	return c.FormatDSN(), nil
}

// registerCA makes the driver know the certificates to trust for one host, and returns the name it has.
func registerCA(path, host string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("I could not read the certificates in ca_file: %w", err)
	}
	sum := sha256.Sum256(append([]byte(host+"\x00"), data...))
	name := "metagente-" + hex.EncodeToString(sum[:6])
	if _, done := registered.LoadOrStore(name, true); done {
		return name, nil
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		registered.Delete(name)
		return "", fmt.Errorf("ca_file has no certificate in PEM form: %s", path)
	}
	if err := mysql.RegisterTLSConfig(name, &tls.Config{RootCAs: pool, ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
		registered.Delete(name)
		return "", err
	}
	return name, nil
}
