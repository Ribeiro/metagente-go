//go:build nomysql

package tools

// A build made with -tags nomysql has no MySQL and MariaDB driver; a connection that names one says so
// when the database is first used. The releases are not built with this tag.
