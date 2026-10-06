//go:build windows

package serve

import (
	"errors"
	"syscall"
)

// On Windows a file that another program has open, or that is being removed while someone still
// holds it, cannot be opened for a moment: the system answers that it is used by another
// process, or that it is to be deleted. An antivirus that looks at a file just written does the
// same. It says nothing about the file, only that it was caught in the middle of a change.
const (
	errorSharingViolation = syscall.Errno(32)
	errorDeletePending    = syscall.Errno(303)
)

// busyFile is true for an error that only means the file is busy for a moment.
func busyFile(err error) bool {
	return errors.Is(err, errorSharingViolation) || errors.Is(err, errorDeletePending)
}
