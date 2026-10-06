package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// File is `tool file`: it reads and writes text files inside one folder and
// nothing else. All access goes through an os.Root opened on that folder, so
// `..` and symbolic links that lead out are refused by the operating system
// call itself, with no gap between checking a path and using it (F1).
type File struct {
	decl     *lang.ToolDecl
	base     string // the folder that nothing may leave
	shown    string // how the folder reads in messages
	maxBytes int64
}

// NewFile creates the tool. Without a scope the folder is the project folder;
// with `tool file "data/"` it is that folder inside the project.
func NewFile(root string, decl *lang.ToolDecl, limits config.Limits) *File {
	base, shown := filepath.Clean(root), "the project folder"
	if decl.HasScope {
		base = filepath.Clean(filepath.Join(root, filepath.FromSlash(decl.Scope)))
		shown = decl.Scope
	}
	return &File{decl: decl, base: base, shown: shown, maxBytes: limits.MaxFileBytes}
}

func (f *File) Name() string { return f.decl.Name }

func (f *File) Actions(context.Context) ([]lang.ActionInfo, error) {
	return lang.BuiltinActions(f.decl), nil
}

func (f *File) Call(_ context.Context, action string, args Args) (value.Value, error) {
	switch action {
	case "read":
		return f.read(args)
	case "write":
		if f.decl.ReadOnly {
			return value.Nothing, readOnlyError("file", "write")
		}
		return f.write(args)
	}
	return value.Nothing, UnknownAction("file", action, actionNames(lang.BuiltinActions(f.decl)))
}

func (f *File) outside(given string) error {
	return diag.Newf("`%s` is outside the folder this agent may use (%s)", printable(given), f.shown).
		Fix("keep the file inside that folder, or change the agent's declaration, for example: tool file \"other-folder/\"")
}

func printable(s string) string {
	return strings.ReplaceAll(s, "\x00", `\x00`)
}

// cleanName turns the name an agent gave into a relative name inside the
// folder, or refuses it.
func (f *File) cleanName(given string) (string, error) {
	if strings.TrimSpace(given) == "" {
		return "", diag.New("the file name is empty").
			Fix("write the name of a file, for example: notes.txt")
	}
	// Leaving the folder is reported first: an absolute Windows path has a `:`
	// that would otherwise be blamed for it.
	cleaned := path.Clean(filepath.ToSlash(given))
	if path.IsAbs(cleaned) || filepath.IsAbs(given) || filepath.VolumeName(given) != "" ||
		cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", f.outside(given)
	}
	if why := badName(given); why != "" {
		return "", diag.Newf("`%s` is not a file name this agent may use: %s", printable(given), why).
			Fix("use a plain relative name such as notes.txt or reports/today.txt")
	}
	return cleaned, nil
}

// isEscape tells the error of os.Root for a path that leaves the folder (a
// `..` or a symbolic link that points outside).
func isEscape(err error) bool {
	return err != nil && strings.Contains(err.Error(), "escapes")
}

// reason gives the part of a file system error that is safe to show: it leaves
// out the path, which can reveal where the project lives.
func reason(err error) string {
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err.Error()
	}
	return err.Error()
}

func (f *File) fileError(given, verb string, err error) error {
	switch {
	case isEscape(err):
		return f.outside(given)
	case errors.Is(err, fs.ErrNotExist):
		return diag.Newf("the file `%s` does not exist", printable(given)).
			Fixf("check the spelling; files are looked up from %s", f.shown)
	default:
		return diag.Newf("I could not %s `%s`: %s", verb, printable(given), reason(err)).
			Fixf("check that you are allowed to %s that file", verb)
	}
}

func (f *File) tooBig(given string) error {
	return diag.Newf("`%s` is larger than the %d bytes a file may have here", printable(given), f.maxBytes).
		Fix("use a smaller file, or raise max_file_bytes in the [limits] section of metagente.toml")
}

