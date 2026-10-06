// Package runtime runs agents: the tree walking interpreter and everything it
// needs to load, check and start an agent file.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Ribeiro/metagente-go/internal/applog"
	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/llm"
	"github.com/Ribeiro/metagente-go/internal/mcp"
	"github.com/Ribeiro/metagente-go/internal/remote"
	"github.com/Ribeiro/metagente-go/internal/tools"
	"github.com/Ribeiro/metagente-go/internal/trust"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// Runtime is what every agent of one process shares.
type Runtime struct {
	Config *config.Config
	// Linker finds the agents that `link` refers to.
	Linker *Linker
	// Trust holds what the person approved for this project (T1, T2).
	Trust *trust.Registry
	// MCP shares the sessions with tool servers.
	MCP *mcp.Pool
	// Remote reaches the agents that run somewhere else.
	Remote *remote.Pool
	// Log keeps the details of failures inside Metagente (requirement P2).
	Log *applog.Log
	// Model answers `think`. It is built from the configuration the first time
	// it is needed; a test may set it first.
	Model llm.Llm
	// Getenv reads the variables of the process; the key of the model comes
	// through it.
	Getenv func(string) string

	modelMu sync.Mutex
	states  *tools.StateStore
}

// New creates a runtime for a configuration.
func New(cfg *config.Config) *Runtime {
	rt := &Runtime{
		Config: cfg,
		Linker: NewLinker(),
		Trust:  trust.NewRegistry(trust.DefaultDir()),
		Getenv: os.Getenv,
		states: tools.NewStateStore(cfg.Limits),
	}
	rt.Log = applog.Default().With(rt.secretValues)
	getenv := func(name string) string { return rt.Getenv(name) }
	rt.Remote = remote.NewPool(remote.Options{
		Allow:       rt.allowRemote,
		MaxResponse: cfg.Limits.MaxHTTPBytes,
		Getenv:      getenv,
	})
	rt.MCP = mcp.NewPool(mcp.Options{
		Root:   cfg.Root,
		Limits: cfg.Limits,
		Hidden: cfg.HiddenEnv(),
		Allow:  rt.allowServer,
		Getenv: getenv,
	})
	return rt
}

// Close ends the programs the runtime started. Call it when the work is done.
func (rt *Runtime) Close() error { return rt.MCP.Close() }

// Call travels with every call, so cycles can be found and calls can be told
// apart.
type Call struct {
	// TaskID identifies the call.
	TaskID string
	// Chain lists the agents running in this call, outermost first.
	Chain []string
}

// Entering returns the call as seen from inside an agent.
func (c *Call) Entering(agent string) *Call {
	chain := make([]string, len(c.Chain), len(c.Chain)+1)
	copy(chain, c.Chain)
	return &Call{TaskID: c.TaskID, Chain: append(chain, agent)}
}

// Agent is a running agent: its definition, what it may use, and the shared
// runtime.
type Agent struct {
	Def   *lang.AgentDef
	RT    *Runtime
	Caps  *lang.Capabilities
	Tools tools.Registry
}

