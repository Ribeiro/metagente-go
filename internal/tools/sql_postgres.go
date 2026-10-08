//go:build !nopostgres

package tools

import (
	"net"
	"net/url"
	"strconv"

	_ "github.com/jackc/pgx/v5/stdlib" // the PostgreSQL driver, written in Go

	"github.com/Ribeiro/metagente-go/internal/config"
)

func init() {
	sqlDrivers["postgres"] = sqlDriver{name: "pgx", connect: postgresDSN, readOnlyTx: true}
}

// postgresDSN builds the address of the database. The session is told to be read only before anything
// runs, and each statement runs in a read only transaction as well.
func postgresDSN(conn *config.SQLConn, password, root string) (string, error) {
	mode := map[string]string{config.TLSVerify: "verify-full", config.TLSRequire: "require", config.TLSDisable: "disable"}[conn.TLS]
	query := url.Values{}
	query.Set("sslmode", mode)
	query.Set("default_transaction_read_only", "on")
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
