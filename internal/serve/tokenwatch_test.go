package serve

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// watched runs WatchTokenFile on a file, and keeps what it applied and what it said.
type watched struct {
	mu      sync.Mutex
	applied [][]Credential
	said    []string
}

func (w *watched) apply(creds []Credential) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.applied = append(w.applied, creds)
	return nil
}

func (w *watched) say(line string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.said = append(w.said, line)
}

func (w *watched) state() (int, string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.applied), strings.Join(w.said, "\n")
}

func (w *watched) wait(t *testing.T, what string, ok func(applied int, said string) bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if ok(w.state()) {
			return
		}
	}
	applied, said := w.state()
	t.Fatalf("%s did not happen: %d applied, said:\n%s", what, applied, said)
}

// req: S1
func TestTheTokenFileIsReadAgainOnlyWhenItChangesAndAProblemIsToldOnce(t *testing.T) {
	mac, notebook := GenerateToken(), GenerateToken()
	path := filepath.Join(t.TempDir(), "tokens")
	write := func(text string) {
		t.Helper()
		if err := os.WriteFile(path+".new", []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := renameOver(path+".new", path); err != nil {
			t.Fatal(err)
		}
	}
	write("mac " + mac + "\n")
	since, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	w := &watched{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { WatchTokenFile(ctx, path, since, time.Millisecond, w.apply, w.say); close(done) }()
	defer func() { cancel(); <-done }()

	time.Sleep(30 * time.Millisecond)
	if applied, said := w.state(); applied != 0 || said != "" {
		t.Fatalf("a file that did not change was read again: %d, %q", applied, said)
	}

	write("mac " + mac + "\nnotebook " + notebook + "\n")
	// A file replaced by a rename may be seen in two steps (the new name, then the old one gone), so the
	// change may be applied twice. What matters is that it is applied and told.
	w.wait(t, "the change", func(applied int, said string) bool {
		return applied >= 1 && strings.Contains(said, "2 tokens, of mac, notebook")
	})

	write("mac " + mac + "\nnotebook short\n")
	w.wait(t, "the problem", func(_ int, said string) bool { return strings.Contains(said, "line 2 (notebook)") })
	removeWhenFree(t, path)
	w.wait(t, "the missing file", func(_ int, said string) bool { return strings.Contains(said, "could not open the token file") })
	time.Sleep(30 * time.Millisecond)
	if _, said := w.state(); strings.Count(said, "Problem:") != 2 {
		t.Errorf("a problem was told more than once:\n%s", said)
	}

	before, _ := w.state()
	write("notebook " + notebook + "\n")
	w.wait(t, "the file that came back", func(applied int, said string) bool {
		return applied > before && strings.Contains(said, "1 token, of notebook")
	})
	applied, said := w.state()
	if strings.Contains(said, mac) || strings.Contains(said, notebook) {
		t.Errorf("a token was told; %d applied; said:\n%s", applied, said)
	}
	w.mu.Lock()
	last := w.applied[len(w.applied)-1]
	w.mu.Unlock()
	if len(last) != 1 || last[0] != (Credential{"notebook", notebook}) {
		t.Errorf("applied %q", last)
	}
}

func TestTheTokensAreDescribedByTheirNames(t *testing.T) {
	for want, creds := range map[string][]Credential{
		"1 token, without a name": {{Token: "x"}},
		"1 token, of mac":         {{"mac", "x"}},
		"2 tokens, of mac, phone": {{"mac", "x"}, {"phone", "y"}},
	} {
		if got := DescribeTokens(creds); got != want {
			t.Errorf("%q, want %q", got, want)
		}
	}
}

// renameOver puts a file in the place of another. On Windows a file that is being read
// cannot be replaced, and the server reads the token file often, so it tries again.
func renameOver(from, to string) error {
	var err error
	for range 50 {
		if err = os.Rename(from, to); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return err
}

// req: S1
func TestATokenFileGoneForAMomentIsNotAProblem(t *testing.T) {
	mac := GenerateToken()
	path := filepath.Join(t.TempDir(), "tokens")
	text := []byte("mac " + mac + "\n")
	if err := os.WriteFile(path, text, 0o600); err != nil {
		t.Fatal(err)
	}
	since, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	w := &watched{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { WatchTokenFile(ctx, path, since, time.Millisecond, w.apply, w.say); close(done) }()
	defer func() { cancel(); <-done }()

	// Gone, as while a file is replaced by hand or held by whoever removes it, and back well within
	// the moment a problem has to last.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	time.Sleep(minSettle / 5)
	if err := os.WriteFile(path, text, 0o600); err != nil {
		t.Fatal(err)
	}
	// os.WriteFile is not one step: the watcher may look while the file is being written and read
	// it again once it is whole, so the file that came back may be applied twice. What matters is
	// that it is applied, and that nothing was told about the moment it was gone.
	w.wait(t, "the file that came back", func(applied int, _ string) bool { return applied >= 1 })
	time.Sleep(minSettle + 100*time.Millisecond)
	if _, said := w.state(); strings.Contains(said, "Problem:") {
		t.Errorf("a file gone for a moment was told as a problem:\n%s", said)
	}
}

// removeWhenFree removes a file that the watcher may have open at this very moment: Windows refuses to remove a file
// that is open, so the removal is tried again for a short while.
func removeWhenFree(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := os.Remove(path)
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