// NewAgent prepares an agent. contextID tells conversations apart: calls with
// the same one share the memory of `tool state`.
func NewAgent(rt *Runtime, def *lang.AgentDef, contextID string) (*Agent, error) {
	registry, err := tools.Build(def, tools.Options{
		Root:           rt.Config.Root,
		Limits:         rt.Config.Limits,
		HiddenEnv:      rt.Config.HiddenEnv(),
		MaxWaitSeconds: rt.Config.Runtime.MaxWaitSeconds,
		States:         rt.states,
		ContextID:      contextID,
	})
	if err != nil {
		return nil, err
	}
	// Links are resolved when they are called, so a linked agent can change
	// while the agent that calls it keeps running.
	for _, link := range def.Links {
		registry[link.Name] = &linkTool{rt: rt, caller: def, decl: link}
	}
	for _, decl := range def.Remotes {
		spec := remote.Spec{Name: decl.Name, URL: decl.URL, Credential: rt.Config.Credentials[decl.Name]}
		registry[decl.Name] = &remoteTool{rt: rt, inner: rt.Remote.Tool(decl.Name, spec)}
	}
	for _, decl := range def.Tools {
		if decl.Kind == lang.ToolMCP {
			spec := mcp.Spec{Command: decl.Command, Env: decl.MCPEnv}
			if lang.IsURL(decl.Command) {
				spec.Credential = rt.Config.Credentials[decl.Name] // only an address gets a token
			}
			var inner tools.Tool
			if decl.ReadOnly {
				inner = rt.MCP.ReadOnlyTool(decl.Name, spec)
			} else {
				inner = rt.MCP.Tool(decl.Name, spec)
			}
			registry[decl.Name] = &serverTool{inner: inner}
		}
	}
	return &Agent{Def: def, RT: rt, Caps: lang.CapabilitiesOf(def), Tools: registry}, nil
}

// env holds the values the handler has kept so far.
type env struct {
	vars map[string]value.Value
}

// Handle runs the handler of a message, after checking the message against the
// interface of the agent.
func (a *Agent) Handle(ctx context.Context, call *Call, message string, args tools.Args) (value.Value, error) {
	if d := CheckMessage(a.Def, message, args); d != nil {
		return value.Nothing, d
	}
	handler := a.Def.FindHandler(message)
	if handler == nil {
		return value.Nothing, a.diag(a.Def.Span, fmt.Sprintf("agent %s accepts `%s` but has no `on %s` section", a.Def.Name, message, message)).
			Fixf("add an `on %s` section with the lines to run", message)
	}
	scope := &env{vars: map[string]value.Value{}}
	for key, v := range args {
		scope.vars[key] = v
	}
	reply, replied, err := a.execBlock(ctx, handler.Body, scope, call.Entering(a.Def.Name))
	if err != nil {
		return value.Nothing, err
	}
	if replied {
		return reply, nil
	}
	return value.Nothing, nil
}

// Start runs the optional `on start` section.
func (a *Agent) Start(ctx context.Context, call *Call) error {
	if a.Def.Start == nil {
		return nil
	}
	scope := &env{vars: map[string]value.Value{}}
	_, _, err := a.execBlock(ctx, a.Def.Start.Body, scope, call.Entering(a.Def.Name))
	return err
}

func (a *Agent) diag(span lang.Span, message string) *diag.Diagnostic {
	return diag.New(message).At(a.Def.Source.Name, span.Line, span.Col).WithSource(a.Def.Source.Text)
}

// located gives a problem that has no place yet the line where it happened.
func (a *Agent) located(err error, span lang.Span) error {
	if d, ok := diag.From(err); ok {
		return d.Located(a.Def.Source.Name, span.Line, span.Col, a.Def.Source.Text)
	}
	return a.diag(span, "something went wrong while running this line").
		Fix("try again; if it keeps happening, the cause is outside the agent")
}

// execBlock runs statements. It returns the value of a `reply` as soon as one
// happens, with replied set to true.
func (a *Agent) execBlock(ctx context.Context, stmts []lang.Stmt, scope *env, call *Call) (value.Value, bool, error) {
	for _, stmt := range stmts {
		if v, replied, err := a.execStmt(ctx, stmt, scope, call); err != nil || replied {
			return v, replied, err
		}
	}
	return value.Nothing, false, nil
}

// execStmt runs one statement. Like execBlock it says whether the statement was a `reply`.
func (a *Agent) execStmt(ctx context.Context, stmt lang.Stmt, scope *env, call *Call) (value.Value, bool, error) {
	switch s := stmt.(type) {
	case *lang.AssignStmt:
		return a.execAssign(ctx, s, scope, call)
	case *lang.ExprStmt:
		_, err := a.eval(ctx, s.Expr, scope, call)
		return value.Nothing, false, err
	case *lang.ReplyStmt:
		return a.execReply(ctx, s, scope, call)
	case *lang.IfStmt:
		return a.execIf(ctx, s, scope, call)
	case *lang.ForStmt:
		return a.execFor(ctx, s, scope, call)
	case *lang.FailStmt:
		return a.execFail(ctx, s, scope, call)
	}
	return value.Nothing, false, nil
}

