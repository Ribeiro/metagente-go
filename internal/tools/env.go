package tools

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/value"
)

// Env is `tool env "NAME" ...`: it reads only the named environment
// variables, and never the ones that hold secrets (requirement E6).
type Env struct {
	decl   *lang.ToolDecl
	hidden []string
}

// NewEnv creates the tool. hidden lists the variables that can never be read.
func NewEnv(decl *lang.ToolDecl, hidden []string) *Env {
	return &Env{decl: decl, hidden: hidden}
}

func (e *Env) Name() string { return e.decl.Name }

func (e *Env) Actions(context.Context) ([]lang.ActionInfo, error) {
	return lang.BuiltinActions(e.decl), nil
}

func (e *Env) Call(_ context.Context, action string, args Args) (value.Value, error) {
	if action != "get" {
		return value.Nothing, UnknownAction("env", action, []string{"get"})
	}
	name, err := NeedText("env", "get", args, "name")
	if err != nil {
		return value.Nothing, err
	}
	if slices.Contains(e.hidden, name) {
		return value.Nothing, diag.Newf("`%s` is a secret (such as the key of the language model), and agents can never read it", name).
			Fix("use a different variable for your own values")
	}
	if !slices.Contains(e.decl.EnvNames, name) {
		all := append(slices.Clone(e.decl.EnvNames), name)
		quoted := make([]string, len(all))
		for i, n := range all {
			quoted[i] = fmt.Sprintf("%q", n)
		}
		return value.Nothing, diag.Newf("this agent did not declare the variable `%s`", name).
			Fixf("change the declaration to: tool env %s", strings.Join(quoted, " "))
	}
	if v, ok := os.LookupEnv(name); ok {
		return value.Text(v), nil
	}
	return value.Nothing, nil
}
