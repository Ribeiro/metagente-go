package lang

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

// didYouMean is the advice for a name that is close to one that exists.
const didYouMean = "did you mean `%s`?"

// HiddenEnvNames are variables that hold secrets. Tool servers are never
// given them (requirement E1), whatever the agent asks.
var HiddenEnvNames = []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "METAGENTE_TOKEN"}

// Result is everything Check found. Every problem is reported, not just the
// first one. Warnings are advice and never block, unless the caller is strict.
type Result struct {
	Problems []*diag.Diagnostic
	Warnings []*diag.Diagnostic
}

// Check looks at parsed agents without running them: structure, names, tool
// actions, permissions and the declarations of tools. It does not look at
// files on disk.
func Check(agents []*AgentDef) Result {
	var res Result
	seen := map[string]bool{}
	for _, agent := range agents {
		if seen[agent.Name] {
			res.Problems = append(res.Problems,
				atSpan(agent, agent.Span, fmt.Sprintf("there are two agents called %s in this file", agent.Name)).
					Fix("give each agent its own name"))
		}
		seen[agent.Name] = true

		checkPermissions(agent, &res)
		checkStructure(agent, &res)
		checkDeclarations(agent, &res)
		w := &walker{agent: agent, caps: CapabilitiesOf(agent), res: &res}
		w.run()
		checkWarnings(agent, &res)
	}
	diag.SortByPlace(res.Problems)
	diag.SortByPlace(res.Warnings)
	return res
}

func atSpan(agent *AgentDef, span Span, message string) *diag.Diagnostic {
	return diag.New(message).At(agent.Source.Name, span.Line, span.Col).WithSource(agent.Source.Text)
}

func acceptNames(agent *AgentDef) []string {
	names := make([]string, 0, len(agent.Accepts))
	for _, a := range agent.Accepts {
		names = append(names, a.Message)
	}
	return names
}

func checkStructure(agent *AgentDef, res *Result) {
	add := func(d *diag.Diagnostic) { res.Problems = append(res.Problems, d) }

	if agent.Goal == nil {
		add(atSpan(agent, agent.Span, fmt.Sprintf("agent %s has no goal", agent.Name)).
			Fixf("add a line under `agent %s` such as: goal \"what this agent is for\"", agent.Name))
	}
	accepted := checkAccepts(agent, add)
	checkHandlers(agent, accepted, add)
	checkDeclaredNames(agent, add)
}

// checkAccepts looks at the messages an agent accepts: none twice, and each one with a handler. It
// returns the messages, for checkHandlers.
func checkAccepts(agent *AgentDef, add func(*diag.Diagnostic)) map[string]bool {
	accepted := map[string]bool{}
	for _, accept := range agent.Accepts {
		if accepted[accept.Message] {
			add(atSpan(agent, accept.Span, fmt.Sprintf("`accepts %s` appears twice", accept.Message)).
				Fix("remove one of them"))
		}
		accepted[accept.Message] = true
		if agent.FindHandler(accept.Message) == nil {
			add(atSpan(agent, accept.Span,
				fmt.Sprintf("agent %s accepts `%s` but nothing says what to do with it", agent.Name, accept.Message)).
				Fixf("add a section: on %s   (then indent the lines to run under it)", accept.Message))
		}
	}
	return accepted
}

// checkHandlers looks at the `on` sections: none twice, and each one for a message that is accepted.
func checkHandlers(agent *AgentDef, accepted map[string]bool, add func(*diag.Diagnostic)) {
	handled := map[string]bool{}
	for _, handler := range agent.Handlers {
		if handled[handler.Message] {
			add(atSpan(agent, handler.Span, fmt.Sprintf("`on %s` appears twice", handler.Message)).
				Fix("keep one of them"))
		}
		handled[handler.Message] = true
		if !accepted[handler.Message] {
			add(atSpan(agent, handler.Span,
				fmt.Sprintf("`on %s` has no matching `accepts %s` line", handler.Message, handler.Message)).
				Fix(adviceForStrayHandler(agent, handler.Message)))
		}
	}
}

