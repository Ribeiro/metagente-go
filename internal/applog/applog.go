// Package applog keeps the details of failures that happen inside Metagente
// (requirements P2 and P3).
//
// When something breaks inside, the person reads one plain sentence, and a remote
// caller reads even less. Everything else, the cause and the stack, goes to a log
// file that only the user can read. It lives in the folder of the user, never in
// a predictable shared place like /tmp where someone else could plant a link, it
// does not follow links out of its folder, and every secret is taken out before a
// line is written.
package applog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Ribeiro/metagente-go/internal/secret"
)

const (
	fileName      = "metagente.log"
	maxEntryBytes = 64 << 10
	maxFileBytes  = 1 << 20
)

// writeMu orders the writes of every Log in the process: they share one file.
var writeMu sync.Mutex

// Log writes to the log file. It is cheap to make, and safe to use from many
// goroutines.
type Log struct {
	dir      func() string
	sources  []func() []string
	maxBytes int64
	now      func() time.Time
}

// New logs in the given folder.
func New(dir string) *Log {
	return &Log{dir: func() string { return dir }, maxBytes: maxFileBytes, now: time.Now}
}

// Default logs in the folder of the user. The folder is worked out at every
// write, so a change of METAGENTE_STATE_DIR is seen.
func Default() *Log {
	return &Log{dir: DefaultDir, sources: []func() []string{baselineSecrets}, maxBytes: maxFileBytes, now: time.Now}
}

// With returns a Log that also hides the secrets the source gives. The source is
// asked at every write, because a variable can be set after the program began.
func (l *Log) With(source func() []string) *Log {
	copied := *l
	copied.sources = append(append([]func() []string(nil), l.sources...), source)
	return &copied
}

// baselineSecrets are the keys that are always secret, whatever the configuration.
func baselineSecrets() []string {
	var values []string
	for _, name := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "METAGENTE_TOKEN"} {
		if v := os.Getenv(name); v != "" {
			values = append(values, v)
		}
	}
	return values
}

// DefaultDir is the folder of the log of this user: METAGENTE_STATE_DIR, or the
// place the system keeps logs and state.
func DefaultDir() string {
	if dir := os.Getenv("METAGENTE_STATE_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Logs", "metagente")
	case "windows":
		if dir := os.Getenv("LocalAppData"); dir != "" {
			return filepath.Join(dir, "metagente")
		}
		return filepath.Join(home, "AppData", "Local", "metagente")
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "metagente")
	}
	return filepath.Join(home, ".local", "state", "metagente")
}

// Path is the log file, or "" when there is no folder for it.
func (l *Log) Path() string {
	dir := l.dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, fileName)
}

func (l *Log) secrets() []string {
	var all []string
	for _, source := range l.sources {
		all = append(all, source()...)
	}
	return all
}

// Panic records a failure that was recovered, with its stack. It returns the
// file, so the person can be told where the details are.
func (l *Log) Panic(where string, recovered any, stack []byte) (string, error) {
	return l.Write("PANIC", where, fmt.Sprint(recovered), string(stack))
}

// Write adds an entry. Failing to write is reported, never raised: a log that
// cannot be written must not make a failure worse.
func (l *Log) Write(level, where, message, detail string) (path string, err error) {
	defer func() {
		if r := recover(); r != nil {
			path, err = "", errors.New("the log could not be written")
		}
	}()
	dir := l.dir()
	if dir == "" {
		return "", errors.New("there is no folder for the log")
	}
	// Worked out before anything is opened: a broken source of secrets must not
	// leave a file open.
	entry := l.format(level, where, message, detail)
	writeMu.Lock()
	defer writeMu.Unlock()

	if err := prepare(dir); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := l.rotate(root); err != nil {
		return "", err
	}
	// The root refuses a file name that is a link leading out of the folder.
	file, err := root.OpenFile(fileName, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return "", err
	}
	_, writeErr := file.WriteString(entry)
	if closeErr := file.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return "", writeErr
	}
	return filepath.Join(dir, fileName), nil
}

// prepare makes the folder, private to the user, and refuses one that others
// can change.
func prepare(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("the place for the log is not a folder")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o022 != 0 {
		return errors.New("the folder of the log can be changed by other users")
	}
	return nil
}

// rotate keeps the file from growing without end: one file of history is kept.
func (l *Log) rotate(root *os.Root) error {
	info, err := root.Stat(fileName)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case info.Size() <= l.maxBytes:
		return nil
	}
	_ = root.Remove(fileName + ".1")
	return root.Rename(fileName, fileName+".1")
}

// format writes an entry: one line that says what happened, and the details
// under it, each indented. Secrets are taken out of all of it.
func (l *Log) format(level, where, message, detail string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s: %s\n", l.now().UTC().Format(time.RFC3339), level, where, oneLine(message))
	if strings.TrimSpace(detail) != "" {
		for _, line := range strings.Split(strings.TrimRight(detail, "\n"), "\n") {
			b.WriteString("\t" + line + "\n")
		}
	}
	out := secret.Redact(b.String(), l.secrets()...)
	if len(out) > maxEntryBytes {
		out = out[:maxEntryBytes]
		for !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
		out += "\n\t[cut: the entry was too long]\n"
	}
	return out
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }
