// Package elt makes the Extractor and the Worker of the asynchronous ELT from a description. An agent
// that says `tool orders from elt "orders" extract` (or `load`) is given, when it is loaded, the tools,
// the messages and the handlers that the description calls for. What comes out is an ordinary agent:
// the checks, `metagente trust`, `run`, `serve` and `consume` see the same things as for an agent
// written by hand, and the statements it calls are the ones that package config made from the
// description (see docs/tutorial-elt.md).
package elt

import (
	"bytes"
	"fmt"
	"reflect"
	"strings"
	"text/template"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
)

// The names that the tools of the generated agents have. An agent that uses a description may not
// declare tools with these names, and the passwords of [credentials] are given under them.
var (
	extractorTools = []string{"source", "outbox", "events", "codec", "clock"}
	workerTools    = []string{"dest", "codec"}
	extractorMsgs  = []string{"extract", "resend"}
	workerMsgs     = []string{"batch", "control", "resume", "retransform", "purge", "purge_control"}
)

// Expand gives each agent of the list what its `tool x from elt "name" role` lines call for. The line
// itself is taken out. An agent that has none is left as it is.
func Expand(agents []*lang.AgentDef, cfg *config.Config) error {
	for _, agent := range agents {
		if err := expandAgent(agent, cfg); err != nil {
			return err
		}
	}
	return nil
}

// Uses says whether an agent has a line that Expand would change.
func Uses(agent *lang.AgentDef) bool {
	for _, decl := range agent.Tools {
		if decl.Kind == lang.ToolELT {
			return true
		}
	}
	return false
}

func expandAgent(agent *lang.AgentDef, cfg *config.Config) error {
	if !Uses(agent) {
		return nil
	}
	var kept []*lang.ToolDecl
	var made []*lang.AgentDef
	for _, decl := range agent.Tools {
		if decl.Kind != lang.ToolELT {
			kept = append(kept, decl)
			continue
		}
		piece, err := piece(agent, decl, cfg)
		if err != nil {
			return err
		}
		made = append(made, piece)
	}
	agent.Tools = kept
	for _, piece := range made {
		if err := merge(agent, piece); err != nil {
			return err
		}
	}
	return nil
}

// problem is a problem with the line of the agent that asked for the description.
func problem(agent *lang.AgentDef, decl *lang.ToolDecl, message string) *diag.Diagnostic {
	d := diag.New(message)
	if agent.Source != nil {
		d = d.At(agent.Source.Name, decl.Span.Line, decl.Span.Col).WithSource(agent.Source.Text)
	}
	return d
}

// piece makes the agent that holds the tools, the messages and the handlers of one line.
func piece(agent *lang.AgentDef, decl *lang.ToolDecl, cfg *config.Config) (*lang.AgentDef, error) {
	e, ok := cfg.ELT[decl.ConnectionName()]
	if !ok {
		return nil, problem(agent, decl, fmt.Sprintf("the tool `%s` uses the description `%s`, and %s has no section [elt.%s]",
			decl.Name, decl.ConnectionName(), config.FileName, decl.ConnectionName())).
			Fixf("add [elt.%s] with the key and the columns of the table (see docs/tutorial-elt.md)", decl.ConnectionName())
	}
	var text string
	switch decl.ELTRole() {
	case "extract":
		if e.Source == nil {
			return nil, problem(agent, decl, fmt.Sprintf("the description [elt.%s] has no source, and this agent extracts", e.Name)).
				Fixf("add [elt.%s.source] with the connection, the table, the outbox and the broker", e.Name)
		}
		text = render(extractorText, e)
	default:
		if e.Destination == nil {
			return nil, problem(agent, decl, fmt.Sprintf("the description [elt.%s] has no destination, and this agent loads", e.Name)).
				Fixf("add [elt.%s.destination] with the connection, the tables and the rules", e.Name)
		}
		text = render(workerText, e)
	}
	made, err := lang.ParseFile(agent.Name, "", text)
	if err != nil || len(made) != 1 {
		// A fault of the template and not of the person: it is told as one.
		return nil, problem(agent, decl, fmt.Sprintf("I made an agent from [elt.%s] that I could not read: %v", e.Name, err)).
			Fix("this is a fault of Metagente; please report it with the description")
	}
	// What was made has no place in the file of the agent: everything in it is said to come from the line
	// that asked for it, which is the one a person can read.
	pointTo(reflect.ValueOf(made[0]), decl.Span)
	return made[0], nil
}

