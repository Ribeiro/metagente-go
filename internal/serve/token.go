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

// maxTokenFile is the most a token file may hold: enough for many named tokens and
// their comments; a larger file is not a token file.
const maxTokenFile = 16 << 10

// maxTokenName is the longest name of a token. It is shorter than any token can be,
// so a line written the other way around (the token first) is never taken for a name
// and written in the log.
const maxTokenName = 24

// Credential is a token that opens the server, and the name of the client that has it.
// The token of METAGENTE_TOKEN, or of a file that holds just one token, has no name.
type Credential struct {
	Name  string
	Token string
}

// CheckTokenName says whether a text can be the name of a token: what the log writes for
// the client that used it.
func CheckTokenName(name string) error {
	if name == "" || len(name) > maxTokenName {
		return fmt.Errorf("a name of a token has 1 to %d characters", maxTokenName)
	}
	for _, c := range name {
		ok := c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
		if !ok {
			return errors.New("a name of a token may only use lowercase letters, digits and the characters - _ .")
		}
	}
	return nil
}

// ParseTokens reads what a token file holds. It is either one token alone, or a line
// for each client, its name and its token:
//
//	# who may call this server
//	mac       0Jx...
//	notebook  q7T...
//
// Blank lines and lines that start with # are left out. Every token has to pass
// CheckToken, and no name or token may be on two lines: a token on two lines would
// leave a doubt about who used it. No reason ever repeats a token; it says the line.
func ParseTokens(text string) ([]Credential, error) {
	type line struct {
		number int
		fields []string
	}
	var lines []line
	for i, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		lines = append(lines, line{i + 1, strings.Fields(raw)})
	}
	if len(lines) == 0 {
		return nil, errors.New("it holds no token. `metagente token` makes a good one")
	}
	if len(lines) == 1 && len(lines[0].fields) == 1 {
		if err := CheckToken(lines[0].fields[0]); err != nil {
			return nil, fmt.Errorf("%v. `metagente token` makes a good one", err)
		}
		return []Credential{{Token: lines[0].fields[0]}}, nil
	}
	creds := make([]Credential, 0, len(lines))
	names, tokens := map[string]int{}, map[string]int{}
	for _, l := range lines {
		if len(l.fields) != 2 {
			return nil, fmt.Errorf("line %d: with more than one token, each line is a name and a token, as `metagente token --name mac` writes it", l.number)
		}
		name, token := l.fields[0], l.fields[1]
		if err := CheckTokenName(name); err != nil {
			return nil, fmt.Errorf("line %d: %v", l.number, err)
		}
		if err := CheckToken(token); err != nil {
			return nil, fmt.Errorf("line %d (%s): %v. `metagente token` makes a good one", l.number, name, err)
		}
		if first, ok := names[name]; ok {
			return nil, fmt.Errorf("lines %d and %d have the same name, %s", first, l.number, name)
		}
		if first, ok := tokens[token]; ok {
			return nil, fmt.Errorf("lines %d and %d have the same token; each client needs its own", first, l.number)
		}
		names[name], tokens[token] = l.number, l.number
		creds = append(creds, Credential{Name: name, Token: token})
	}
	return creds, nil
}

// checkCredentials is what ParseTokens checks, for tokens that come from elsewhere: each
// one good enough, and no name or token twice.
func checkCredentials(creds []Credential) error {
	if len(creds) == 0 {
		return errors.New("the server needs a token")
	}
	names, tokens := map[string]bool{}, map[string]bool{}
	for _, c := range creds {
		if err := CheckToken(c.Token); err != nil {
			return err
		}
		if c.Name != "" {
			if err := CheckTokenName(c.Name); err != nil {
				return err
			}
			if names[c.Name] {
				return fmt.Errorf("two tokens have the name %s", c.Name)
			}
			names[c.Name] = true
		}
		if tokens[c.Token] {
			return errors.New("two clients have the same token")
		}
		tokens[c.Token] = true
	}
	return nil
}

// TokenNames are the names of the tokens, for a message: never the tokens.
func TokenNames(creds []Credential) []string {
	var names []string
	for _, c := range creds {
		if c.Name != "" {
			names = append(names, c.Name)
		}
	}
	return names
}

// ReadTokenFile reads the tokens of a server from a file, for where a secret is given
// as a file and not as a variable (the secrets of a container, a file that only the
// user of the server can read). What the file may hold is in ParseTokens.
//
// On Linux and macOS a file that others may change is refused: whoever could write it
// could choose the token. A file that others may read is used, and a note says so,
// because the secrets of containers are often made that way. Windows keeps its
// permissions in lists that this does not read. No reason ever repeats what the file
// holds.
func ReadTokenFile(path string) (creds []Credential, note string, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", fmt.Errorf("I could not open the token file %s: %w", path, unwrapPathError(err))
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, "", fmt.Errorf("I could not read the token file %s: %w", path, unwrapPathError(err))
	}
	if !info.Mode().IsRegular() {
		return nil, "", fmt.Errorf("the token file %s is not a plain file", path)
	}
	if runtime.GOOS != "windows" {
		mode := info.Mode().Perm()
		if mode&0o022 != 0 {
			return nil, "", fmt.Errorf("others may change the token file %s (its permissions are %04o), so they could choose the token; make it yours alone with chmod 600", path, mode)
		}
		if mode&0o044 != 0 {
			note = fmt.Sprintf("others on this computer may read the token file %s (its permissions are %04o); chmod 600 makes it yours alone", path, mode)
		}
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxTokenFile+1))
	if err != nil {
		return nil, "", fmt.Errorf("I could not read the token file %s: %w", path, unwrapPathError(err))
	}
	if len(raw) > maxTokenFile {
		return nil, "", fmt.Errorf("the token file %s is larger than a token file can be (%d KiB)", path, maxTokenFile>>10)
	}
	creds, err = ParseTokens(string(raw))
	if err != nil {
		return nil, "", fmt.Errorf("the token file %s: %w", path, err)
	}
	return creds, note, nil
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
