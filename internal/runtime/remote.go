package runtime

import (
	"context"

	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/remote"
	"metagente/internal/tools"
	"metagente/internal/value"
)

// remoteTool is an agent that runs somewhere else, used like a tool. It adds
// two things to the client: it refuses to go deeper than the setup allows, and
// it tells the other side which agents are already running, so the limit
// holds across processes (requirement D2).
type remoteTool struct {
	rt    *Runtime
	inner tools.Tool
}

func (t *remoteTool) Name() string { return t.inner.Name() }

func (t *remoteTool) Actions(ctx context.Context) ([]lang.ActionInfo, error) {
	return t.inner.Actions(ctx)
}

func (t *remoteTool) Call(ctx context.Context, action string, args tools.Args) (value.Value, error) {
	call := callFrom(ctx)
	if limit := t.rt.Config.Runtime.MaxCallDepth; limit > 0 && len(call.Chain) >= limit {
		return value.Nothing, diag.Newf("the agents are calling each other %d levels deep, which is the most this setup allows", limit).
			Fix("check that they are not calling each other in a circle, or raise max_call_depth in the [runtime] section of metagente.toml")
	}
	return t.inner.Call(remote.WithTrail(ctx, call.Chain), action, args)
}
