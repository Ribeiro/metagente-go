//go:build !windows

package mcp

// EndChildrenWithProcess does nothing here: on Linux and macOS a tool server leads a group of
// processes of its own, which the pool ends with it (see group_unix.go).
func EndChildrenWithProcess() error { return nil }
