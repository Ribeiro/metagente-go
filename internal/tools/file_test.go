package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

func newFile(root, scope string, readOnly bool, maxBytes int64) *File {
	decl := &lang.ToolDecl{Name: "file", Kind: lang.ToolFile, Scope: scope, HasScope: scope != "", ReadOnly: readOnly}
	limits := config.Default().Limits
	if maxBytes > 0 {
		limits.MaxFileBytes = maxBytes
	}
	return NewFile(root, decl, limits)
}

func args(pairs ...string) Args {
	a := Args{}
	for i := 0; i+1 < len(pairs); i += 2 {
		a[pairs[i]] = value.Text(pairs[i+1])
	}
	return a
}

// rendered turns an error into the text a person would read.
func rendered(t *testing.T, err error) string {
	t.Helper()
	if err == nil {
		t.Fatal("expected a problem")
	}
	d, ok := diag.From(err)
	if !ok {
		t.Fatalf("the error is not a diagnostic: %v", err)
	}
	return d.Render()
}

func mustContain(t *testing.T, text string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWriteThenReadRoundTripsAndCreatesFolders(t *testing.T) {
	dir := t.TempDir()
	f := newFile(dir, "", false, 0)
	ctx := context.Background()
	if _, err := f.Call(ctx, "write", args("path", "out/a.txt", "text", "remember me")); err != nil {
		t.Fatal(rendered(t, err))
	}
	got, err := f.Call(ctx, "read", args("path", "out/a.txt"))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if got.Text != "remember me" {
		t.Errorf("read back %q", got.Text)
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "out", "a.txt")); string(onDisk) != "remember me" {
		t.Errorf("on disk: %q", onDisk)
	}

	// Writing again replaces the content instead of appending to it.
	if _, err := f.Call(ctx, "write", args("path", "out/a.txt", "text", "short")); err != nil {
		t.Fatal(rendered(t, err))
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "out", "a.txt")); string(onDisk) != "short" {
		t.Errorf("on disk after a second write: %q", onDisk)
	}
}

func TestAMissingFileIsExplainedWithoutShowingWhereTheProjectLives(t *testing.T) {
	dir := t.TempDir()
	f := newFile(dir, "", false, 0)
	_, err := f.Call(context.Background(), "read", args("path", "notes.txt"))
	text := rendered(t, err)
	mustContain(t, text, "the file `notes.txt` does not exist", "files are looked up from the project folder")
	if strings.Contains(text, dir) {
		t.Errorf("the message shows the absolute path of the project:\n%s", text)
	}
}

// req: F1
func TestAScopedToolStaysInsideItsFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "data", "ok.txt"), "fine")
	write(t, filepath.Join(dir, "secret.txt"), "top secret")
	f := newFile(dir, "data/", false, 0)
	ctx := context.Background()

	got, err := f.Call(ctx, "read", args("path", "ok.txt"))
	if err != nil || got.Text != "fine" {
		t.Fatalf("reading inside the folder: %v %v", got, err)
	}
	for _, bad := range []string{"../secret.txt", "../../etc/passwd", "sub/../../secret.txt", filepath.Join(dir, "secret.txt")} {
		_, err := f.Call(ctx, "read", args("path", bad))
		mustContain(t, rendered(t, err), "outside the folder this agent may use (data/)")
	}
	_, err = f.Call(ctx, "write", args("path", "../evil.txt", "text", "x"))
	mustContain(t, rendered(t, err), "outside the folder")
	if _, statErr := os.Stat(filepath.Join(dir, "evil.txt")); statErr == nil {
		t.Error("a file was written outside the folder")
	}
}

func TestTheDefaultScopeIsTheProjectFolder(t *testing.T) {
	outer := t.TempDir()
	project := filepath.Join(outer, "project")
	write(t, filepath.Join(project, "inside.txt"), "yes")
	write(t, filepath.Join(outer, "outside.txt"), "no")
	f := newFile(project, "", false, 0)

	if got, err := f.Call(context.Background(), "read", args("path", "inside.txt")); err != nil || got.Text != "yes" {
		t.Fatalf("reading inside: %v %v", got, err)
	}
	_, err := f.Call(context.Background(), "read", args("path", "../outside.txt"))
	mustContain(t, rendered(t, err), "outside the folder this agent may use (the project folder)")
}