func (f *File) read(args Args) (value.Value, error) {
	given, err := NeedText("file", "read", args, "path")
	if err != nil {
		return value.Nothing, err
	}
	name, err := f.cleanName(given)
	if err != nil {
		return value.Nothing, err
	}
	root, err := os.OpenRoot(f.base)
	if err != nil {
		return value.Nothing, f.fileError(given, "read", err)
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		return value.Nothing, f.fileError(given, "read", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return value.Nothing, f.fileError(given, "read", err)
	}
	if info.IsDir() {
		return value.Nothing, diag.Newf("`%s` is a folder, not a file", printable(given)).
			Fix("name a file inside it")
	}
	if info.Size() > f.maxBytes {
		return value.Nothing, f.tooBig(given)
	}
	data, err := io.ReadAll(io.LimitReader(file, f.maxBytes+1))
	if err != nil {
		return value.Nothing, f.fileError(given, "read", err)
	}
	if int64(len(data)) > f.maxBytes {
		return value.Nothing, f.tooBig(given)
	}
	if !utf8.Valid(data) {
		return value.Nothing, diag.Newf("`%s` is not a text file I can read", printable(given)).
			Fix("only plain text files can be read with file.read")
	}
	return value.Text(string(data)), nil
}

func (f *File) write(args Args) (value.Value, error) {
	given, err := NeedText("file", "write", args, "path")
	if err != nil {
		return value.Nothing, err
	}
	text, err := NeedText("file", "write", args, "text")
	if err != nil {
		return value.Nothing, err
	}
	name, err := f.cleanName(given)
	if err != nil {
		return value.Nothing, err
	}
	if int64(len(text)) > f.maxBytes {
		return value.Nothing, f.tooBig(given)
	}
	if err := os.MkdirAll(f.base, 0o755); err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	root, err := os.OpenRoot(f.base)
	if err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	defer root.Close()
	if dir := path.Dir(name); dir != "." {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return value.Nothing, f.fileError(given, "write", err)
		}
	}

	existing, statErr := root.Lstat(name)
	switch {
	case statErr == nil && existing.Mode()&fs.ModeSymlink != 0:
		// A link inside the folder is written through, in place, as before. A link
		// that points outside is refused by the root.
		return f.writeInPlace(root, given, name, text)
	case statErr == nil && !existing.Mode().IsRegular():
		return value.Nothing, diag.Newf("`%s` is not a regular file", printable(given)).
			Fix("write to a file name instead")
	case statErr == nil && linkCount(root, name, existing) > 1:
		return value.Nothing, diag.Newf("`%s` has more than one name on disk (a hard link), so writing to it could change a file outside this folder", printable(given)).
			Fix("write to a new file name instead, or remove the extra link")
	case statErr != nil && !errors.Is(statErr, fs.ErrNotExist):
		return value.Nothing, f.fileError(given, "write", statErr)
	}
	if statErr != nil {
		existing = nil
	}
	return f.writeAtomically(root, given, name, text, existing)
}

// writeAtomically writes to a temporary file in the same folder and then
// renames it over the target (requirement F4). If anything fails on the way,
// the old content is still there, and the temporary file is removed. Even if the
// target is swapped for a link at the last moment, the rename replaces the name
// and never writes through it.
func (f *File) writeAtomically(root *os.Root, given, name, text string, existing os.FileInfo) (value.Value, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	temp := path.Join(path.Dir(name), "."+path.Base(name)+".tmp-"+hex.EncodeToString(random))
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	finished := false
	defer func() {
		if !finished {
			_ = root.Remove(temp)
		}
	}()
	if _, err := file.WriteString(text); err != nil {
		file.Close()
		return value.Nothing, f.fileError(given, "write", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return value.Nothing, f.fileError(given, "write", err)
	}
	if err := file.Close(); err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	if existing != nil {
		// Replacing a file keeps the permissions it had.
		if err := root.Chmod(temp, existing.Mode().Perm()); err != nil {
			return value.Nothing, f.fileError(given, "write", err)
		}
	}
	if err := root.Rename(temp, name); err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	finished = true
	return value.Nothing, nil
}

// writeInPlace writes into the file a link inside the folder points to.
func (f *File) writeInPlace(root *os.Root, given, name, text string) (value.Value, error) {
	// Open without truncating: the checks must come before any byte of an
	// existing file is lost.
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	if !info.Mode().IsRegular() {
		return value.Nothing, diag.Newf("`%s` is not a regular file", printable(given)).
			Fix("write to a file name instead")
	}
	if linkCount(root, name, info) > 1 {
		return value.Nothing, diag.Newf("`%s` has more than one name on disk (a hard link), so writing to it could change a file outside this folder", printable(given)).
			Fix("write to a new file name instead, or remove the extra link")
	}
	if err := file.Truncate(0); err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	if _, err := file.WriteString(text); err != nil {
		return value.Nothing, f.fileError(given, "write", err)
	}
	return value.Nothing, nil
}

var reservedNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true, "CONIN$": true, "CONOUT$": true,
}

func isReserved(component string) bool {
	base := component
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToUpper(strings.TrimRight(base, " "))
	if reservedNames[base] {
		return true
	}
	return len(base) == 4 && (strings.HasPrefix(base, "COM") || strings.HasPrefix(base, "LPT")) &&
		base[3] >= '1' && base[3] <= '9'
}

// badName says why a name cannot be used, or returns "" when it can. The rules
// are those of the strictest system (Windows) and apply everywhere, so an
// agent that works on one machine works on all of them (requirement F3).
func badName(name string) string {
	if strings.ContainsRune(name, 0) {
		return "it contains a character that cannot be part of a file name"
	}
	if strings.HasPrefix(name, `\\`) {
		return "it is a Windows device or network path"
	}
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' || r == '\\' })
	for _, part := range parts {
		switch {
		case part == "." || part == "..":
		case strings.ContainsRune(part, ':'):
			return "a `:` is used by Windows for drives and streams"
		case strings.HasSuffix(part, ".") || strings.HasSuffix(part, " "):
			return "a name that ends with a dot or a space is not portable"
		case isReserved(part):
			return "it is a reserved Windows device name"
		}
	}
	return ""
}
