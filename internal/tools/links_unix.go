//go:build unix

package tools

import (
	"os"
	"syscall"
)

// linkCount returns how many names a file has. More than one means a hard
// link, which a check on the path cannot see (requirement F4).
func linkCount(info os.FileInfo) uint64 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Nlink)
	}
	return 1
}