// req: F1
func TestASymbolicLinkCannotLeadOutOfTheFolder(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "secret.txt"), "top secret")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(dir, "secret.txt"), filepath.Join(dir, "data", "link.txt"))
	symlink(t, dir, filepath.Join(dir, "data", "up"))
	f := newFile(dir, "data/", false, 0)
	ctx := context.Background()

	for _, bad := range []string{"link.txt", "up/secret.txt"} {
		_, err := f.Call(ctx, "read", args("path", bad))
		mustContain(t, rendered(t, err), "outside the folder")
	}
	// Writing through the link must not change the file it points to either.
	_, err := f.Call(ctx, "write", args("path", "link.txt", "text", "overwritten"))
	mustContain(t, rendered(t, err), "outside the folder")
	if got, _ := os.ReadFile(filepath.Join(dir, "secret.txt")); string(got) != "top secret" {
		t.Errorf("the file behind the link was changed: %q", got)
	}
}

// req: F4
func TestWritingToAHardLinkIsRefused(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(dir, "outside.txt")
	write(t, outside, "original")
	if err := os.MkdirAll(filepath.Join(dir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(dir, "data", "h.txt")); err != nil {
		t.Skipf("this file system does not allow hard links: %v", err)
	}
	f := newFile(dir, "data/", false, 0)

	_, err := f.Call(context.Background(), "write", args("path", "h.txt", "text", "changed"))
	mustContain(t, rendered(t, err), "more than one name on disk")
	if got, _ := os.ReadFile(outside); string(got) != "original" {
		t.Errorf("the file outside the folder was changed: %q", got)
	}
}

func TestReadOnlyRefusesWriting(t *testing.T) {
	dir := t.TempDir()
	f := newFile(dir, "", true, 0)
	_, err := f.Call(context.Background(), "write", args("path", "a.txt", "text", "x"))
	mustContain(t, rendered(t, err), "`file.write` is not available because `tool file` was declared readonly")
	if _, statErr := os.Stat(filepath.Join(dir, "a.txt")); statErr == nil {
		t.Error("a readonly tool wrote a file")
	}
}

// req: F2
func TestFilesOverTheLimitAreRefused(t *testing.T) {
	dir := t.TempDir()
	f := newFile(dir, "", false, 100)
	ctx := context.Background()

	_, err := f.Call(ctx, "write", args("path", "big.txt", "text", strings.Repeat("x", 101)))
	mustContain(t, rendered(t, err), "larger than the 100 bytes", "max_file_bytes")

	write(t, filepath.Join(dir, "existing.txt"), strings.Repeat("y", 200))
	_, err = f.Call(ctx, "read", args("path", "existing.txt"))
	mustContain(t, rendered(t, err), "larger than the 100 bytes")

	write(t, filepath.Join(dir, "edge.txt"), strings.Repeat("z", 100))
	if got, err := f.Call(ctx, "read", args("path", "edge.txt")); err != nil || len(got.Text) != 100 {
		t.Errorf("a file exactly at the limit must be readable: %v", err)
	}
}

func TestAFileThatIsNotTextIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "blob.bin"), []byte{0xff, 0xfe, 0x00, 0x80}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := newFile(dir, "", false, 0).Call(context.Background(), "read", args("path", "blob.bin"))
	mustContain(t, rendered(t, err), "is not a text file I can read")
}

func TestReadingAFolderIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := newFile(dir, "", false, 0).Call(context.Background(), "read", args("path", "sub"))
	mustContain(t, rendered(t, err), "is a folder, not a file")
}