func (a *Agent) execAssign(ctx context.Context, s *lang.AssignStmt, scope *env, call *Call) (value.Value, bool, error) {
	v, err := a.eval(ctx, s.Value, scope, call)
	if err != nil {
		return value.Nothing, false, err
	}
	scope.vars[s.Name] = v
	return value.Nothing, false, nil
}

func (a *Agent) execReply(ctx context.Context, s *lang.ReplyStmt, scope *env, call *Call) (value.Value, bool, error) {
	v, err := a.eval(ctx, s.Value, scope, call)
	if err != nil {
		return value.Nothing, false, err
	}
	return v, true, nil
}

// execIf runs the branch that the condition chooses.
func (a *Agent) execIf(ctx context.Context, s *lang.IfStmt, scope *env, call *Call) (value.Value, bool, error) {
	cond, err := a.eval(ctx, s.Cond, scope, call)
	if err != nil {
		return value.Nothing, false, err
	}
	branch := s.Otherwise
	if cond.Truthy() {
		branch = s.Then
	}
	return a.execBlock(ctx, branch, scope, call)
}

// execFor runs the body once for each item of a list, and stops at a `reply`.
func (a *Agent) execFor(ctx context.Context, s *lang.ForStmt, scope *env, call *Call) (value.Value, bool, error) {
	list, err := a.eval(ctx, s.Iter, scope, call)
	if err != nil {
		return value.Nothing, false, err
	}
	if list.Kind != value.KindList {
		return value.Nothing, false, a.diag(s.Span, fmt.Sprintf("`for` needs a list, but it got %s", list.Describe())).
			Fix("give it a list such as [1, 2, 3], or a value that holds a list")
	}
	for _, item := range list.List {
		scope.vars[s.Var] = item
		if v, replied, err := a.execBlock(ctx, s.Body, scope, call); err != nil || replied {
			return v, replied, err
		}
	}
	return value.Nothing, false, nil
}

func (a *Agent) execFail(ctx context.Context, s *lang.FailStmt, scope *env, call *Call) (value.Value, bool, error) {
	message, err := a.eval(ctx, s.Value, scope, call)
	if err != nil {
		return value.Nothing, false, err
	}
	return value.Nothing, false, a.diag(s.Span, message.Display())
}

func (a *Agent) eval(ctx context.Context, expr lang.Expr, scope *env, call *Call) (value.Value, error) {
	switch e := expr.(type) {
	case *lang.TextExpr:
		return a.evalText(ctx, e, scope, call)
	case *lang.NumberExpr:
		return value.Number(e.Value), nil
	case *lang.BoolExpr:
		return value.Bool(e.Value), nil
	case *lang.NothingExpr:
		return value.Nothing, nil
	case *lang.ListExpr:
		return a.evalList(ctx, e, scope, call)
	case *lang.PathExpr:
		return a.evalPath(ctx, e.Path, e.Span, scope, call)
	case *lang.CallExpr:
		return a.evalCall(ctx, e, scope, call)
	case *lang.ThinkExpr:
		return a.evalThink(ctx, e, scope, call)
	case *lang.NotExpr:
		return a.evalNot(ctx, e, scope, call)
	case *lang.AndExpr:
		return a.evalAnd(ctx, e, scope, call)
	case *lang.OrExpr:
		return a.evalOr(ctx, e, scope, call)
	case *lang.CompareExpr:
		return a.evalCompare(ctx, e, scope, call)
	}
	return value.Nothing, a.diag(expr.Pos(), "I do not know how to run this").
		Fix("this is a mistake in Metagente, not in your agent")
}

