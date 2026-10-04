//go:build !unix

package tools

import "os"

// linkCount cannot be read on this system, so no file counts as hard linked.
func linkCount(info os.FileInfo) uint64 {
	return 1
}