// req: F3
func TestNamesThatAreNotPortableAreRefused(t *testing.T) {
	bad := []string{
		"CON", "con.txt", "NUL", "aux", "COM1", "lpt9.log", "dir/PRN.md",
		"a:b", "report.txt:stream", "trailing.", "trailing ", "nul\x00byte",
		`\\?\C:\x`, `\\server\share\x`,
	}
	for _, name := range bad {
		if badName(name) == "" {
			t.Errorf("%q should be refused", name)
		}
	}
	good := []string{"notes.txt", "reports/2026/today.md", "console.txt", "com10.txt", "a b.txt", ".hidden", "dir/..hidden", "conf", "a.b.c"}
	for _, name := range good {
		if reason := badName(name); reason != "" {
			t.Errorf("%q should be accepted, but: %s", name, reason)
		}
	}

	f := newFile(t.TempDir(), "", false, 0)
	_, err := f.Call(context.Background(), "read", args("path", "CON"))
	mustContain(t, rendered(t, err), "is not a file name this agent may use", "reserved Windows device name")
	_, err = f.Call(context.Background(), "read", args("path", "   "))
	mustContain(t, rendered(t, err), "the file name is empty")
}

func TestMissingValuesAndUnknownActionsAreExplained(t *testing.T) {
	f := newFile(t.TempDir(), "", false, 0)
	ctx := context.Background()

	_, err := f.Call(ctx, "write", args("path", "a.txt"))
	mustContain(t, rendered(t, err), "`file.write` needs a value for `text`")
	_, err = f.Call(ctx, "read", Args{})
	mustContain(t, rendered(t, err), "`file.read` needs a value for `path`")

	_, err = f.Call(ctx, "copy", Args{})
	mustContain(t, rendered(t, err), "`file` has no action called `copy`", "`file` can do: read, write")

	readOnly := newFile(t.TempDir(), "", true, 0)
	_, err = readOnly.Call(ctx, "copy", Args{})
	mustContain(t, rendered(t, err), "`file` can do: read")
	if strings.Contains(rendered(t, err), "write") {
		t.Error("a readonly tool must not offer write")
	}
}

// req: F4
func TestAWriteLeavesNoTemporaryFileBehindAndKeepsThePermissions(t *testing.T) {
	dir := t.TempDir()
	f := newFile(dir, "", false, 0)
	ctx := context.Background()
	target := filepath.Join(dir, "notes.txt")
	write(t, target, "old")
	if runtime.GOOS != "windows" {
		if err := os.Chmod(target, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	for _, text := range []string{"first", "second"} {
		if _, err := f.Call(ctx, "write", args("path", "notes.txt", "text", text)); err != nil {
			t.Fatal(rendered(t, err))
		}
	}
	if got, _ := os.ReadFile(target); string(got) != "second" {
		t.Errorf("content = %q", got)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "notes.txt" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("the folder should hold only notes.txt, it holds %v", names)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(target); info.Mode().Perm() != 0o600 {
			t.Errorf("the permissions changed to %v", info.Mode().Perm())
		}
	}
}

// req: F4
func TestAWriteInANewFolderLeavesNoTemporaryFileEither(t *testing.T) {
	dir := t.TempDir()
	f := newFile(dir, "", false, 0)
	if _, err := f.Call(context.Background(), "write", args("path", "a/b/c.txt", "text", "x")); err != nil {
		t.Fatal(rendered(t, err))
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "a", "b"))
	if len(entries) != 1 || entries[0].Name() != "c.txt" {
		t.Errorf("unexpected content of the folder: %v", entries)
	}
}

func TestAWriteThroughALinkInsideTheFolderChangesTheFileItPointsTo(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "data", "real.txt"), "old")
	symlink(t, "real.txt", filepath.Join(dir, "data", "alias.txt"))
	f := newFile(dir, "data/", false, 0)
	if _, err := f.Call(context.Background(), "write", args("path", "alias.txt", "text", "new")); err != nil {
		t.Fatal(rendered(t, err))
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "data", "real.txt")); string(got) != "new" {
		t.Errorf("the target of the link has %q", got)
	}
	if info, err := os.Lstat(filepath.Join(dir, "data", "alias.txt")); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced by a regular file")
	}
}

func TestWritingOverAFolderIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := newFile(dir, "", false, 0).Call(context.Background(), "write", args("path", "sub", "text", "x"))
	mustContain(t, rendered(t, err), "is not a regular file")
}

// symlink makes a symbolic link. Where the one who runs the tests may not make one (on Windows that
// takes the privilege to create symbolic links), the test is skipped; anywhere else, it fails.
func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("this computer does not let the test make symbolic links: %v", err)
		}
		t.Fatal(err)
	}
}
