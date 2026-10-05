package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
