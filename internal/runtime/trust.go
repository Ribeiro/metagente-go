package runtime

import (
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/llm"
	"metagente/internal/mcp"
	"metagente/internal/remote"
	"metagente/internal/trust"
)

// Needs lists what the agents, and the agents they link to, start or connect
// to. It follows the links as far as they can be resolved; a link that cannot
// be resolved is reported by the check, not here.
func (rt *Runtime) Needs(agents []*lang.AgentDef) []trust.Item {
	w := &needsWalk{rt: rt, seen: map[*lang.AgentDef]bool{}}
	w.visit(agents)
	// The key of the model goes to the address in the configuration. When that
	// is not the one the provider uses by itself, the person approves it too.
	if w.thinks {
		if address, ok := llm.NonDefaultBase(rt.Config.LLM); ok {
			w.lists = append(w.lists, []trust.Item{trust.ModelItem(address)})
		}
	}
	return trust.Merge(w.lists...)
}

// needsWalk follows the links of some agents, once for each agent, and collects what they need.
type needsWalk struct {
	rt     *Runtime
	seen   map[*lang.AgentDef]bool
	lists  [][]trust.Item
	thinks bool
}

func (w *needsWalk) visit(list []*lang.AgentDef) {
	var fresh []*lang.AgentDef
	for _, agent := range list {
		if w.seen[agent] {
			continue
		}
		w.seen[agent] = true
		fresh = append(fresh, agent)
		w.thinks = w.thinks || lang.UsesThink(agent)
	}
	w.lists = append(w.lists, trust.NeedsOfWith(fresh, w.rt.Config.Credentials))
	for _, agent := range fresh {
		for _, decl := range agent.Links {
			target, err := w.rt.Linker.Resolve(w.rt.Config.Root, agent, decl.Name, decl.Path, decl.HasPath)
			if err != nil {
				continue
			}
			w.visit([]*lang.AgentDef{target})
		}
	}
}

// model returns the language model of this runtime, building it the first time.
// The address of the model is checked against the approvals again here, because
// the key is about to be sent there.
func (rt *Runtime) model() (llm.Llm, error) {
	rt.modelMu.Lock()
	defer rt.modelMu.Unlock()
	if rt.Model != nil {
		return rt.Model, nil
	}
	if address, ok := llm.NonDefaultBase(rt.Config.LLM); ok {
		item := trust.ModelItem(address)
		missing, err := rt.Trust.Missing(rt.Config.Root, []trust.Item{item})
		if err != nil {
			return nil, err
		}
		if len(missing) > 0 {
			return nil, diag.Newf("the address of the language model, `%s`, has not been approved for this project", address).
				AddRelated(item.Describe()).
				Fix("read where your key would go, and approve it with: metagente trust FILE.ag")
		}
	}
	built, err := llm.FromConfig(rt.Config.LLM, rt.Getenv)
	if err != nil {
		return nil, err
	}
	rt.Model = built
	return built, nil
}

// Authorize makes sure everything the agents start or connect to has been
// approved for this project (requirement T1). What is not approved is shown to
// the person through confirm; without a confirm nothing new is approved, and
// the way to approve is explained.
func (rt *Runtime) Authorize(agents []*lang.AgentDef, file string, confirm func([]trust.Item) bool) error {
	needs := rt.Needs(agents)
	missing, err := rt.Trust.Missing(rt.Config.Root, needs)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	if confirm != nil && confirm(missing) {
		return rt.Trust.Approve(rt.Config.Root, needs)
	}
	d := diag.New("this agent starts programs or connects to addresses that you have not approved for this project")
	for _, line := range trust.Explain(missing) {
		d.AddRelated(line)
	}
	return d.Fixf("read the lines above, and if you trust them run: metagente trust %s", file)
}

// allowServer is the check the pool makes before it starts or reaches a tool
// server. It also covers what appeared after the run began, for example in an
// agent that was linked and then edited.
func (rt *Runtime) allowServer(spec mcp.Spec) error {
	item := trust.CommandItem(spec.Command, spec.Env)
	if lang.IsURL(spec.Command) {
		item = trust.RemoteItem(spec.Command).WithCredential(spec.Credential)
	}
	missing, err := rt.Trust.Missing(rt.Config.Root, []trust.Item{item})
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	return diag.Newf("the tool server `%s` has not been approved for this project", spec.Command).
		AddRelated(item.Describe()).
		Fix("read what it does, and approve it with: metagente trust FILE.ag")
}

// allowRemote is the check the pool of remote agents makes before it reaches an
// address.
func (rt *Runtime) allowRemote(spec remote.Spec) error {
	item := trust.RemoteItem(spec.URL).WithCredential(spec.Credential)
	missing, err := rt.Trust.Missing(rt.Config.Root, []trust.Item{item})
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	return diag.Newf("the remote agent %s, at %s, has not been approved for this project", spec.Name, spec.URL).
		AddRelated(item.Describe()).
		Fix("read where it connects to, and approve it with: metagente trust FILE.ag")
}
