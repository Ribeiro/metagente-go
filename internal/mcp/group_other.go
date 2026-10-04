//go:build !unix

package mcp

import (
	"syscall"
	"time"
)

// The group of processes is not used here yet: ending the children of a program on Windows
// needs a job object, and that is to be tried before it is promised.
func groupAttr() *syscall.SysProcAttr { return nil }

func endGroup(int, time.Duration) {}
