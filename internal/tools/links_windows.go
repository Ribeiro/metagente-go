//go:build windows

package tools

import (
	"os"
	"syscall"
)

// linkCount returns how many names a file has. More than one means a hard
// link, which a check on the path cannot see (requirement F4). On Windows the
// number is not in what Stat reads, so the file is opened inside the folder and
// the system is asked for it. A file that cannot be opened to be read counts
// as having one name, as before; the write that follows would fail on it too.
func linkCount(root *os.Root, name string, _ os.FileInfo) uint64 {
	f, err := root.Open(name)
	if err != nil {
		return 1
	}
	defer f.Close()
	var data syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(syscall.Handle(f.Fd()), &data); err != nil {
		return 1
	}
	return uint64(data.NumberOfLinks)
}
