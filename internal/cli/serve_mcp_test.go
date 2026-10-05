package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// bearer sends the token with every request.
type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

// req: S10
func TestServeWithMCPOffersTheAgentsAsToolsOverHTTPBehindTheToken(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	live := startServe(t, []string{"hello.ag", "--port", "0", "--mcp"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &sdk.StreamableClientTransport{
		Endpoint:   "http://" + live.address + "/mcp",
		HTTPClient: &http.Client{Transport: bearer(testToken)},
		MaxRetries: -1,
	}
	session, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, transport, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: "Hello__greet", Arguments: map[string]any{"name": "Ana"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultText(t, result); got != "Hello, Ana!" {
		t.Errorf("Hello__greet = %q", got)
	}
	_ = session.Close()
	if code, _ := live.do(t, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, ""); code != 401 {
		t.Errorf("MCP without the token: %d", code)
	}
	// A2A is still there.
	if code, answer := live.do(t, "POST", "/agents/Hello", greet("Bia"), testToken); code != 200 || !strings.Contains(answer, "Hello, Bia!") {
		t.Errorf("A2A beside MCP: %d %s", code, answer)
	}

	exit, stderr := live.stop(t)
	if exit != 0 {
		t.Errorf("exit code = %d", exit)
	}
	assertContains(t, stderr, "/mcp (the agents as MCP tools)", "agent=Hello rpc=mcp message=greet task=task-", "result=ok")
	if strings.Contains(stderr, testToken) {
		t.Errorf("the token reached the output:\n%s", stderr)
	}
}

func TestServeWithoutMCPHasNoMCP(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	live := startServe(t, []string{"hello.ag", "--port", "0"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)
	if code, _ := live.do(t, "POST", "/mcp", `{"jsonrpc":"2.0","id":1,"method":"initialize"}`, testToken); code != 404 {
		t.Errorf("/mcp without --mcp: %d", code)
	}
	if _, stderr := live.stop(t); strings.Contains(stderr, "MCP") {
		t.Errorf("the banner speaks of MCP:\n%s", stderr)
	}
}
