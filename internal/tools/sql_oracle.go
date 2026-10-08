//go:build !nooracle

package tools

import (
	"errors"
	"strconv"

	go_ora "github.com/sijms/go-ora/v2"
	"github.com/sijms/go-ora/v2/network"

	"github.com/Ribeiro/metagente-go/internal/config"
)

func init() {
	sqlDrivers["oracle"] = sqlDriver{
		name: "oracle", connect: oracleDSN, transient: oracleTransient,
		// The driver cannot ask for a transaction that only reads, so the statement that does it runs first.
		readOnlyStart: "SET TRANSACTION READ ONLY",
	}
}

// oracleTransient is an error that may pass: a listener that does not know the service yet (12514, 12528,
// 12541), a shutdown or a start (1033, 1034, 3113, 3114), too many sessions (18, 20, 12516, 12519, 12520), or
// a deadlock or a lock that waited too long (60, 30006), or a table that was changed a moment before a transaction
// that only reads began (1466).
func oracleTransient(err error) bool {
	var ora *network.OracleError
	if !errors.As(err, &ora) {
		return false
	}
	switch ora.ErrCode {
	case 18, 20, 60, 1466, 1033, 1034, 3113, 3114, 12514, 12516, 12519, 12520, 12528, 12541, 30006:
		return true
	}
	return false
}

// oracleDSN builds the address of the database; the "database" of the connection is the name of the
// service. With tls other than "disable" the session is over TLS, which is the port of the listener that
// speaks it. The certificates to trust are the ones of the computer.
func oracleDSN(conn *config.SQLConn, password, _ string) (string, error) {
	options := map[string]string{"CONNECT TIMEOUT": "10"}
	if conn.TLS != config.TLSDisable {
		options["SSL"] = "true"
		options["SSL VERIFY"] = strconv.FormatBool(conn.TLS == config.TLSVerify)
	}
	return go_ora.BuildUrl(conn.Host, conn.Port, conn.Database, conn.User, password, options), nil
}
