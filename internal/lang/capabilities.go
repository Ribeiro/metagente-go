package lang

import (
	"sort"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

// BuiltinNames are the names of the built in tools.
var BuiltinNames = []string{"file", "http", "env", "state", "clock", "codec", "meter"}

// IsBuiltinName reports whether name is one of the built in tools.
func IsBuiltinName(name string) bool { return isBuiltinName(name) }

func isBuiltinName(name string) bool {
	for _, n := range BuiltinNames {
		if n == name {
			return true
		}
	}
	return false
}

// Granted tells how an agent got the right to use a name.
type Granted int

const (
	GrantedBuiltin Granted = iota
	GrantedMCP
	GrantedLink
	GrantedRemote
)

// Capabilities is what an agent may use: exactly what it declared.
type Capabilities struct {
	agent   string
	granted map[string]Granted
}

// CapabilitiesOf builds the capabilities of an agent from its declarations.
func CapabilitiesOf(def *AgentDef) *Capabilities {
	granted := map[string]Granted{}
	for _, tool := range def.Tools {
		if tool.Kind == ToolMCP {
			granted[tool.Name] = GrantedMCP
		} else {
			granted[tool.Name] = GrantedBuiltin
		}
	}
	for _, link := range def.Links {
		granted[link.Name] = GrantedLink
	}
	for _, remote := range def.Remotes {
		granted[remote.Name] = GrantedRemote
	}
	return &Capabilities{agent: def.Name, granted: granted}
}

// Allows reports whether the agent declared target.
func (c *Capabilities) Allows(target string) bool {
	_, ok := c.granted[target]
	return ok
}

// DeclaredNames lists the declared names in alphabetical order.
func (c *Capabilities) DeclaredNames() []string {
	names := make([]string, 0, len(c.granted))
	for name := range c.granted {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Check returns a plain language refusal when the agent did not declare
// target, or nil when it did.
func (c *Capabilities) Check(target string) *diag.Diagnostic {
	if c.Allows(target) {
		return nil
	}
	if isBuiltinName(target) {
		return diag.Newf("agent %s uses `%s` but never declared it", c.agent, target).
			Fixf("add the line `tool %s` under `agent %s`", target, c.agent)
	}
	declared := c.DeclaredNames()
	d := diag.Newf("agent %s does not know anything called `%s`", c.agent, target)
	if len(declared) == 0 {
		return d.Fixf("declare it under `agent %s` with `tool %s from mcp \"command\"`, `link %s`, or `remote %s at \"address\"`",
			c.agent, target, target, target)
	}
	return d.Fixf("use one of the names this agent declared (%s), or declare `%s` under `agent %s`",
		strings.Join(declared, ", "), target, c.agent)
}