// evalText joins the pieces of a text, with the values that it names.
func (a *Agent) evalText(ctx context.Context, e *lang.TextExpr, scope *env, call *Call) (value.Value, error) {
	var b strings.Builder
	for _, part := range e.Parts {
		if !part.IsVar {
			b.WriteString(part.Lit)
			continue
		}
		v, err := a.evalPath(ctx, part.Var, e.Span, scope, call)
		if err != nil {
			return value.Nothing, err
		}
		b.WriteString(v.Display())
	}
	return value.Text(b.String()), nil
}

func (a *Agent) evalList(ctx context.Context, e *lang.ListExpr, scope *env, call *Call) (value.Value, error) {
	items := make([]value.Value, 0, len(e.Items))
	for _, item := range e.Items {
		v, err := a.eval(ctx, item, scope, call)
		if err != nil {
			return value.Nothing, err
		}
		items = append(items, v)
	}
	return value.List(items), nil
}

func (a *Agent) evalNot(ctx context.Context, e *lang.NotExpr, scope *env, call *Call) (value.Value, error) {
	v, err := a.eval(ctx, e.Inner, scope, call)
	if err != nil {
		return value.Nothing, err
	}
	return value.Bool(!v.Truthy()), nil
}

// evalAnd does not look at the right side when the left one is already false.
func (a *Agent) evalAnd(ctx context.Context, e *lang.AndExpr, scope *env, call *Call) (value.Value, error) {
	left, err := a.eval(ctx, e.Left, scope, call)
	if err != nil || !left.Truthy() {
		return value.Bool(false), err
	}
	right, err := a.eval(ctx, e.Right, scope, call)
	if err != nil {
		return value.Nothing, err
	}
	return value.Bool(right.Truthy()), nil
}

// evalOr does not look at the right side when the left one is already true.
func (a *Agent) evalOr(ctx context.Context, e *lang.OrExpr, scope *env, call *Call) (value.Value, error) {
	left, err := a.eval(ctx, e.Left, scope, call)
	if err != nil {
		return value.Nothing, err
	}
	if left.Truthy() {
		return value.Bool(true), nil
	}
	right, err := a.eval(ctx, e.Right, scope, call)
	if err != nil {
		return value.Nothing, err
	}
	return value.Bool(right.Truthy()), nil
}

func (a *Agent) evalCompare(ctx context.Context, e *lang.CompareExpr, scope *env, call *Call) (value.Value, error) {
	left, err := a.eval(ctx, e.Left, scope, call)
	if err != nil {
		return value.Nothing, err
	}
	right, err := a.eval(ctx, e.Right, scope, call)
	if err != nil {
		return value.Nothing, err
	}
	return a.compare(e, left, right)
}

// evalPath reads a name, with its fields. A declared tool used without values,
// like `clock.now`, is a call.
func (a *Agent) evalPath(ctx context.Context, path []string, span lang.Span, scope *env, call *Call) (value.Value, error) {
	first := path[0]
	if current, ok := scope.vars[first]; ok {
		return a.fieldsOf(current, path, span)
	}
	if a.Caps.Allows(first) || lang.IsBuiltinName(first) {
		return a.evalToolName(ctx, path, span, call)
	}
	return value.Nothing, a.unknownName(first, span, scope)
}

// fieldsOf follows the fields of a path from a value that is already known: `result.city.name`.
func (a *Agent) fieldsOf(current value.Value, path []string, span lang.Span) (value.Value, error) {
	for i := 1; i < len(path); i++ {
		if current.Kind != value.KindRecord {
			return value.Nothing, a.diag(span,
				fmt.Sprintf("`%s` is %s, so it has no field `%s`", strings.Join(path[:i], "."), current.Describe(), path[i])).
				Fix("only records (like results from tools) have fields")
		}
		next, ok := current.Record[path[i]]
		if !ok {
			return value.Nothing, a.missingField(current, path, i, span)
		}
		current = next
	}
	return current, nil
}

