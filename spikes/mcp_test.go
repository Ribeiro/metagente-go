package spikes

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The tests start this very test binary as the MCP server, so no other program
// is needed. When the variable below is set, the binary is the server.
const helperEnv = "METAGENTE_SPIKE_MCP_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(helperEnv) == "1" {
		if err := newSpikeServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
			fmt.Fprintln(os.Stderr, "spike server:", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

type slowArgs struct {
	Millis int `json:"millis" jsonschema:"how long to wait, in milliseconds"`
}

type textArgs struct {
	Text string `json:"text" jsonschema:"text to send back"`
}

type nameArgs struct {
	Name string `json:"name" jsonschema:"name of an environment variable"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func newSpikeServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "spike", Version: "v0.0.1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "slow", Description: "wait, then answer"},
		func(ctx context.Context, _ *mcp.CallToolRequest, in slowArgs) (*mcp.CallToolResult, any, error) {
			select {
			case <-time.After(time.Duration(in.Millis) * time.Millisecond):
			case <-ctx.Done():
			}
			return text("done"), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "send the text back"},
		func(_ context.Context, _ *mcp.CallToolRequest, in textArgs) (*mcp.CallToolResult, any, error) {
			return text(in.Text), nil, nil
		})
	mcp.AddTool(server, &mcp.Tool{Name: "env", Description: "read an environment variable of this process"},
		func(_ context.Context, _ *mcp.CallToolRequest, in nameArgs) (*mcp.CallToolResult, any, error) {
			return text(os.Getenv(in.Name)), nil, nil
		})
	return server
}

// connectStdio starts the helper as a child process with ONLY the environment
// given here (requirement E1) and connects to it over stdio.
func connectStdio(t *testing.T, extraEnv ...string) (*mcp.ClientSession, *exec.Cmd) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append([]string{helperEnv + "=1"}, extraEnv...)
	if runtime.GOOS == "windows" {
		cmd.Env = append(cmd.Env, "SYSTEMROOT="+os.Getenv("SYSTEMROOT"))
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "spike-client", Version: "v0.0.1"}, nil)
	transport := &mcp.CommandTransport{Command: cmd, TerminateDuration: 5 * time.Second}
	session, err := client.Connect(context.Background(), transport, nil)
	if err != nil {
		t.Fatalf("could not connect to the child process: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, cmd
}

func callText(t *testing.T, session *mcp.ClientSession, tool string, args map[string]any) string {
	t.Helper()
	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if res.IsError {
		t.Fatalf("%s returned a tool error: %v", tool, res.Content)
	}
	if len(res.Content) == 0 {
		return ""
	}
	content, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("%s: unexpected content type %T", tool, res.Content[0])
	}
	return content.Text
}

// V1 / E3: can several calls run at the same time on ONE session?
// If this fails, a pool of sessions is needed instead of one shared session.
func TestV1ConcurrentCallsOnOneStdioSession(t *testing.T) {
	session, _ := connectStdio(t)
	const calls = 8
	const delayMillis = 400

	start := time.Now()
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for i := 0; i < calls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{
				Name: "slow", Arguments: map[string]any{"millis": delayMillis}})
			switch {
			case err != nil:
				errs <- err
			case res.IsError:
				errs <- fmt.Errorf("tool error: %v", res.Content)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	elapsed := time.Since(start)
	serial := time.Duration(calls*delayMillis) * time.Millisecond
	t.Logf("%d calls of %d ms took %v (one after the other would take about %v)", calls, delayMillis, elapsed, serial)
	if elapsed > serial/2 {
		t.Errorf("the calls look serialised on one session: %v is more than half of %v", elapsed, serial)
	}
}

// E1: the child must see only what we give it, never the parent's secrets.
func TestE1TheChildSeesOnlyTheEnvironmentWeGiveIt(t *testing.T) {
	t.Setenv("METAGENTE_SPIKE_SECRET", "must-not-leak")
	session, _ := connectStdio(t, "METAGENTE_SPIKE_ALLOWED=yes")

	if got := callText(t, session, "env", map[string]any{"name": "METAGENTE_SPIKE_ALLOWED"}); got != "yes" {
		t.Errorf("the variable we passed on arrived as %q", got)
	}
	if got := callText(t, session, "env", map[string]any{"name": "METAGENTE_SPIKE_SECRET"}); got != "" {
		t.Errorf("the child inherited a variable of the parent: %q", got)
	}
}

// E4: closing the session must end the child, in bounded time.
func TestE4ClosingTheSessionEndsTheChild(t *testing.T) {
	session, cmd := connectStdio(t)
	start := time.Now()
	err := session.Close()
	took := time.Since(start)
	t.Logf("Close returned %v after %v", err, took)
	if took > 5*time.Second {
		t.Errorf("Close took %v", took)
	}
	if cmd.ProcessState == nil {
		t.Error("the child process was never waited for, so it may still be running")
	}
}

// V6: a large text with newlines, quotes, tabs and unicode crosses stdio intact.
func TestV6LargeMultilineTextOverStdio(t *testing.T) {
	session, _ := connectStdio(t)
	body := strings.Repeat("line one \"quoted\" \\ back\nline two ünïcode ✓\ttab\r\n", 4000)
	t.Logf("sending %d bytes", len(body))
	if got := callText(t, session, "echo", map[string]any{"text": body}); got != body {
		t.Errorf("the text changed on the way: sent %d bytes, got %d", len(body), len(got))
	}
}

// V2: which protocol versions does the SDK speak?
func TestV2TheSDKSupportsTheProtocolVersionsWeNeed(t *testing.T) {
	versions := mcp.SupportedProtocolVersions()
	t.Logf("supported protocol versions: %v", versions)
	for _, want := range []string{"2026-07-28", "2025-11-25", "2025-06-18"} {
		if !slices.Contains(versions, want) {
			t.Errorf("protocol version %s is not supported", want)
		}
	}
}

func TestV2AClientCanAskForAnOlderVersion(t *testing.T) {
	ctx := context.Background()
	t1, t2 := mcp.NewInMemoryTransports()
	if _, err := newSpikeServer().Connect(ctx, t1, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "spike-client", Version: "v0.0.1"}, nil)
	session, err := client.Connect(ctx, t2, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
	if err != nil {
		t.Fatalf("connecting with the older version failed: %v", err)
	}
	defer session.Close()
	if got := callText(t, session, "echo", map[string]any{"text": "hi"}); got != "hi" {
		t.Errorf("echo returned %q", got)
	}
}