// adviceForStrayHandler is what to do about an `on` section whose message is not accepted: the
// closest message that is, or the line that would accept it.
func adviceForStrayHandler(agent *AgentDef, message string) string {
	if s := ClosestName(message, acceptNames(agent)); s != "" {
		return fmt.Sprintf("did you mean `on %s`? Otherwise add the line: accepts %s", s, message)
	}
	return fmt.Sprintf("add the line `accepts %s` under `agent %s`", message, agent.Name)
}

// checkDeclaredNames looks for a name used for more than one tool, link or remote.
func checkDeclaredNames(agent *AgentDef, add func(*diag.Diagnostic)) {
	var declared []string
	for _, t := range agent.Tools {
		declared = append(declared, t.Name)
	}
	for _, l := range agent.Links {
		declared = append(declared, l.Name)
	}
	for _, r := range agent.Remotes {
		declared = append(declared, r.Name)
	}
	names := map[string]bool{}
	for _, name := range declared {
		if names[name] {
			add(atSpan(agent, agent.Span,
				fmt.Sprintf("the name `%s` is declared more than once in agent %s", name, agent.Name)).
				Fix("use each name for only one tool, link, or remote"))
		}
		names[name] = true
	}
}

// checkDeclarations looks at the clauses of tool declarations: the domains of
// `allow` (requirement H2) and the variables given to tool servers (E1).
func checkDeclarations(agent *AgentDef, res *Result) {
	add := func(d *diag.Diagnostic) { res.Problems = append(res.Problems, d) }
	for _, tool := range agent.Tools {
		switch tool.Kind {
		case ToolHTTP:
			checkAllowedDomains(agent, tool, add)
		case ToolMCP:
			checkServerDeclaration(agent, tool, add)
		}
	}
}

// checkAllowedDomains refuses a domain in `allow` that is not a domain name.
func checkAllowedDomains(agent *AgentDef, tool *ToolDecl, add func(*diag.Diagnostic)) {
	for _, domain := range tool.Allow {
		if !validAllowDomain(domain) {
			add(atSpan(agent, tool.Span,
				fmt.Sprintf("`%s` is not a domain name that `allow` can use", domain)).
				Fix("write a domain such as \"api.example.com\", or \"*.example.com\" for all its subdomains; use `allow private` for private networks"))
		}
	}
}

// checkServerDeclaration looks at a tool server: its command must not be empty, and the variables it
// is given must be names of variables, and none that holds a secret.
func checkServerDeclaration(agent *AgentDef, tool *ToolDecl, add func(*diag.Diagnostic)) {
	if strings.TrimSpace(tool.Command) == "" {
		add(atSpan(agent, tool.Span,
			fmt.Sprintf("the tool server command of `%s` is empty", tool.Name)).
			Fix("write the command in quotes, for example: tool weather from mcp \"npx -y weather-mcp@1.2.3\""))
	}
	for _, name := range tool.MCPEnv {
		switch {
		case !validEnvName(name):
			add(atSpan(agent, tool.Span,
				fmt.Sprintf("`%s` is not a valid environment variable name", name)).
				Fix("use letters, digits and underscores, starting with a letter or underscore"))
		case isHiddenEnv(name):
			add(atSpan(agent, tool.Span,
				fmt.Sprintf("`%s` holds a secret, and secrets can never be passed to a tool server", name)).
				Fix("remove it from the `env` list; give the tool server its own credential instead"))
		}
	}
}

