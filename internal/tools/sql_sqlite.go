//go:build !nosqlite

package tools

import (
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite" // the SQLite driver, written in Go: no C is needed to build or to run it
)

func init() {
	sqlDrivers["sqlite"] = sqlDriver{name: "sqlite", file: true, dsn: sqliteDSN}
}

// sqliteDSN opens the file to be read only, twice over: the file is opened read only, and the
// connection refuses to write. A connection that writes opens it to be changed; the file has to exist
// already, so it is never made here. The path is given as a file address, which also works with the drive
// letter of a path of Windows.
func sqliteDSN(path string, write bool) string {
	slash := filepath.ToSlash(path)
	if len(slash) == 0 || slash[0] != '/' {
		slash = "/" + slash
	}
	address := url.URL{Scheme: "file", Path: slash}
	if write {
		return address.String() + "?mode=rw&_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	}
	return address.String() + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
}
