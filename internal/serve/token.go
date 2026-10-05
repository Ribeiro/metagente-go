package serve

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

// The token that opens the server (requirement S1). Whoever has it can do
// everything the served agents can do, so it is long, random, and never repeated
// in a message.

const (
	tokenEnv       = "METAGENTE_TOKEN"
	minTokenLength = 32
	// minTokenKinds is how many different characters a token must have. Whether a
	// token is random cannot be seen, but one that repeats a few characters is not:
	// `metagente token` gives about 30, and 64 hexadecimal digits give 16.
	minTokenKinds = 12
)

// GenerateToken makes a token of 32 random bytes, written so it fits in a header.
func GenerateToken() string {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		// Without randomness nothing safe can be made; stopping is better than
		// serving with a token that can be guessed.
		panic("the system could not give random bytes")
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

// CheckToken says whether a text can be used as the token. The reason never
// repeats the text: it may be the real thing.
func CheckToken(token string) error {
	if len(token) < minTokenLength {
		return errors.New("the token has to be at least 32 characters long")
	}
	for _, c := range token {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~+/=", c)
		if !ok {
			return errors.New("the token may only use letters, digits and the characters - . _ ~ + / =")
		}
	}
	kinds := map[rune]bool{}
	for _, c := range token {
		kinds[c] = true
	}
	if len(kinds) < minTokenKinds {
		return errors.New("the token repeats too few characters to be random")
	}
	return nil
}

// ResolveToken works out the token of a server. It comes from the environment;
// if it is not there, one is made only for a person at a terminal on this same
// computer, because showing a secret anywhere else (a CI log, a container)
// leaves it in a place that outlives the server. Otherwise the server does not
// start.
func ResolveToken(getenv func(string) string, terminal, loopback bool) (token string, generated bool, err error) {
	if value := strings.TrimSpace(getenv(tokenEnv)); value != "" {
		if err := CheckToken(value); err != nil {
			return "", false, errors.New(tokenEnv + ": " + err.Error() + ". `metagente token` makes a good one")
		}
		return value, false, nil
	}
	if terminal && loopback {
		return GenerateToken(), true, nil
	}
	return "", false, errors.New(tokenEnv + " is not set. A server that is not on this computer, or has no terminal to show a token in, " +
		"needs the token to be given: run `metagente token`, keep the result, and set " + tokenEnv + " to it")
}

// maxTokenFile is the most a token file may hold. A token is a line; a larger file is
// not one.
const maxTokenFile = 4 << 10

// ReadTokenFile reads the token of a server from a file, for where a secret is given
// as a file and not as a variable (the secrets of a container, a file that only the
// user of the server can read). The file holds the token and nothing else; spaces
// and line breaks around it are not part of it.
//
// On Linux and macOS a file that others may change is refused: whoever could write it
// could choose the token. A file that others may read is used, and a note says so,
// because the secrets of containers are often made that way. Windows keeps its
// permissions in lists that this does not read. No reason ever repeats what the file
// holds.
func ReadTokenFile(path string) (token string, note string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("I could not open the token file %s: %w", path, unwrapPathError(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", "", fmt.Errorf("I could not read the token file %s: %w", path, unwrapPathError(err))
	}
	if !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("the token file %s is not a plain file", path)
	}
	if runtime.GOOS != "windows" {
		mode := info.Mode().Perm()
		if mode&0o022 != 0 {
			return "", "", fmt.Errorf("others may change the token file %s (its permissions are %04o), so they could choose the token; make it yours alone with chmod 600", path, mode)
		}
		if mode&0o044 != 0 {
			note = fmt.Sprintf("others on this computer may read the token file %s (its permissions are %04o); chmod 600 makes it yours alone", path, mode)
		}
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxTokenFile+1))
	if err != nil {
		return "", "", fmt.Errorf("I could not read the token file %s: %w", path, unwrapPathError(err))
	}
	if len(raw) > maxTokenFile {
		return "", "", fmt.Errorf("the token file %s is larger than a token can be", path)
	}
	token = strings.TrimSpace(string(raw))
	if token == "" {
		return "", "", fmt.Errorf("the token file %s is empty. `metagente token` makes a good token", path)
	}
	if err := CheckToken(token); err != nil {
		return "", "", fmt.Errorf("the token file %s: %v. `metagente token` makes a good one", path, err)
	}
	return token, note, nil
}

// unwrapPathError keeps the reason of an error about a file, without the path, which
// the message already says.
func unwrapPathError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}
