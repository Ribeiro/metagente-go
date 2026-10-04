// Package tools holds what an agent can call: the built in tools here, and the
// stand-ins for the ones that arrive in later steps (MCP, links, remote agents).
package tools

import (
	"context"
	"fmt"
	"strings"

	"metagente/internal/config"
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/value"
)

// Args are the values of a call, by name.
type Args = map[string]value.Value

// Tool is something an agent can call: `name.action key: value`. A failure is
// reported as a *diag.Diagnostic without a place; the interpreter adds the
// line where it happened.
type Tool interface {
	Name() string
	Actions(ctx context.Context) ([]lang.ActionInfo, error)
	Call(ctx context.Context, action string, args Args) (value.Value, error)
}

// Registry is the tools of one agent by name. Nothing else is reachable from
// the agent.
type Registry map[string]Tool

// Options is what the built in tools need from the runtime.
type Options struct {
	// Root is the project folder.
	Root           string
	Limits         config.Limits
	HiddenEnv      []string
	MaxWaitSeconds int
	States         *StateStore
	// ContextID tells conversations apart, for the memory of `tool state`.
	ContextID string
}

// Build creates the tools an agent declared.
func Build(def *lang.AgentDef, opts Options) (Registry, error) {
	if opts.States == nil {
		opts.States = NewStateStore(opts.Limits)
	}
	registry := Registry{}
	for _, decl := range def.Tools {
		switch decl.Kind {
		case lang.ToolFile:
			registry[decl.Name] = NewFile(opts.Root, decl, opts.Limits)
		case lang.ToolHTTP:
			registry[decl.Name] = NewHTTP(decl, opts.Limits)
		case lang.ToolState:
			registry[decl.Name] = NewState(decl, opts.States.For(def.Name, opts.ContextID))
		case lang.ToolClock:
			registry[decl.Name] = NewClock(decl, opts.MaxWaitSeconds)
		case lang.ToolEnv:
			registry[decl.Name] = NewEnv(decl, opts.HiddenEnv)
		case lang.ToolMCP:
			registry[decl.Name] = Unavailable(decl.Name, "tool servers (MCP)")
		}
	}
	for _, link := range def.Links {
		registry[link.Name] = Unavailable(link.Name, "links to other agents")
	}
	for _, remote := range def.Remotes {
		registry[remote.Name] = Unavailable(remote.Name, "remote agents")
	}
	return registry, nil
}

// NeedText reads a required value as text.
func NeedText(tool, action string, args Args, key string) (string, error) {
	v, ok := args[key]
	if !ok || v.Kind == value.KindNothing {
		return "", diag.Newf("`%s.%s` needs a value for `%s`", tool, action, key).
			Fixf("add it to the call, for example: %s.%s %s: \"...\"", tool, action, key)
	}
	if v.Kind == value.KindText {
		return v.Text, nil
	}
	return v.Display(), nil
}

// UnknownAction is the error for an action a tool does not have; it lists the
// ones it does.
func UnknownAction(tool, action string, available []string) error {
	fix := fmt.Sprintf("`%s` can do: %s", tool, strings.Join(available, ", "))
	if s := lang.ClosestName(action, available); s != "" {
		fix = fmt.Sprintf("did you mean `%s.%s`? (%s)", tool, s, fix)
	}
	return diag.Newf("`%s` has no action called `%s`", tool, action).Fix(fix)
}

func actionNames(actions []lang.ActionInfo) []string {
	names := make([]string, len(actions))
	for i, a := range actions {
		names[i] = a.Name
	}
	return names
}

// readOnlyError is the refusal of an action that changes things.
func readOnlyError(tool, action string) error {
	return diag.Newf("`%s.%s` is not available because `tool %s` was declared readonly", tool, action, tool).
		Fixf("remove `readonly` from the declaration if this agent really needs it")
}

// unavailable stands in for tools that arrive in later steps, so an agent
// that uses one fails with a clear sentence instead of a crash.
type unavailable struct {
	name string
	what string
}

// Unavailable returns a tool that refuses every call, saying that what it
// stands for does not exist in this build yet.
func Unavailable(name, what string) Tool {
	return &unavailable{name: name, what: what}
}

func (u *unavailable) Name() string { return u.name }

func (u *unavailable) Actions(context.Context) ([]lang.ActionInfo, error) { return nil, nil }

func (u *unavailable) Call(context.Context, string, Args) (value.Value, error) {
	return value.Nothing, diag.Newf("`%s` uses %s, which are not available in this build yet", u.name, u.what).
		Fix("this build has the built in tools only: file, http, env, state and clock")
}
