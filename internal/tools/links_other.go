//go:build !unix && !windows

package tools

import "os"

// linkCount cannot be read on this system, so no file counts as hard linked.
func linkCount(_ *os.Root, _ string, _ os.FileInfo) uint64 {
	return 1
}
