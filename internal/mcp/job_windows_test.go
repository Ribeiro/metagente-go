//go:build windows

package mcp

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// The test starts this same test binary as a parent, which starts a child, which starts a
// grandchild, the way Metagente starts `npx` and `npx` starts the server. The parent leaves
// without ending anyone, and the test looks at whether the grandchild is still there.
const jobRole = "METAGENTE_JOB_TEST_ROLE"

func TestJobHelper(t *testing.T) {
	switch os.Getenv(jobRole) {
	case "parent", "parent-without-job":
		if os.Getenv(jobRole) == "parent" {
			if err := EndChildrenWithProcess(); err != nil {
				fmt.Println("error", err)
				os.Exit(1)
			}
		}
		lines, err := startRole("child")
		if err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		for _, line := range lines {
			fmt.Println(line)
		}
		os.Exit(0) // without waiting for the child or ending it
	case "child":
		lines, err := startRole("grandchild")
		if err != nil {
			fmt.Println("error", err)
			os.Exit(1)
		}
		fmt.Println("child", os.Getpid())
		fmt.Println(lines[0])
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	case "grandchild":
		fmt.Println("grandchild", os.Getpid())
		time.Sleep(2 * time.Minute)
		os.Exit(0)
	}
}

// startRole starts this binary in a role and returns what it says before it goes quiet: one line
// for the grandchild, two for the child.
func startRole(role string) ([]string, error) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestJobHelper$")
	cmd.Env = append(os.Environ(), jobRole+"="+role)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	want := map[string]int{"grandchild": 1, "child": 2}[role]
	scanner := bufio.NewScanner(out)
	var lines []string
	for len(lines) < want && scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) < want {
		return nil, fmt.Errorf("%s said %q", role, lines)
	}
	return lines, nil
}

// family runs a parent in a role and returns the numbers of the child and the grandchild.
func family(t *testing.T, role string) (child, grandchild uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJobHelper$")
	cmd.Env = append(os.Environ(), jobRole+"="+role)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the parent: %v\n%s", err, out)
	}
	pids := map[string]uint32{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		name, number, _ := strings.Cut(strings.TrimSpace(line), " ")
		n, err := strconv.ParseUint(number, 10, 32)
		if err != nil {
			t.Fatalf("the parent said %q", out)
		}
		pids[name] = uint32(n)
	}
	if pids["child"] == 0 || pids["grandchild"] == 0 {
		t.Fatalf("the parent said %q", out)
	}
	return pids["child"], pids["grandchild"]
}

// endsWithin says whether a process ends within the time; a process that is already gone ends at once.
func endsWithin(t *testing.T, pid uint32, wait time.Duration) bool {
	t.Helper()
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return true // no such process any more
	}
	defer windows.CloseHandle(h)
	event, err := windows.WaitForSingleObject(h, uint32(wait/time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	return event == windows.WAIT_OBJECT_0
}

func terminate(pid uint32) {
	if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid); err == nil {
		_ = windows.TerminateProcess(h, 1)
		_ = windows.CloseHandle(h)
	}
}

// req: E4
func TestOnWindowsTheProgramsOfAToolServerEndWithMetagente(t *testing.T) {
	child, grandchild := family(t, "parent")
	t.Cleanup(func() { terminate(grandchild); terminate(child) })
	if !endsWithin(t, grandchild, 10*time.Second) {
		t.Error("the grandchild was left running after the process that put itself in the job ended")
	}
	if !endsWithin(t, child, 10*time.Second) {
		t.Error("the child was left running after the process that put itself in the job ended")
	}
}

// Without the job the grandchild is left behind: what the test above sees is the job's doing.
func TestOnWindowsWithoutTheJobTheGrandchildIsLeftRunning(t *testing.T) {
	child, grandchild := family(t, "parent-without-job")
	t.Cleanup(func() { terminate(grandchild); terminate(child) })
	if endsWithin(t, grandchild, 2*time.Second) {
		t.Error("the grandchild ended by itself: the test of the job would prove nothing")
	}
}
