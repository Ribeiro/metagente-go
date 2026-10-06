package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"metagente/internal/serve"
)

// req: S1
func TestServeTakesTheTokenFromAFile(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(testToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	live := startServe(t, []string{"hello.ag", "--port", "0", "--token-file", tokenFile}, nil, false)
	if live.token != testToken {
		t.Errorf("the server took another token than the one of the file")
	}
	if code, answer := live.do(t, "POST", "/agents/Hello", greet("Ana"), testToken); code != 200 || !strings.Contains(answer, "Hello, Ana!") {
		t.Errorf("the token of the file does not open the door: %d %s", code, answer)
	}
	_, stderr := live.stop(t)
	if strings.Contains(stderr, testToken) || strings.Contains(stderr, "Token for this run") {
		t.Errorf("the token was shown:\n%s", stderr)
	}
}

// req: S1
func TestServeRefusesATokenFileAndTheVariableTogether(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte(testToken), 0o600); err != nil {
		t.Fatal(err)
	}
	var errOut bytes.Buffer
	code := serveCommand(context.Background(), []string{"hello.ag", "--port", "0", "--token-file", tokenFile}, io.Discard, &errOut,
		serveEnv{getenv: func(name string) string { return map[string]string{"METAGENTE_TOKEN": testToken}[name] }})
	if code != 2 || !strings.Contains(errOut.String(), "both --token-file and METAGENTE_TOKEN") {
		t.Errorf("exit %d\n%s", code, errOut.String())
	}
	if strings.Contains(errOut.String(), testToken) {
		t.Errorf("the token was repeated:\n%s", errOut.String())
	}
}

// eventually waits for a thing that the server does on its own time.
func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("%s did not happen in time", what)
}

// req: S1, P5
func TestEachClientHasItsOwnTokenAndTheFileIsReadAgainWhenItChanges(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	mac, notebook, phone := serve.GenerateToken(), serve.GenerateToken(), serve.GenerateToken()
	tokenFile := filepath.Join(t.TempDir(), "tokens")
	write := func(text string) {
		t.Helper()
		// Through a new file and a rename, as a secret of a container is replaced, or in place.
		if err := os.WriteFile(tokenFile+".new", []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := renameOver(tokenFile+".new", tokenFile); err != nil {
			t.Fatal(err)
		}
	}
	write("# who may call\nmac " + mac + "\nnotebook " + notebook + "\n")
	live := startServe(t, []string{"hello.ag", "--port", "0", "--token-file", tokenFile}, nil, false)
	opens := func(token string) bool {
		code, _ := live.do(t, "POST", "/agents/Hello", greet("Ana"), token)
		return code == 200
	}
	if !opens(mac) || !opens(notebook) {
		t.Fatal("a token of the file does not open the door")
	}
	if !strings.Contains(live.stderr.String(), "client=mac") || !strings.Contains(live.stderr.String(), "client=notebook") {
		t.Errorf("the log does not say which client called:\n%s", live.stderr.String())
	}

	// The notebook is taken away, a phone comes: the mac goes on as it was.
	write("mac " + mac + "\nphone " + phone + "\n")
	eventually(t, "taking the token of the notebook away", func() bool { return !opens(notebook) })
	if !opens(mac) || !opens(phone) {
		t.Error("after the change, the mac or the phone is not let in")
	}

	// A file that is not good enough changes nothing.
	write("mac " + mac + "\nphone short\n")
	eventually(t, "the problem with the file", func() bool { return strings.Contains(live.stderr.String(), "Problem:") })
	if !opens(mac) || !opens(phone) {
		t.Error("a bad file took tokens away")
	}

	_, stderr := live.stop(t)
	for _, want := range []string{"Tokens from " + tokenFile + ": 2 tokens, of mac, notebook.", "changed: 2 tokens, of mac, phone.",
		"line 2 (phone)", "the tokens it had still open the server"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not say %q:\n%s", want, stderr)
		}
	}
	for _, token := range []string{mac, notebook, phone} {
		if strings.Contains(stderr, token) {
			t.Errorf("a token was shown:\n%s", stderr)
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
