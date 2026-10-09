//go:build !nopostgres

package tools

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // the PostgreSQL driver, written in Go

	"github.com/Ribeiro/metagente-go/internal/config"
)

func init() {
	sqlDrivers["postgres"] = sqlDriver{name: "pgx", connect: postgresDSN, readOnlyTx: true, transient: postgresTransient}
}

// postgresTransient is an error that may pass: a connection exception (class 08), a server that shuts down or
// starts (57P01 to 57P03), too many connections or no resources (class 53), a serialization failure or a
// deadlock (40001, 40P01), and a database that was closed to connections for a while, as for maintenance: it answers
// with 55000, a code that also means other things, so its words count too.
func postgresTransient(err error) bool {
	var pg *pgconn.PgError
	if !errors.As(err, &pg) {
		return false
	}
	switch {
	case strings.HasPrefix(pg.Code, "08"), strings.HasPrefix(pg.Code, "53"):
		return true
	}
	switch pg.Code {
	case "57P01", "57P02", "57P03", "40001", "40P01":
		return true
	case "55000":
		return strings.Contains(pg.Message, "is not currently accepting connections")
	}
	return false
}

// postgresDSN builds the address of the database. The session is told to be read only before anything
// runs (unless the connection writes), and each statement that reads runs in a read only transaction.
func postgresDSN(conn *config.SQLConn, password, root string) (string, error) {
	mode := map[string]string{config.TLSVerify: "verify-full", config.TLSRequire: "require", config.TLSDisable: "disable"}[conn.TLS]
	query := url.Values{}
	query.Set("sslmode", mode)
	if !conn.Writes() {
		query.Set("default_transaction_read_only", "on")
	}
	query.Set("application_name", "metagente")
	query.Set("connect_timeout", "10")
	if conn.CAFile != "" {
		query.Set("sslrootcert", absolute(root, conn.CAFile))
	}
	address := url.URL{
		Scheme:   "postgres",
		Host:     net.JoinHostPort(conn.Host, strconv.Itoa(conn.Port)),
		Path:     "/" + conn.Database,
		RawQuery: query.Encode(),
	}
	if password != "" {
		address.User = url.UserPassword(conn.User, password)
	} else {
		address.User = url.User(conn.User)
	}
	return address.String(), nil
}
