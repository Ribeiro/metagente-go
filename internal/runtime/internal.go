package runtime

import (
	"runtime/debug"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

// secretValues are the secrets that must never reach the log: the keys and the
// tokens, read from the variables the configuration names.
func (rt *Runtime) secretValues() []string {
	var values []string
	for _, name := range rt.Config.HiddenEnv() {
		if v := strings.TrimSpace(rt.Getenv(name)); v != "" {
			values = append(values, v)
		}
	}
	return values
}

// internalFailure is what a panic that was caught becomes. The error has one
// plain sentence, because it may be shown to a remote caller or to a model; the
// cause and the stack go to the log. It must be called from the function that
// recovered, or the stack is not the one of the failure.
func (rt *Runtime) internalFailure(where string, recovered any) *diag.Diagnostic {
	d := diag.New("Something went wrong inside Metagente. This is not a mistake in your agent.")
	if path, err := rt.Log.Panic(where, recovered, debug.Stack()); err == nil {
		// A related line is shown to the person at the terminal and left out of
		// what a remote caller or a model reads.
		d.AddRelated("The details were saved in " + path)
	}
	return d
}

// ForgetConversation drops the memory a conversation made, once it is over.
func (rt *Runtime) ForgetConversation(contextID string) { rt.states.Forget(contextID) }
