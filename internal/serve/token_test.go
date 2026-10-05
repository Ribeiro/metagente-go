package serve

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAGeneratedTokenIsLongRandomAndFitForAHeader(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		token := GenerateToken()
		if len(token) < 43 {
			t.Fatalf("token of %d characters", len(token))
		}
		if err := CheckToken(token); err != nil {
			t.Fatalf("a generated token is not accepted: %v", err)
		}
		if seen[token] {
			t.Fatal("the same token twice")
		}
		seen[token] = true
	}
}

// req: S1
func TestOnlyALongPlainTokenIsAccepted(t *testing.T) {
	long := "abcdefghijklmnop0123456789ABCDEF"
	for _, good := range []string{long, strings.Repeat("Ab3-_xyZ09~+", 3), "A" + long, strings.Repeat("0123456789abcdef", 4)} {
		if err := CheckToken(good); err != nil {
			t.Errorf("%q was refused: %v", good, err)
		}
	}
	for name, bad := range map[string]string{
		"short":        "tooshort",
		"31":           strings.Repeat("a", 31),
		"space":        long + " x",
		"tab":          long + "\t",
		"newline":      long + "\n",
		"quote":        long + `"`,
		"colon":        long + ":",
		"non ascii":    long + "é",
		"control":      long + "\x01",
		"empty":        "",
		"only spaces":  strings.Repeat(" ", 40),
		"one letter":   strings.Repeat("k", 40),
		"a word again": strings.Repeat("password", 5),
		"two letters":  strings.Repeat("ab", 20),
	} {
		err := CheckToken(bad)
		if err == nil {
			t.Errorf("%s was accepted", name)
			continue
		}
		// A token that is refused is never repeated in the reason.
		if strings.Contains(err.Error(), "tooshort") || (len(bad) > 8 && strings.Contains(err.Error(), bad)) {
			t.Errorf("%s: the reason repeats the token: %v", name, err)
		}
	}
}

// req: S1
func TestTheTokenComesFromTheEnvironmentOrIsGeneratedOnlyWhereItIsSafeToShowIt(t *testing.T) {
	good := "tok-0123456789-abcdefghij-ABCDEFGHIJ"
	for name, tt := range map[string]tokenCase{
		"given":                  {env: good, wantToken: good},
		"given and public":       {env: good, terminal: false, loopback: false, wantToken: good},
		"generated for a person": {terminal: true, loopback: true, wantGen: true},
		"no terminal":            {terminal: false, loopback: true, wantProblem: "METAGENTE_TOKEN"},
		"not loopback":           {terminal: true, loopback: false, wantProblem: "METAGENTE_TOKEN"},
		"neither":                {wantProblem: "METAGENTE_TOKEN"},
		"too short":              {env: "short", terminal: true, loopback: true, wantProblem: "at least 32"},
	} {
		token, generated, err := ResolveToken(environmentWith(tt.env), tt.terminal, tt.loopback)
		tt.check(t, name, token, generated, err)
	}
}

// environmentWith is an environment where only METAGENTE_TOKEN is set.
func environmentWith(value string) func(string) string {
	return func(name string) string {
		if name == "METAGENTE_TOKEN" {
			return value
		}
		return ""
	}
}

// tokenCase is one way to start the server, and what is wanted of it.
type tokenCase struct {
	env         string
	terminal    bool
	loopback    bool
	wantToken   string // "" means a generated one
	wantGen     bool
	wantProblem string
}

// check compares what ResolveToken gave with what the case wants.
func (tt tokenCase) check(t *testing.T, name, token string, generated bool, err error) {
	t.Helper()
	switch {
	case tt.wantProblem != "":
		tt.checkRefused(t, name, token, err)
	case err != nil:
		t.Errorf("%s: unexpected problem: %v", name, err)
	case tt.wantGen && (!generated || CheckToken(token) != nil):
		t.Errorf("%s: token %q, generated %v", name, token, generated)
	case !tt.wantGen && (generated || token != tt.wantToken):
		t.Errorf("%s: token %q, generated %v", name, token, generated)
	}
}

// checkRefused wants a problem that names the cause, no token, and a message that does not repeat the
// token that was given.
func (tt tokenCase) checkRefused(t *testing.T, name, token string, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), tt.wantProblem) {
		t.Errorf("%s: err = %v", name, err)
	}
	if token != "" {
		t.Errorf("%s: a token came with the problem", name)
	}
	if tt.env != "" && strings.Contains(err.Error(), tt.env) && len(tt.env) > 8 {
		t.Errorf("%s: the problem repeats the token", name)
	}
}

func TestAnEmptyOrSpacedVariableIsTreatedAsNotSet(t *testing.T) {
	_, _, err := ResolveToken(func(string) string { return "   " }, false, false)
	if err == nil || !strings.Contains(err.Error(), "METAGENTE_TOKEN") {
		t.Errorf("err = %v", err)
	}
}

// req: S1
func TestATokenFileHoldsTheTokenAndNothingThatIsRefusedIsRepeated(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string, mode os.FileMode) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(text), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, mode); err != nil { // the umask may have taken some bits away
			t.Fatal(err)
		}
		return path
	}
	good := "tok-0123456789-abcdefghij-ABCDEFGHIJ"
	token, note, err := ReadTokenFile(write("good", "  "+good+"\n", 0o600))
	if err != nil || token != good || note != "" {
		t.Errorf("token %q, note %q, err %v", token, note, err)
	}
	secret := "tok-live-short-secret"
	for name, path := range map[string]string{
		"empty":      write("empty", "\n\n", 0o600),
		"short":      write("short", secret+"\n", 0o600),
		"two words":  write("two", secret+" "+good+"\n", 0o600),
		"too large":  write("large", strings.Repeat("k", maxTokenFile+1), 0o600),
		"missing":    filepath.Join(dir, "nothing-here"),
		"not a file": dir,
	} {
		_, _, err := ReadTokenFile(path)
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), good) {
			t.Errorf("%s: the reason repeats what the file holds: %v", name, err)
		}
	}
}

// req: S1
func TestATokenFileThatOthersMayChangeIsRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows keeps the permissions of a file in lists that this does not read")
	}
	good := "tok-0123456789-abcdefghij-ABCDEFGHIJ"
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []os.FileMode{0o620, 0o602, 0o666} {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ReadTokenFile(path); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("%04o: %v", mode, err)
		}
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	token, note, err := ReadTokenFile(path)
	if err != nil || token != good || !strings.Contains(note, "others on this computer may read") {
		t.Errorf("0644: token %q, note %q, err %v", token, note, err)
	}
}
