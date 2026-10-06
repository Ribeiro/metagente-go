//go:build !unix

package mcp

import (
	"syscall"
	"time"
)

// There is no group of processes here. On Windows the children of a program are ended through
// the job object that the whole process joins when it starts (job_windows.go), when Metagente
// ends; a program alone is ended by the SDK.
func groupAttr() *syscall.SysProcAttr { return nil }

func endGroup(int, time.Duration) {
	// Nothing to end here: see groupAttr.
}
