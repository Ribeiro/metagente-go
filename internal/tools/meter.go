package tools

import (
	"context"
	"sync/atomic"

	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// ModelUse counts what a conversation has asked of the language model: how many requests, and how many tokens
// they cost. The runtime adds to it each time the model answers.
type ModelUse struct {
	calls  atomic.Int64
	tokens atomic.Int64
}

// Add counts one request that cost tokens.
func (u *ModelUse) Add(tokens int) {
	u.calls.Add(1)
	u.tokens.Add(int64(tokens))
}

// Totals gives the requests and the tokens so far.
func (u *ModelUse) Totals() (calls, tokens int64) { return u.calls.Load(), u.tokens.Load() }

// Meter is `tool meter`: it tells how much of the language model this conversation has used. An agent that
// has a budget for the model reads it before and after a `think`, and keeps the difference.
type Meter struct {
	decl *lang.ToolDecl
	use  *ModelUse
}

// NewMeter creates the tool.
func NewMeter(decl *lang.ToolDecl, use *ModelUse) *Meter {
	if use == nil {
		use = &ModelUse{}
	}
	return &Meter{decl: decl, use: use}
}

func (m *Meter) Name() string { return m.decl.Name }

func (m *Meter) Actions(context.Context) ([]lang.ActionInfo, error) {
	return lang.BuiltinActions(m.decl), nil
}

func (m *Meter) Call(_ context.Context, action string, _ Args) (value.Value, error) {
	if action != "model" {
		return value.Nothing, UnknownAction(m.decl.Name, action, []string{"model"})
	}
	calls, tokens := m.use.Totals()
	return value.Record(map[string]value.Value{"calls": value.Number(float64(calls)), "tokens": value.Number(float64(tokens))}), nil
}