// missingField is the problem of a record that does not have the field that was asked for.
func (a *Agent) missingField(current value.Value, path []string, i int, span lang.Span) *diag.Diagnostic {
	d := a.diag(span, fmt.Sprintf("`%s` has no field called `%s`", strings.Join(path[:i], "."), path[i]))
	fields := current.FieldNames()
	if s := lang.ClosestName(path[i], fields); s != "" {
		d.Fixf("did you mean `%s.%s`?", strings.Join(path[:i], "."), s)
	} else {
		d.Fixf("it has: %s", strings.Join(fields, ", "))
	}
	return d
}

// evalToolName is a tool named where a value was expected: with an action it is a call, without
// one it is a mistake that says what to do.
func (a *Agent) evalToolName(ctx context.Context, path []string, span lang.Span, call *Call) (value.Value, error) {
	first := path[0]
	if len(path) >= 2 {
		synthetic := &lang.CallExpr{Span: span, Target: first, Action: strings.Join(path[1:], ".")}
		return a.evalCall(ctx, synthetic, &env{vars: map[string]value.Value{}}, call)
	}
	if d := a.Caps.Check(first); d != nil {
		return value.Nothing, d.Located(a.Def.Source.Name, span.Line, span.Col, a.Def.Source.Text)
	}
	return value.Nothing, a.diag(span, fmt.Sprintf("`%s` is a tool, not a value", first)).
		Fixf("call one of its actions, for example: %s.action", first)
}

// unknownName is the problem of a name that is neither a value nor a tool, with the closest
// name that does exist.
func (a *Agent) unknownName(name string, span lang.Span, scope *env) *diag.Diagnostic {
	names := make([]string, 0, len(scope.vars))
	for n := range scope.vars {
		names = append(names, n)
	}
	sort.Strings(names)
	names = append(names, a.Caps.DeclaredNames()...)
	d := a.diag(span, fmt.Sprintf("I do not know what `%s` is here", name))
	if s := lang.ClosestName(name, names); s != "" {
		d.Fixf("did you mean `%s`?", s)
	} else {
		d.Fix("give it a value first, for example: name = \"...\"  or list it after `accepts` at the top")
	}
	return d
}

// evalCall runs `target.action key: value`. The agent may only call what it
// declared, and the call may take only as long as `within` allows.
func (a *Agent) evalCall(ctx context.Context, c *lang.CallExpr, scope *env, call *Call) (value.Value, error) {
	if d := a.Caps.Check(c.Target); d != nil {
		return value.Nothing, d.Located(a.Def.Source.Name, c.Span.Line, c.Span.Col, a.Def.Source.Text)
	}
	args, err := a.evalArgs(ctx, c, scope, call)
	if err != nil {
		return value.Nothing, err
	}
	tool, ok := a.Tools[c.Target]
	if !ok {
		return value.Nothing, a.diag(c.Span, fmt.Sprintf("`%s` is declared but could not be set up", c.Target))
	}
	seconds, err := a.timeAllowed(c)
	if err != nil {
		return value.Nothing, err
	}

	callCtx, cancel := context.WithTimeout(withCall(ctx, call), time.Duration(seconds*float64(time.Second)))
	defer cancel()
	v, err := a.runTool(callCtx, tool, c, args)
	if err != nil {
		return value.Nothing, a.callFailure(c, seconds, err)
	}
	return v, nil
}

// evalArgs evaluates the values that a call gives.
func (a *Agent) evalArgs(ctx context.Context, c *lang.CallExpr, scope *env, call *Call) (tools.Args, error) {
	args := tools.Args{}
	for _, arg := range c.Args {
		v, err := a.eval(ctx, arg.Value, scope, call)
		if err != nil {
			return nil, err
		}
		args[arg.Key] = v
	}
	return args, nil
}

