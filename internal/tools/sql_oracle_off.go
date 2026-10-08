//go:build nooracle

package tools

// A build made with -tags nooracle has no Oracle driver; a connection that names it says so when the
// database is first used. The releases are not built with this tag.
