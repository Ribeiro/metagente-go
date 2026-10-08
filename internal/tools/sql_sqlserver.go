//go:build !nosqlserver

package tools

import (
	"errors"
	"net"
	"net/url"
	"strconv"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/Ribeiro/metagente-go/internal/config"
)

func init() {
	sqlDrivers["sqlserver"] = sqlDriver{name: "sqlserver", connect: sqlserverDSN, transient: sqlserverTransient}
}

// sqlserverTransient is an error that may pass: a server that is not up, a login that cannot be had now
// (4060 and 18456 are not here: they are a wrong database or a wrong password), a deadlock (1205), a lock
// that waited too long (1222), a database that is not ready (922, 927, 6005 and 6006 for a shutdown) or a
// connection that is gone.
func sqlserverTransient(err error) bool {
	var ms mssql.Error
	if errors.As(err, &ms) {
		switch ms.Number {
		case 1205, 1222, 921, 922, 927, 6005, 6006, 40501, 40613, 49918, 49919, 49920:
			return true
		}
		return false
	}
	return false
}

// sqlserverDSN builds the address of the database. SQL Server has no transaction that the database knows
// to be read only, so a connection that only reads says so in the intent of the session (which a server
// with readable copies uses to send it to one); what really stops a write is that a statement is a SELECT,
// that a function of SQL Server cannot change rows, and that the user of the database can only read.
func sqlserverDSN(conn *config.SQLConn, password, root string) (string, error) {
	query := url.Values{}
	query.Set("database", conn.Database)
	query.Set("app name", "metagente")
	query.Set("dial timeout", "10")
	query.Set("connection timeout", "10")
	switch conn.TLS {
	case config.TLSDisable:
		query.Set("encrypt", "disable")
	case config.TLSRequire:
		query.Set("encrypt", "true")
		query.Set("TrustServerCertificate", "true")
	case config.TLSVerify:
		query.Set("encrypt", "true")
		query.Set("TrustServerCertificate", "false")
		if conn.CAFile != "" {
			query.Set("certificate", absolute(root, conn.CAFile))
		}
	}
	if !conn.Writes() {
		query.Set("ApplicationIntent", "ReadOnly")
	}
	address := url.URL{
		Scheme:   "sqlserver",
		Host:     net.JoinHostPort(conn.Host, strconv.Itoa(conn.Port)),
		RawQuery: query.Encode(),
	}
	if password != "" {
		address.User = url.UserPassword(conn.User, password)
	} else {
		address.User = url.User(conn.User)
	}
	return address.String(), nil
}