// timeAllowed is how many seconds a call may take: the setting of the runtime, or the `within`
// of the line, and never more than the setup allows.
func (a *Agent) timeAllowed(c *lang.CallExpr) (float64, error) {
	seconds := float64(a.RT.Config.Runtime.TimeoutSeconds)
	if c.HasWithin {
		seconds = c.Within
	}
	if limit := a.RT.Config.Runtime.MaxWaitSeconds; seconds > float64(limit) {
		return 0, a.diag(c.Span,
			fmt.Sprintf("`within %s seconds` is more than the %d seconds this setup allows", trimNumber(seconds), limit)).
			Fix("use a shorter time, or raise max_wait_seconds in the [runtime] section of metagente.toml")
	}
	if seconds < 0.001 {
		seconds = 0.001
	}
	return seconds, nil
}

// runTool calls the tool in a goroutine of its own and stops waiting when the time is up. A panic
// of the tool is caught there, where the stack of the failure still is, and goes to the log.
func (a *Agent) runTool(callCtx context.Context, tool tools.Tool, c *lang.CallExpr, args tools.Args) (value.Value, error) {
	type outcome struct {
		v   value.Value
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- outcome{err: a.RT.internalFailure("tool call "+c.Target+"."+c.Action, r)}
			}
		}()
		v, err := tool.Call(callCtx, c.Action, args)
		done <- outcome{v, err}
	}()

	var out outcome
	select {
	case out = <-done:
	case <-callCtx.Done():
		out = outcome{err: callCtx.Err()}
	}
	// A tool that gives up because time ran out may answer at the very moment
	// the deadline fires. Either way the cause is the time, so say so.
	if out.err != nil && callCtx.Err() != nil {
		out.err = callCtx.Err()
	}
	return out.v, out.err
}

// callFailure is the problem for a call that did not work: the time ran out, the run was stopped,
// or the tool said why.
func (a *Agent) callFailure(c *lang.CallExpr, seconds float64, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return a.diag(c.Span,
			fmt.Sprintf("`%s.%s` did not finish within %s seconds", c.Target, c.Action, trimNumber(seconds))).
			Fix("try again, or allow more time by ending the line with: within 60 seconds")
	case errors.Is(err, context.Canceled):
		return a.diag(c.Span, "the run was stopped before this line finished")
	}
	return a.located(err, c.Span)
}

