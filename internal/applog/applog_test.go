package applog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func read(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func freshLog(t *testing.T) (*Log, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	l := New(dir)
	l.now = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	return l, dir
}

// req: P2
func TestAnEntryIsWrittenToAPrivateFileInAPrivateFolder(t *testing.T) {
	l, dir := freshLog(t)
	path, err := l.Write("ERROR", "think", "the model said no", "first detail\nsecond detail")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, "metagente.log") {
		t.Errorf("path = %s", path)
	}
	want := "2026-10-03T12:00:00Z ERROR think: the model said no\n\tfirst detail\n\tsecond detail\n"
	if got := read(t, path); got != want {
		t.Errorf("log:\n%q\nwant:\n%q", got, want)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Errorf("the file has permissions %v, want 0600", info.Mode().Perm())
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Errorf("the folder has permissions %v, want 0700", info.Mode().Perm())
	}
}

func TestEntriesAreAddedAndEachMessageIsOneLine(t *testing.T) {
	l, _ := freshLog(t)
	_, _ = l.Write("ERROR", "a", "one\nmessage\twith   spaces", "")
	path, _ := l.Write("ERROR", "b", "two", "")
	lines := strings.Split(strings.TrimSpace(read(t, path)), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "a: one message with spaces") || !strings.HasSuffix(lines[1], "b: two") {
		t.Errorf("lines = %q", lines)
	}
}

// req: P3
func TestSecretsAreTakenOutOfEverythingThatIsWritten(t *testing.T) {
	const key = "sk-ant-api03-abcdef0123456789"
	const token = "tok-live-0123456789"
	l, _ := freshLog(t)
	l = l.With(func() []string { return []string{key} }).With(func() []string { return []string{token, "tiny"} })
	path, err := l.Panic("evalCall", "boom with "+key, []byte("goroutine 1\n\tcalled with "+token+"\n\ta tiny word"))
	if err != nil {
		t.Fatal(err)
	}
	text := read(t, path)
	if strings.Contains(text, key) || strings.Contains(text, token) {
		t.Errorf("a secret reached the log:\n%s", text)
	}
	if !strings.Contains(text, "boom with [hidden]") || !strings.Contains(text, "called with [hidden]") {
		t.Errorf("the secrets were not replaced:\n%s", text)
	}
	if !strings.Contains(text, "a tiny word") {
		t.Errorf("a short word was hidden, which makes the log unreadable:\n%s", text)
	}
}

// req: P3
func TestTheDefaultLogAlwaysHidesTheKeysOfTheProviders(t *testing.T) {
	const key = "sk-ant-api03-abcdef0123456789"
	t.Setenv("ANTHROPIC_API_KEY", key)
	t.Setenv("METAGENTE_STATE_DIR", filepath.Join(t.TempDir(), "state"))
	path, err := Default().Write("ERROR", "x", "failed with "+key, "")
	if err != nil {
		t.Fatal(err)
	}
	if text := read(t, path); strings.Contains(text, key) || !strings.Contains(text, "failed with [hidden]") {
		t.Errorf("log:\n%s", text)
	}
}

func TestTheFolderOfTheDefaultLogFollowsTheEnvironment(t *testing.T) {
	t.Setenv("METAGENTE_STATE_DIR", "/somewhere/private")
	if got := DefaultDir(); got != "/somewhere/private" {
		t.Errorf("DefaultDir() = %q", got)
	}
	if got := Default().Path(); got != "/somewhere/private/metagente.log" && runtime.GOOS != "windows" {
		t.Errorf("Path() = %q", got)
	}
	t.Setenv("METAGENTE_STATE_DIR", "")
	if dir := DefaultDir(); dir != "" && !strings.HasSuffix(filepath.ToSlash(dir), "metagente") {
		t.Errorf("DefaultDir() = %q", dir)
	}
}

// req: P2
func TestALinkInPlaceOfTheLogCannotSendTheWritesElsewhere(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symbolic links needs privileges on Windows")
	}
	l, dir := freshLog(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim.txt")
	if err := os.WriteFile(victim, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, "metagente.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write("ERROR", "x", "attack", ""); err == nil {
		t.Fatal("a log that is a link to another place was written")
	}
	if got := read(t, victim); got != "do not touch" {
		t.Errorf("the file the link pointed to changed: %q", got)
	}
}

func TestAFolderOthersCanChangeIsNotUsed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits mean something else on Windows")
	}
	l, dir := freshLog(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Write("ERROR", "x", "y", ""); err == nil || !strings.Contains(err.Error(), "can be changed by other users") {
		t.Errorf("err = %v", err)
	}
}

func TestAFailureToWriteIsReportedAndNeverRaised(t *testing.T) {
	notAFolder := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(notAFolder, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(notAFolder).Write("ERROR", "x", "y", ""); err == nil {
		t.Error("writing in a place that is not a folder was said to work")
	}
	if _, err := New("").Write("ERROR", "x", "y", ""); err == nil {
		t.Error("a log without a folder was said to work")
	}
	broken := New(filepath.Join(t.TempDir(), "s")).With(func() []string { panic("a broken source") })
	if _, err := broken.Write("ERROR", "x", "y", ""); err == nil {
		t.Error("a panic while writing was not turned into an error")
	}
	if _, err := broken.Write("ERROR", "again", "y", ""); err == nil {
		t.Error("the second write did not fail the same way, so the lock may have been left taken")
	}
}

func TestTheFileDoesNotGrowWithoutEnd(t *testing.T) {
	l, dir := freshLog(t)
	l.maxBytes = 200
	for i := 0; i < 30; i++ {
		if _, err := l.Write("ERROR", "x", strings.Repeat("a", 40), ""); err != nil {
			t.Fatal(err)
		}
	}
	current, old := filepath.Join(dir, "metagente.log"), filepath.Join(dir, "metagente.log.1")
	for _, path := range []string{current, old} {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			t.Fatalf("%s: %v", path, err)
		}
	}
	if info, _ := os.Stat(current); info.Size() > 400 {
		t.Errorf("the current file has %d bytes", info.Size())
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 2 {
		t.Errorf("the folder should hold two files, it holds %d", len(entries))
	}
}

func TestAnEntryThatIsTooLongIsCutAndSaysSo(t *testing.T) {
	l, _ := freshLog(t)
	path, err := l.Write("ERROR", "x", "big", strings.Repeat("é", maxEntryBytes))
	if err != nil {
		t.Fatal(err)
	}
	text := read(t, path)
	if len(text) > maxEntryBytes+100 || !strings.HasSuffix(text, "[cut: the entry was too long]\n") {
		t.Errorf("entry of %d bytes, ending %q", len(text), text[len(text)-40:])
	}
}

func TestManyWritersDoNotMixTheirLines(t *testing.T) {
	l, _ := freshLog(t)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = l.Write("ERROR", "writer", strings.Repeat("x", 200), "")
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimSpace(read(t, l.Path())), "\n")
	if len(lines) != 40 {
		t.Fatalf("got %d lines, want 40", len(lines))
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, strings.Repeat("x", 200)) || strings.Count(line, "ERROR") != 1 {
			t.Errorf("a damaged line: %q", line)
		}
	}
}
