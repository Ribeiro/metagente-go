package serve

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// WatchTokenFile reads the token file again whenever it changes, until the context ends,
// so that a token is added, changed or taken away without stopping the server. It looks
// at the file every so often: its size, its time, its permissions, and whether it is
// still the same file (a secret of a container is replaced, not written over).
//
// What it reads goes to apply. A file that cannot be read, or holds something that is not
// good enough, changes nothing: the tokens the server had still open it, and say tells
// why, once for each new problem that lasts more than a moment; such a file is read again
// at each look until it is good. say never hears a token, only names.
func WatchTokenFile(ctx context.Context, path string, since os.FileInfo, every time.Duration, apply func([]Credential) error, say func(string)) {
	last := since
	told := ""
	// A problem is told only once it has lasted: a file is caught in the middle of a change more
	// often than one would think (half written, being renamed over, or on Windows held for a
	// moment by whoever removes it or by an antivirus), and that says nothing about the file.
	settle := max(2*every, minSettle)
	pending, pendingSince := "", time.Time{}
	problem := func(text string) {
		now := time.Now()
		if text != pending {
			pending, pendingSince = text, now
		}
		if now.Sub(pendingSince) >= settle && text != told {
			told = text
			say("Problem: " + text + "; the tokens it had still open the server.")
		}
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		info, err := os.Stat(path)
		if err != nil {
			last = nil
			// The same words ReadTokenFile uses when the file goes away between the two looks.
			problem(fmt.Sprintf("I could not open the token file %s: %v", path, unwrapPathError(err)))
			continue
		}
		if sameState(last, info) {
			pending = ""
			continue
		}
		creds, note, err := ReadTokenFile(path)
		if err == nil {
			err = apply(creds)
		}
		if err != nil {
			// Read again at the next look, in case it was caught in the middle of a change.
			problem(err.Error())
			continue
		}
		last, told, pending = info, "", ""
		say(fmt.Sprintf("The token file %s changed: %s.", path, DescribeTokens(creds)))
		if note != "" {
			say("Note: " + note + ".")
		}
	}
}

// minSettle is the least time a problem with the token file has to last before it is told.
const minSettle = 500 * time.Millisecond

func sameState(a, b os.FileInfo) bool {
	return a != nil && os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
}

// DescribeTokens says how many tokens open the server and whose they are.
func DescribeTokens(creds []Credential) string {
	names := TokenNames(creds)
	if len(names) == 0 {
		return "1 token, without a name"
	}
	word := "tokens"
	if len(names) == 1 {
		word = "token"
	}
	return fmt.Sprintf("%d %s, of %s", len(names), word, strings.Join(names, ", "))
}