func trimNumber(n float64) string {
	if n == float64(int64(n)) {
		return strconv.FormatInt(int64(n), 10)
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

// compare applies `is`, `is not`, `is more than`, `is less than` and
// `contains`. A text and a number are equal when the text reads as that
// number, so "5" from the command line is 5.
func (a *Agent) compare(e *lang.CompareExpr, left, right value.Value) (value.Value, error) {
	switch e.Op {
	case lang.CompareIs:
		return value.Bool(valuesEqual(left, right)), nil
	case lang.CompareIsNot:
		return value.Bool(!valuesEqual(left, right)), nil
	case lang.CompareMoreThan, lang.CompareLessThan:
		return a.compareOrder(e, left, right)
	case lang.CompareContains:
		return a.compareContains(e, left, right)
	}
	return value.Nothing, a.diag(e.Span, "I do not know this comparison")
}

// valuesEqual says whether two values are the same. A text that reads as a number equals that number.
func valuesEqual(x, y value.Value) bool {
	switch {
	case x.Kind == value.KindNumber && y.Kind == value.KindNumber:
		return x.Number == y.Number
	case x.Kind == value.KindText && y.Kind == value.KindNumber:
		n, ok := x.AsNumber()
		return ok && n == y.Number
	case x.Kind == value.KindNumber && y.Kind == value.KindText:
		n, ok := y.AsNumber()
		return ok && n == x.Number
	}
	return value.Equal(x, y)
}

// compareOrder is `is more than` and `is less than`, which only numbers understand.
func (a *Agent) compareOrder(e *lang.CompareExpr, left, right value.Value) (value.Value, error) {
	x, okX := left.AsNumber()
	y, okY := right.AsNumber()
	if !okX || !okY {
		return value.Nothing, a.diag(e.Span,
			fmt.Sprintf("I can only compare numbers, but I got %s and %s", left.Describe(), right.Describe())).
			Fix("compare two numbers, for example: count is more than 3")
	}
	if e.Op == lang.CompareMoreThan {
		return value.Bool(x > y), nil
	}
	return value.Bool(x < y), nil
}

// compareContains is `contains`: a part of a text, or an item of a list.
func (a *Agent) compareContains(e *lang.CompareExpr, left, right value.Value) (value.Value, error) {
	switch left.Kind {
	case value.KindText:
		return value.Bool(strings.Contains(left.Text, right.Display())), nil
	case value.KindList:
		for _, item := range left.List {
			if valuesEqual(item, right) {
				return value.Bool(true), nil
			}
		}
		return value.Bool(false), nil
	}
	return value.Nothing, a.diag(e.Span,
		fmt.Sprintf("`contains` needs text or a list on its left, but it got %s", left.Describe())).
		Fix("use it like: message contains \"hello\"")
}

// CheckMessage checks a message against the public interface (`accepts`) of an
// agent before it runs: the message must exist, and the values must be exactly
// the ones it takes. It returns nil when all is well.
func CheckMessage(def *lang.AgentDef, message string, args tools.Args) *diag.Diagnostic {
	accept := def.FindAccept(message)
	if accept == nil {
		return unknownMessage(def, message)
	}
	if d := missingValue(def, accept, message, args); d != nil {
		return d
	}
	return unexpectedValue(def, accept, message, args)
}

// unknownMessage is the problem of a message that the agent does not accept, with the closest one.
func unknownMessage(def *lang.AgentDef, message string) *diag.Diagnostic {
	known := make([]string, len(def.Accepts))
	for i, a := range def.Accepts {
		known[i] = a.Message
	}
	d := diag.Newf("agent %s does not accept the message `%s`", def.Name, message)
	switch {
	case len(known) == 0:
		d.Fixf("add a line like `accepts %s` to agent %s", message, def.Name)
	case lang.ClosestName(message, known) != "":
		d.Fixf("did you mean `%s`? %s accepts: %s", lang.ClosestName(message, known), def.Name, strings.Join(known, ", "))
	default:
		d.Fixf("%s accepts: %s", def.Name, strings.Join(known, ", "))
	}
	return d
}

// missingValue is the problem of a value that the message takes and was not given.
func missingValue(def *lang.AgentDef, accept *lang.Accept, message string, args tools.Args) *diag.Diagnostic {
	for _, param := range accept.Params {
		if _, ok := args[param]; !ok {
			return diag.Newf("the message `%s` of agent %s needs a value for `%s`", message, def.Name, param).
				Fixf("send it like: %s %s: \"...\"", message, param)
		}
	}
	return nil
}

// unexpectedValue is the problem of a value that the message does not take. The values are looked at
// in order, so the one that is reported does not change from one run to the next.
func unexpectedValue(def *lang.AgentDef, accept *lang.Accept, message string, args tools.Args) *diag.Diagnostic {
	keys := make([]string, 0, len(args))
	for key := range args {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if hasParam(accept.Params, key) {
			continue
		}
		d := diag.Newf("the message `%s` of agent %s does not take `%s`", message, def.Name, key)
		switch {
		case lang.ClosestName(key, accept.Params) != "":
			d.Fixf("did you mean `%s`?", lang.ClosestName(key, accept.Params))
		case len(accept.Params) == 0:
			d.Fixf("`%s` takes no values", message)
		default:
			d.Fixf("`%s` takes: %s", message, strings.Join(accept.Params, ", "))
		}
		return d
	}
	return nil
}

func hasParam(params []string, name string) bool {
	for _, param := range params {
		if param == name {
			return true
		}
	}
	return false
}