// merge adds the tools, the messages and the handlers of the piece to the agent. A name that the agent
// has already is a problem: the generated agent does not take its place.
func merge(agent, piece *lang.AgentDef) error {
	for _, tool := range piece.Tools {
		if agent.FindTool(tool.Name) != nil {
			return clash(agent, "a tool", tool.Name, "the tools")
		}
		agent.Tools = append(agent.Tools, tool)
	}
	for _, accept := range piece.Accepts {
		if agent.FindAccept(accept.Message) != nil {
			return clash(agent, "a message", accept.Message, "the messages")
		}
		agent.Accepts = append(agent.Accepts, accept)
	}
	for _, handler := range piece.Handlers {
		if agent.FindHandler(handler.Message) != nil {
			return clash(agent, "a handler", handler.Message, "the handlers")
		}
		agent.Handlers = append(agent.Handlers, handler)
	}
	return nil
}

func clash(agent *lang.AgentDef, what, name, plural string) error {
	d := diag.Newf("agent %s has %s called `%s`, and the description makes one with that name", agent.Name, what, name)
	if agent.Source != nil {
		d = d.At(agent.Source.Name, agent.Span.Line, agent.Span.Col).WithSource(agent.Source.Text)
	}
	return d.Fixf("rename yours: the description uses %s %s",
		plural, strings.Join(append(append(append([]string{}, extractorTools...), workerTools...), append(extractorMsgs, workerMsgs...)...), ", "))
}

// view is what the templates are filled with.
type view struct {
	Name        string
	Table       string
	Key         string
	Columns     string // ["id", "customer"]
	Source      string
	Outbox      string
	Broker      string
	Dest        string
	Rows        int
	Bytes       int
	RejectShare string
}

func viewOf(e *config.ELT) view {
	quoted := make([]string, len(e.Columns))
	for i, column := range e.Columns {
		quoted[i] = fmt.Sprintf("%q", column)
	}
	v := view{Name: e.Name, Key: e.Key, Columns: "[" + strings.Join(quoted, ", ") + "]"}
	if e.Source != nil {
		v.Table, v.Source, v.Outbox, v.Broker = e.Source.Table, e.Source.Connection, e.Source.Outbox, e.Source.Broker
		v.Rows, v.Bytes = e.Source.Rows, e.Source.Bytes
	}
	if e.Destination != nil {
		v.Dest = e.Destination.Connection
		v.RejectShare = fmt.Sprintf("%g", e.Destination.RejectShare)
	}
	return v
}

func render(t *template.Template, e *config.ELT) string {
	var out bytes.Buffer
	if err := t.Execute(&out, viewOf(e)); err != nil {
		panic(err) // the templates are fixed, and the view has every field they name
	}
	return out.String()
}

var spanType = reflect.TypeOf(lang.Span{})

// pointTo sets every place in the tree to span.
func pointTo(v reflect.Value, span lang.Span) {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if !v.IsNil() {
			pointTo(v.Elem(), span)
		}
	case reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			pointTo(v.Index(i), span)
		}
	case reflect.Struct:
		if v.Type() == spanType {
			v.Set(reflect.ValueOf(span))
			return
		}
		for i := 0; i < v.NumField(); i++ {
			if v.Field(i).CanSet() {
				pointTo(v.Field(i), span)
			}
		}
	}
}
