//go:build nosqlite

package tools

// A build made with -tags nosqlite has no SQLite driver, and the program is smaller by what the driver
// weighs. `tool x from sql` still reads, and the connection says that the driver is not in the build when
// the database is first used. The releases are not built with this tag.
