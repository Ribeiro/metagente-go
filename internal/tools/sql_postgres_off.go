//go:build nopostgres

package tools

// A build made with -tags nopostgres has no PostgreSQL driver; a connection that names it says so when
// the database is first used. The releases are not built with this tag.
