//go:build acceptance

package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rogpeppe/go-internal/testscript"

	"metagente/internal/cli"
)

// TestMain makes the test binary answer to the command name `metagente`, so
// scripts can run `exec metagente ...` without a separate build step.
func TestMain(m *testing.M) {
	os.Exit(testscript.RunMain(m, map[string]func() int{
		"metagente": cli.Main,
		"mcpecho":   mcpEcho,
	}))
}

// mcpEcho is a small tool server for the scripts: `echo` sends the text back
// and `env` reads a variable of its own process.
func mcpEcho() int {
	type textIn struct {
		Text string `json:"text" jsonschema:"text to send back"`
	}
	type nameIn struct {
		Name string `json:"name" jsonschema:"name of an environment variable"`
	}
	answer := func(text string) *sdk.CallToolResult {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "mcpecho", Version: "v0.0.1"}, nil)
	sdk.AddTool(server, &sdk.Tool{Name: "echo", Description: "send the text back"},
		func(_ context.Context, _ *sdk.CallToolRequest, in textIn) (*sdk.CallToolResult, any, error) {
			return answer("echo: " + in.Text), nil, nil
		})
	sdk.AddTool(server, &sdk.Tool{Name: "env", Description: "read a variable of this process"},
		func(_ context.Context, _ *sdk.CallToolRequest, in nameIn) (*sdk.CallToolResult, any, error) {
			return answer("value: " + os.Getenv(in.Name)), nil, nil
		})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		return 1
	}
	return 0
}

func TestScripts(t *testing.T) {
	examples, err := filepath.Abs("../../testdata/examples")
	if err != nil {
		t.Fatal(err)
	}
	testscript.Run(t, testscript.Params{
		Dir: "../../testdata/script",
		Setup: func(env *testscript.Env) error {
			env.Setenv("EXAMPLES", examples)
			// The approvals of a script never touch the real ones, and they live
			// outside the work folder because the project is where the script runs.
			config, err := os.MkdirTemp("", "metagente-config-")
			if err != nil {
				return err
			}
			env.Defer(func() { _ = os.RemoveAll(config) })
			env.Setenv("METAGENTE_CONFIG_DIR", config)
			state, err := os.MkdirTemp("", "metagente-state-")
			if err != nil {
				return err
			}
			env.Defer(func() { _ = os.RemoveAll(state) })
			env.Setenv("METAGENTE_STATE_DIR", filepath.Join(state, "state"))
			return nil
		},
	})
}
