//go:build unix

package mcp

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func processAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitForNumber(t *testing.T, file string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(file); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return pid
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("the tool server never said the number of its child")
	return 0
}

func waitUntilGone(pid int) bool {
	deadline := time.Now().Add(5 * time.Second)
	for processAlive(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	return !processAlive(pid)
}

// req: E4
func TestClosingThePoolEndsWhatTheToolServerStartedToo(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	pool, spec := newPool(t, nil)
	spec.Command += ` --child "` + pidFile + `"`
	srv := pool.Tool("srv", spec)
	mustWork(t, srv, "echo", "text", "start")

	child := waitForNumber(t, pidFile)
	if !processAlive(child) {
		t.Fatal("the child of the tool server is not running before the pool is closed")
	}
	if err := pool.Close(); err != nil {
		t.Fatal(err)
	}
	if !waitUntilGone(child) {
		_ = syscall.Kill(child, syscall.SIGKILL)
		t.Error("the child of the tool server was still running after the pool was closed")
	}
}

// req: E4
func TestAToolServerThatDiesDoesNotLeaveItsChildBehind(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	pool, spec := newPool(t, nil)
	spec.Command += ` --child "` + pidFile + `"`
	srv := pool.Tool("srv", spec)
	mustWork(t, srv, "echo", "text", "start")
	child := waitForNumber(t, pidFile)

	_, err := call(t, srv, "die") // the program ends in the middle of the call
	if err == nil {
		t.Fatal("a call that kills the server was answered")
	}
	if !waitUntilGone(child) {
		_ = syscall.Kill(child, syscall.SIGKILL)
		t.Error("the child of a tool server that died was still running")
	}
}
