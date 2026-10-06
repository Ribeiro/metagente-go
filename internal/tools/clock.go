package tools

import (
	"context"
	"math"
	"time"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// Clock is `tool clock`: the current time, and waiting.
type Clock struct {
	decl           *lang.ToolDecl
	maxWaitSeconds int
}

// NewClock creates the tool. Waiting is limited to maxWaitSeconds
// (requirement D1).
func NewClock(decl *lang.ToolDecl, maxWaitSeconds int) *Clock {
	return &Clock{decl: decl, maxWaitSeconds: maxWaitSeconds}
}

func (c *Clock) Name() string { return c.decl.Name }

func (c *Clock) Actions(context.Context) ([]lang.ActionInfo, error) {
	return lang.BuiltinActions(c.decl), nil
}

func (c *Clock) Call(ctx context.Context, action string, args Args) (value.Value, error) {
	switch action {
	case "now":
		now := time.Now().UTC()
		return value.Record(map[string]value.Value{
			"text": value.Text(now.Format(time.RFC3339Nano)),
			"unix": value.Number(float64(now.Unix())),
		}), nil
	case "wait":
		seconds, ok := args["seconds"].AsNumber()
		if !ok || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
			return value.Nothing, diag.New("`clock.wait` needs a number of seconds").
				Fix("write it like: clock.wait seconds: 5")
		}
		if seconds < 0 || seconds > float64(c.maxWaitSeconds) {
			return value.Nothing, diag.Newf("`clock.wait` needs between 0 and %d seconds", c.maxWaitSeconds).
				Fix("use a smaller number of seconds, or raise max_wait_seconds in the [runtime] section of metagente.toml")
		}
		timer := time.NewTimer(time.Duration(seconds * float64(time.Second)))
		defer timer.Stop()
		select {
		case <-timer.C:
			return value.Nothing, nil
		case <-ctx.Done():
			return value.Nothing, ctx.Err()
		}
	}
	return value.Nothing, UnknownAction("clock", action, []string{"now", "wait"})
}
