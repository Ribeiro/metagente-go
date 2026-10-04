//go:build unix

package mcp

import (
	"errors"
	"syscall"
	"time"
)

// groupAttr makes the program the leader of a group of processes of its own. Its number
// is then the number of the group, and everything it starts belongs to the group, however
// far down: a tool server that is started by `npx` is a program that starts the program.
func groupAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// endGroup ends what is left of the group that a program led, once the program itself is
// gone: it asks, waits a little for the processes to end by themselves, and then forces
// them. When nobody is left, which is the usual case, it returns at once.
func endGroup(pid int, grace time.Duration) {
	if pid <= 0 {
		return
	}
	if err := syscall.Kill(-pid, syscall.SIGTERM); errors.Is(err, syscall.ESRCH) {
		return // nobody is left
	}
	deadline := time.Now().Add(grace)
	for time.Now().Before(deadline) {
		if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
