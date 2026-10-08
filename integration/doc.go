// Package integration holds the tests that need real services (PostgreSQL, MariaDB, MySQL), started in
// containers with Testcontainers. It is a module of its own so that the program does not take the
// dependencies of the tests. See section 16 of docs/design-async-elt.md.
package integration
