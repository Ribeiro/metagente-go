package serve

import (
	"context"

	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/runtime"
	"github.com/Ribeiro/metagente-go/internal/tools"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// RuntimeAgent serves an agent of the interpreter.
type RuntimeAgent struct {
	rt  *runtime.Runtime
	def *lang.AgentDef
}

// NewRuntimeAgent serves the agent of a definition.
func NewRuntimeAgent(rt *runtime.Runtime, def *lang.AgentDef) *RuntimeAgent {
	return &RuntimeAgent{rt: rt, def: def}
}

func (a *RuntimeAgent) Name() string { return a.def.Name }

func (a *RuntimeAgent) Goal() string {
	if a.def.Goal == nil {
		return ""
	}
	return a.def.Goal.Text
}

func (a *RuntimeAgent) Skills() []Skill {
	skills := make([]Skill, len(a.def.Accepts))
	for i, accept := range a.def.Accepts {
		skills[i] = Skill{ID: accept.Message, Description: accept.Description, Params: accept.Params}
	}
	return skills
}

// Begin builds the agent of a conversation, with the memory of that conversation
// only, and runs its `on start` section.
func (a *RuntimeAgent) Begin(ctx context.Context, id string, call Call) (Conversation, error) {
	agent, err := runtime.NewAgent(a.rt, a.def, id)
	if err != nil {
		a.rt.ForgetConversation(id)
		return nil, err
	}
	if err := agent.Start(ctx, &runtime.Call{TaskID: call.ID, Chain: call.Chain}); err != nil {
		a.rt.ForgetConversation(id)
		return nil, err
	}
	return &runtimeConversation{rt: a.rt, agent: agent, id: id}, nil
}

type runtimeConversation struct {
	rt    *runtime.Runtime
	agent *runtime.Agent
	id    string
}

func (c *runtimeConversation) Run(ctx context.Context, call Call, skill string, args map[string]value.Value) (value.Value, error) {
	return c.agent.Handle(ctx, &runtime.Call{TaskID: call.ID, Chain: call.Chain}, skill, tools.Args(args))
}

func (c *runtimeConversation) Close() { c.rt.ForgetConversation(c.id) }
