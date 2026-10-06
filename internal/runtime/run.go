package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/tools"
	"github.com/Ribeiro/metagente-go/internal/trust"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// LoadAgents reads and parses a .ag file.
func LoadAgents(path string) ([]*lang.AgentDef, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		why := err.Error()
		if os.IsNotExist(err) {
			why = "the file does not exist"
		}
		return nil, diag.Newf("I could not open %s: %s", path, why).
			Fix("check the file name and the folder you are in")
	}
	return lang.ParseFile(filepath.Base(path), path, string(text))
}

// Options is what `metagente run` was asked to do.
type Options struct {
	File    string
	Message string
	Params  []string
	Agent   string
	// Confirm asks the person whether to approve what is not approved yet. When
	// it is nil, nothing new is approved (requirement T1).
	Confirm func(missing []trust.Item) bool
}

// ParseParams reads the `key=value` values of the command line. Every value
// is a text; a text that reads as a number compares as one.
func ParseParams(params []string) (tools.Args, error) {
	args := tools.Args{}
	for _, p := range params {
		key, text, ok := strings.Cut(p, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, diag.Newf("`%s` is not a key=value pair", p).
				Fix("write values like: city=Lisbon")
		}
		args[strings.TrimSpace(key)] = value.Text(text)
	}
	return args, nil
}

// PickAgent chooses an agent of a file: the one asked for, or the first.
func PickAgent(agents []*lang.AgentDef, wanted, file string) (*lang.AgentDef, error) {
	if wanted != "" {
		names := make([]string, len(agents))
		for i, a := range agents {
			if a.Name == wanted {
				return a, nil
			}
			names[i] = a.Name
		}
		return nil, diag.Newf("%s has no agent called `%s`", filepath.Base(file), wanted).
			Fixf("it has: %s", strings.Join(names, ", "))
	}
	if len(agents) == 0 {
		return nil, diag.New("this file has no agent in it").Fix("start with a line like: agent Helper")
	}
	return agents[0], nil
}

var taskCounter atomic.Uint64

// NewTaskID makes an identifier that is unique in this process.
func NewTaskID() string {
	return fmt.Sprintf("task-%x-%d", time.Now().UnixNano(), taskCounter.Add(1))
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// RunFile runs an agent file from start to finish: it checks the file, picks
// the agent and the message, runs `on start` and then the handler.
func RunFile(ctx context.Context, rt *Runtime, opts Options) (value.Value, error) {
	agents, err := LoadAgents(opts.File)
	if err != nil {
		return value.Nothing, err
	}
	rt.Linker.Register(agents)
	if err := CheckAgents(rt, agents, opts.File); err != nil {
		return value.Nothing, err
	}
	if err := rt.Authorize(agents, opts.File, opts.Confirm); err != nil {
		return value.Nothing, err
	}
	def, err := PickAgent(agents, opts.Agent, opts.File)
	if err != nil {
		return value.Nothing, err
	}
	message, params, err := chooseMessage(def, opts)
	if err != nil {
		return value.Nothing, err
	}
	args, err := ParseParams(params)
	if err != nil {
		return value.Nothing, err
	}
	return runAgent(ctx, rt, def, message, args)
}

// CheckAgents checks the agents of a file, and the links between them. It returns the first problem,
// with a note of how many more there are in the file, or nil when there is none.
func CheckAgents(rt *Runtime, agents []*lang.AgentDef, file string) error {
	problems := lang.Check(agents).Problems
	problems = append(problems, CheckLinks(rt.Linker, rt.Config.Root, agents)...)
	diag.SortByPlace(problems)
	if len(problems) == 0 {
		return nil
	}
	first := problems[0]
	if extra := len(problems) - 1; extra > 0 {
		first.AddRelated(fmt.Sprintf("(%d more %s in this file; run `metagente check %s` to see them all)",
			extra, plural(extra, "problem"), file))
	}
	return first
}

// chooseMessage works out the message to send and the `key=value` words that go with it. In
// `run file.ag question=...` there is no message: the first word is a value, and the agent has to
// accept only one message.
func chooseMessage(def *lang.AgentDef, opts Options) (string, []string, error) {
	message, params := opts.Message, opts.Params
	if strings.Contains(message, "=") {
		params = append([]string{message}, params...)
		message = ""
	}
	if message != "" {
		return message, params, nil
	}
	if len(def.Accepts) != 1 {
		return "", nil, needsMessage(def, opts.File)
	}
	return def.Accepts[0].Message, params, nil
}

// needsMessage is the problem of an agent that accepts several messages and was not told which.
func needsMessage(def *lang.AgentDef, file string) error {
	names := make([]string, len(def.Accepts))
	for i, a := range def.Accepts {
		names[i] = a.Message
	}
	example := "message"
	if len(names) > 0 {
		example = names[0]
	}
	return diag.Newf("agent %s needs to be told what to do", def.Name).
		Fixf("add the message after the file name, for example: metagente run %s %s   (it accepts: %s)",
			file, example, strings.Join(names, ", "))
}

// runAgent starts the agent and sends it the message.
func runAgent(ctx context.Context, rt *Runtime, def *lang.AgentDef, message string, args tools.Args) (value.Value, error) {
	taskID := NewTaskID()
	agent, err := NewAgent(rt, def, taskID)
	if err != nil {
		return value.Nothing, err
	}
	call := &Call{TaskID: taskID}
	if err := agent.Start(ctx, call); err != nil {
		return value.Nothing, err
	}
	return agent.Handle(ctx, call, message, args)
}

// ShowValue says how a reply reads on the terminal: nothing prints nothing, a
// text prints as it is, anything else prints as JSON.
func ShowValue(v value.Value) (string, bool) {
	switch v.Kind {
	case value.KindNothing:
		return "", false
	case value.KindText:
		return v.Text, true
	}
	pretty, err := json.MarshalIndent(v.ToJSON(), "", "  ")
	if err != nil {
		return v.Display(), true
	}
	return string(pretty), true
}
