//go:build windows

package serve

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestAFileCaughtInTheMiddleOfAChangeIsNotAProblem(t *testing.T) {
	sharing := &os.PathError{Op: "open", Path: "tokens", Err: errorSharingViolation}
	for name, err := range map[string]error{
		"used by another process":   sharing,
		"to be deleted":             &os.PathError{Op: "open", Path: "tokens", Err: errorDeletePending},
		"as ReadTokenFile wraps it": fmt.Errorf("I could not open the token file tokens: %w", sharing.Err),
	} {
		if !busyFile(err) {
			t.Errorf("%s: taken for a problem", name)
		}
	}
	for name, err := range map[string]error{
		"no such file":  &os.PathError{Op: "open", Path: "tokens", Err: syscall.ERROR_FILE_NOT_FOUND},
		"access denied": &os.PathError{Op: "open", Path: "tokens", Err: syscall.ERROR_ACCESS_DENIED},
		"none":          nil,
	} {
		if busyFile(err) {
			t.Errorf("%s: taken for a busy file", name)
		}
	}
}
