//go:build nosqlserver

package tools

// A build made with -tags nosqlserver has no SQL Server driver; a connection that names it says so when
// the database is first used. The releases are not built with this tag.
