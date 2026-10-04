package serve

import (
	"context"

	"metagente/internal/value"
)

// The server does not know how an agent runs. It knows these three things, and
// the runtime is plugged in behind them; that keeps the protocol and its
// security testable without a language model, a tool or a file.

// Skill is one message an agent accepts.
type Skill struct {
	ID          string
	Description string
	Params      []string
}

// Call says who is calling: the identity of the task, and the agents already
// running in it, outermost first.
type Call struct {
	ID    string
	Chain []string
}

// Agent is an agent that can be served.
type Agent interface {
	Name() string
	Goal() string
	Skills() []Skill
	// Begin starts a conversation. The id is the one the server issued for it.
	Begin(ctx context.Context, id string, call Call) (Conversation, error)
}

// Conversation is the running agent of one conversation, with the state that
// belongs to it.
type Conversation interface {
	Run(ctx context.Context, call Call, skill string, args map[string]value.Value) (value.Value, error)
	// Close lets go of what the conversation holds.
	Close()
}