func isHiddenEnv(name string) bool {
	for _, hidden := range HiddenEnvNames {
		if hidden == name {
			return true
		}
	}
	return false
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

// validAllowDomain accepts `example.com` and `*.example.com`. A single label
// like `com`, an address like `10.0.0.1`, and anything with other characters
// are refused.
func validAllowDomain(domain string) bool {
	rest := strings.TrimPrefix(domain, "*.")
	labels := strings.Split(rest, ".")
	if len(labels) < 2 {
		return false
	}
	allNumeric := true
	for _, label := range labels {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		numeric := true
		for _, r := range label {
			switch {
			case r >= '0' && r <= '9':
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '-':
				numeric = false
			default:
				return false
			}
		}
		if !numeric {
			allNumeric = false
		}
	}
	return !allNumeric
}

type walker struct {
	agent *AgentDef
	caps  *Capabilities
	res   *Result
}

func (w *walker) add(d *diag.Diagnostic) {
	w.res.Problems = append(w.res.Problems, d)
}

func (w *walker) at(span Span, message string) *diag.Diagnostic {
	return atSpan(w.agent, span, message)
}

func (w *walker) run() {
	for _, handler := range w.agent.Handlers {
		vars := map[string]bool{}
		if accept := w.agent.FindAccept(handler.Message); accept != nil {
			for _, param := range accept.Params {
				vars[param] = true
			}
		}
		w.stmts(handler.Body, vars)
	}
	if w.agent.Start != nil {
		w.stmts(w.agent.Start.Body, map[string]bool{})
	}
}

func (w *walker) stmts(stmts []Stmt, vars map[string]bool) {
	for _, stmt := range stmts {
		switch s := stmt.(type) {
		case *AssignStmt:
			w.expr(s.Value, vars)
			if w.caps.Allows(s.Name) {
				w.add(w.at(s.Span, fmt.Sprintf("`%s` is already the name of a tool, link, or remote", s.Name)).
					Fix("choose a different name for this value"))
			}
			vars[s.Name] = true
		case *ExprStmt:
			w.expr(s.Expr, vars)
		case *ReplyStmt:
			w.expr(s.Value, vars)
		case *FailStmt:
			w.expr(s.Value, vars)
		case *IfStmt:
			w.expr(s.Cond, vars)
			w.stmts(s.Then, vars)
			w.stmts(s.Otherwise, vars)
		case *ForStmt:
			w.expr(s.Iter, vars)
			vars[s.Var] = true
			w.stmts(s.Body, vars)
		case *RepeatStmt:
			w.expr(s.Cond, vars)
			w.stmts(s.Body, vars)
		}
	}
}

func (w *walker) expr(expr Expr, vars map[string]bool) {
	switch e := expr.(type) {
	case *TextExpr:
		for _, part := range e.Parts {
			if part.IsVar {
				w.path(part.Var, e.Span, vars)
			}
		}
	case *ListExpr:
		for _, item := range e.Items {
			w.expr(item, vars)
		}
	case *PathExpr:
		w.path(e.Path, e.Span, vars)
	case *CallExpr:
		w.call(e, vars)
	case *ThinkExpr:
		w.expr(e.Prompt, vars)
		w.using(e)
	case *NotExpr:
		w.expr(e.Inner, vars)
	case *AndExpr:
		w.expr(e.Left, vars)
		w.expr(e.Right, vars)
	case *OrExpr:
		w.expr(e.Left, vars)
		w.expr(e.Right, vars)
	case *CompareExpr:
		w.expr(e.Left, vars)
		w.expr(e.Right, vars)
	}
}

// using checks that `think ... using a b` names only what the agent declared
// (requirement X2).
func (w *walker) using(think *ThinkExpr) {
	for _, name := range think.Using {
		if w.caps.Allows(name) {
			continue
		}
		d := w.at(think.Span, fmt.Sprintf("`using` names `%s`, but this agent never declared it", name))
		if s := ClosestName(name, w.caps.DeclaredNames()); s != "" {
			d.Fixf(didYouMean, s)
		} else {
			d.Fix("declare it first, with a `tool`, `link` or `remote` line")
		}
		w.add(d)
	}
}

func (w *walker) path(path []string, span Span, vars map[string]bool) {
	first := path[0]
	if vars[first] {
		return
	}
	known := w.caps.Allows(first) || isBuiltinName(first)
	if len(path) >= 2 && known {
		w.call(&CallExpr{Span: span, Target: first, Action: strings.Join(path[1:], ".")}, vars)
		return
	}
	if known {
		return // a bare tool name; the permission pass or the runtime explains it
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	names = append(names, w.caps.DeclaredNames()...)
	d := w.at(span, fmt.Sprintf("I do not know what `%s` is here", first))
	if s := ClosestName(first, names); s != "" {
		d.Fixf(didYouMean, s)
	} else {
		d.Fix("give it a value first, for example: name = \"...\"  or list it after `accepts` at the top")
	}
	w.add(d)
}

func actionNames(actions []ActionInfo) []string {
	names := make([]string, 0, len(actions))
	for _, a := range actions {
		names = append(names, a.Name)
	}
	return names
}

func (w *walker) call(call *CallExpr, vars map[string]bool) {
	for _, arg := range call.Args {
		w.expr(arg.Value, vars)
	}
	tool := w.agent.FindTool(call.Target)
	if tool == nil {
		w.undeclaredTarget(call)
		return
	}
	if tool.Kind == ToolMCP || tool.Kind == ToolSQL {
		return // the actions of these are only known when the server runs, or from metagente.toml
	}

	available := actionNames(BuiltinActions(tool))
	info := findAction(AllBuiltinActions(tool.Kind), call.Action)
	if info == nil {
		w.unknownAction(call, available)
		return
	}
	if tool.ReadOnly && info.Mutates {
		w.readOnlyAction(call, available)
		return
	}
	w.missingValues(call, info)
	w.unexpectedValues(call, info)
}

// undeclaredTarget refuses a call to a name that was never declared, with the same words the
// runtime would use (requirement X1). Built in names are left to the permission pass, and links
// and remotes are declared names.
func (w *walker) undeclaredTarget(call *CallExpr) {
	if w.caps.Allows(call.Target) || isBuiltinName(call.Target) {
		return
	}
	if d := w.caps.Check(call.Target); d != nil {
		w.add(d.Located(w.agent.Source.Name, call.Span.Line, call.Span.Col, w.agent.Source.Text))
	}
}

func findAction(actions []ActionInfo, name string) *ActionInfo {
	for i := range actions {
		if actions[i].Name == name {
			return &actions[i]
		}
	}
	return nil
}

// unknownAction is the problem of an action that the tool does not have, with the closest one.
func (w *walker) unknownAction(call *CallExpr, available []string) {
	fix := fmt.Sprintf("`%s` can do: %s", call.Target, strings.Join(available, ", "))
	if s := ClosestName(call.Action, available); s != "" {
		fix = fmt.Sprintf("did you mean `%s.%s`? (`%s` can do: %s)",
			call.Target, s, call.Target, strings.Join(available, ", "))
	}
	w.add(w.at(call.Span, fmt.Sprintf("`%s` has no action called `%s`", call.Target, call.Action)).Fix(fix))
}

// readOnlyAction is the problem of an action that changes things, on a tool declared `readonly`.
func (w *walker) readOnlyAction(call *CallExpr, available []string) {
	w.add(w.at(call.Span,
		fmt.Sprintf("`%s.%s` is not available because `tool %s` was declared readonly",
			call.Target, call.Action, call.Target)).
		Fixf("remove `readonly` from the declaration if this agent really needs it; the actions it can use are: %s",
			strings.Join(available, ", ")))
}

// missingValues is the problem of each value that the action needs and the call does not give.
func (w *walker) missingValues(call *CallExpr, info *ActionInfo) {
	for _, param := range info.Params {
		if !param.Required || hasArg(call.Args, param.Name) {
			continue
		}
		w.add(w.at(call.Span,
			fmt.Sprintf("`%s.%s` needs a value for `%s`", call.Target, call.Action, param.Name)).
			Fixf("add it to the line, for example: %s.%s %s: \"...\"", call.Target, call.Action, param.Name))
	}
}

// unexpectedValues is the problem of each value that the call gives and the action does not take.
func (w *walker) unexpectedValues(call *CallExpr, info *ActionInfo) {
	known := make([]string, 0, len(info.Params))
	for _, param := range info.Params {
		known = append(known, param.Name)
	}
	for _, arg := range call.Args {
		if containsName(known, arg.Key) {
			continue
		}
		fix := fmt.Sprintf("`%s.%s` takes: %s", call.Target, call.Action, strings.Join(known, ", "))
		if s := ClosestName(arg.Key, known); s != "" {
			fix = fmt.Sprintf(didYouMean, s)
		}
		w.add(w.at(call.Span,
			fmt.Sprintf("`%s.%s` does not take `%s`", call.Target, call.Action, arg.Key)).Fix(fix))
	}
}

func hasArg(args []Arg, key string) bool {
	for _, arg := range args {
		if arg.Key == key {
			return true
		}
	}
	return false
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

// checkPermissions finds uses of built in tools the agent never declared.
func checkPermissions(agent *AgentDef, res *Result) {
	caps := CapabilitiesOf(agent)
	reported := map[string]bool{}
	for _, t := range usedTargets(agent) {
		if !isBuiltinName(t.name) || caps.Allows(t.name) {
			continue
		}
		key := fmt.Sprintf("%s:%d", t.name, t.span.Line)
		if reported[key] {
			continue
		}
		reported[key] = true
		if d := caps.Check(t.name); d != nil {
			res.Problems = append(res.Problems,
				d.Located(agent.Source.Name, t.span.Line, t.span.Col, agent.Source.Text))
		}
	}
}

// usedTarget is a name that a line uses as a tool: the target of a call, or the first part of a path
// or of a hole in a text that has a field after it.
type usedTarget struct {
	name string
	span Span
}

// usedTargets lists the names that the handlers use as tools, in the order they appear.
func usedTargets(agent *AgentDef) []usedTarget {
	var targets []usedTarget
	eachBody(agent, func(stmts []Stmt) {
		visitStmts(stmts, func(expr Expr) {
			targets = append(targets, targetsOf(expr)...)
		})
	})
	return targets
}

func targetsOf(expr Expr) []usedTarget {
	switch e := expr.(type) {
	case *CallExpr:
		return []usedTarget{{e.Target, e.Span}}
	case *PathExpr:
		if len(e.Path) >= 2 {
			return []usedTarget{{e.Path[0], e.Span}}
		}
	case *TextExpr:
		var out []usedTarget
		for _, part := range e.Parts {
			if part.IsVar && len(part.Var) >= 2 {
				out = append(out, usedTarget{part.Var[0], e.Span})
			}
		}
		return out
	}
	return nil
}

// eachBody calls fn with the statements of every handler, and then with those of `on start`.
func eachBody(agent *AgentDef, fn func([]Stmt)) {
	for _, handler := range agent.Handlers {
		fn(handler.Body)
	}
	if agent.Start != nil {
		fn(agent.Start.Body)
	}
}

// checkWarnings adds advice that never blocks: tool servers started without a
// pinned version (requirement E2) and `think` calls that can use tools able to
// change things or send data (requirement L7).
func checkWarnings(agent *AgentDef, res *Result) {
	warnUnpinnedServers(agent, res)
	if risky := riskyTools(agent); len(risky) > 0 {
		warnThinkWithRiskyTools(agent, risky, res)
	}
}

// warnUnpinnedServers warns about a tool server that is started with a package of which no version
// is pinned.
func warnUnpinnedServers(agent *AgentDef, res *Result) {
	for _, tool := range agent.Tools {
		if tool.Kind != ToolMCP || isURL(tool.Command) {
			continue
		}
		if spec, unpinned := unpinnedPackage(tool.Command); unpinned {
			res.Warnings = append(res.Warnings,
				atSpan(agent, tool.Span,
					fmt.Sprintf("the tool server command `%s` does not pin a version of `%s`", tool.Command, spec)).
					Fixf("write the package with a version, for example: %s@1.2.3", strings.TrimSuffix(spec, "@latest")).
					AsWarning())
		}
	}
}

// riskyTools are the tools that can change things or send data: a file that is not read only, and the
// web.
func riskyTools(agent *AgentDef) []string {
	var risky []string
	for _, tool := range agent.Tools {
		if (tool.Kind == ToolFile && !tool.ReadOnly) || tool.Kind == ToolHTTP {
			risky = append(risky, tool.Name)
		}
	}
	return risky
}

// warnThinkWithRiskyTools warns about each `think` that does not limit the tools it may use.
func warnThinkWithRiskyTools(agent *AgentDef, risky []string, res *Result) {
	eachBody(agent, func(stmts []Stmt) {
		visitStmts(stmts, func(expr Expr) {
			think, ok := expr.(*ThinkExpr)
			if !ok || len(think.Using) > 0 {
				return
			}
			res.Warnings = append(res.Warnings,
				atSpan(agent, think.Span,
					fmt.Sprintf("this `think` can use every tool the agent declared, including tools that can change things or send data (%s)",
						strings.Join(risky, ", "))).
					Fix("limit it with `using`, for example: think \"...\" using clock; or declare the tool `readonly`").
					AsWarning())
		})
	})
}
