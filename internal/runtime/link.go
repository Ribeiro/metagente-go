package runtime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/tools"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// Linker finds the agents that `link` refers to. It loads agent files when a
// call happens, and reads a file again only when it changed on disk, so a
// change to a linked agent applies without restarting the one that calls it.
type Linker struct {
	mu    sync.Mutex
	files map[string]cachedFile
	// registered holds the agents of files that are already loaded, so that
	// `link` can find the siblings of an agent in the same file.
	registered map[string][]*lang.AgentDef
}

type cachedFile struct {
	modified time.Time
	agents   []*lang.AgentDef
}

// NewLinker creates an empty linker.
func NewLinker() *Linker {
	return &Linker{files: map[string]cachedFile{}, registered: map[string][]*lang.AgentDef{}}
}

func canonicalPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return resolveDeep(abs)
}

// resolveDeep follows the symbolic links of a path also when its last parts do
// not exist yet, by resolving the deepest folder that exists.
func resolveDeep(abs string) string {
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs
	}
	return filepath.Join(resolveDeep(parent), filepath.Base(abs))
}

func fileKey(src *lang.SourceFile) string {
	if src.Path != "" {
		return canonicalPath(src.Path)
	}
	return src.Name
}

// Register remembers the agents of a file, so the other agents of that file can
// be linked by name.
func (l *Linker) Register(agents []*lang.AgentDef) {
	if len(agents) == 0 {
		return
	}
	key := fileKey(agents[0].Source)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.registered[key] = agents
}

func (l *Linker) siblings(src *lang.SourceFile) []*lang.AgentDef {
	key := fileKey(src)
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.registered[key]
}

// Load reads and parses an agent file, or returns the copy it already has when
// the file did not change.
func (l *Linker) Load(path string) ([]*lang.AgentDef, error) {
	resolved := canonicalPath(path)
	var modified time.Time
	known := false
	if info, err := os.Stat(resolved); err == nil {
		modified, known = info.ModTime(), true
	}
	l.mu.Lock()
	if cached, ok := l.files[resolved]; ok && known && cached.modified.Equal(modified) {
		l.mu.Unlock()
		return cached.agents, nil
	}
	l.mu.Unlock()

	text, err := os.ReadFile(resolved)
	if err != nil {
		return nil, diag.Newf("I could not open %s: %v", filepath.Base(path), reasonOf(err)).
			Fix("check that the file is there")
	}
	agents, err := lang.ParseFile(filepath.Base(resolved), resolved, string(text))
	if err != nil {
		return nil, err
	}
	l.Register(agents)
	if known {
		l.mu.Lock()
		l.files[resolved] = cachedFile{modified: modified, agents: agents}
		l.mu.Unlock()
	}
	return agents, nil
}

func reasonOf(err error) string {
	if os.IsNotExist(err) {
		return "the file does not exist"
	}
	return err.Error()
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func pick(agents []*lang.AgentDef, name, path string, explicit bool) (*lang.AgentDef, error) {
	for _, a := range agents {
		if a.Name == name {
			return a, nil
		}
	}
	if explicit && len(agents) == 1 {
		return agents[0], nil
	}
	names := make([]string, len(agents))
	for i, a := range agents {
		names[i] = a.Name
	}
	return nil, diag.Newf("%s has no agent called %s", filepath.Base(path), name).
		Fixf("it has: %s", strings.Join(names, ", "))
}

// exactCase returns the path with the capitalisation the file has on disk. A
// system that ignores case finds `inner.ag` when asked for `Inner.ag`, and the
// messages must name the file the way it is written there, or the person looks
// for a file that does not exist. When several names differ only by case, the
// one that matches exactly wins.
func exactCase(path string) string {
	dir, base := filepath.Split(path)
	if dir == "" {
		dir = "."
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return path
	}
	for _, entry := range entries {
		if entry.Name() == base {
			return path
		}
	}
	for _, entry := range entries {
		if strings.EqualFold(entry.Name(), base) {
			return filepath.Join(dir, entry.Name())
		}
	}
	return path
}

func candidates(dir, name string) []string {
	out := []string{filepath.Join(dir, name+".ag")}
	if lower := filepath.Join(dir, strings.ToLower(name)+".ag"); lower != out[0] {
		out = append(out, lower)
	}
	return out
}

// callerDirOf is the folder of the file an agent was written in; the project
// folder when the agent did not come from a file.
func callerDirOf(root string, caller *lang.AgentDef) string {
	if p := caller.Source.Path; p != "" {
		if dir := filepath.Dir(p); dir != "." && dir != "" {
			return canonicalPath(dir)
		}
	}
	return root
}

// outsideProject reports whether path lies outside the project folder.
func outsideProject(root, path string) bool {
	rel, err := filepath.Rel(canonicalPath(root), canonicalPath(path))
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}

// LinkWarnings advises about links that reach outside the project (requirement
// T5): an agent that lives elsewhere can change without anyone in this project
// noticing. It never blocks anything.
func LinkWarnings(root string, agents []*lang.AgentDef) []*diag.Diagnostic {
	var warnings []*diag.Diagnostic
	for _, agent := range agents {
		dir := callerDirOf(root, agent)
		for _, decl := range agent.Links {
			if !decl.HasPath {
				continue
			}
			path := decl.Path
			if !filepath.IsAbs(path) {
				path = filepath.Join(dir, path)
			}
			if !outsideProject(root, path) {
				continue
			}
			warnings = append(warnings, diag.Newf("the link to `%s` points outside this project (`%s`)", decl.Name, decl.Path).
				Fix("keep the agents you link to inside the project, or make sure you trust that file and the programs it starts").
				Located(agent.Source.Name, decl.Span.Line, decl.Span.Col, agent.Source.Text).
				AsWarning())
		}
	}
	diag.SortByPlace(warnings)
	return warnings
}

// Resolve finds the agent that `link name [from "path"]` refers to, in this
// order: the same file, next to the calling file, the `agents/` folder of the
// project. A quoted path is used exactly as written, starting from the folder
// of the calling file.
func (l *Linker) Resolve(root string, caller *lang.AgentDef, name, quoted string, hasQuoted bool) (*lang.AgentDef, error) {
	callerDir := callerDirOf(root, caller)
	if hasQuoted {
		return l.resolveQuoted(callerDir, caller, name, quoted)
	}
	for _, sibling := range l.siblings(caller.Source) {
		if sibling.Name == name {
			return sibling, nil
		}
	}
	return l.resolveSearched(root, callerDir, caller, name)
}

// resolveQuoted is the agent of `link name from "path"`: the file is the one that is written.
func (l *Linker) resolveQuoted(callerDir string, caller *lang.AgentDef, name, quoted string) (*lang.AgentDef, error) {
	path := quoted
	if !filepath.IsAbs(quoted) {
		path = filepath.Join(callerDir, quoted)
	}
	if !isFile(path) {
		return nil, diag.Newf("I could not find the agent file `%s`", quoted).
			AddRelated(fmt.Sprintf("agent %s asked for it with: link %s from \"%s\"", caller.Name, name, quoted)).
			Fix("check the path, which starts from the folder of the file that has the link line")
	}
	agents, err := l.Load(path)
	if err != nil {
		return nil, err
	}
	return pick(agents, name, path, true)
}

// resolveSearched looks for the file of a `link name` that has no path: next to the caller, and in
// the `agents/` folder of the project. When it finds nothing, it says every place it looked.
func (l *Linker) resolveSearched(root, callerDir string, caller *lang.AgentDef, name string) (*lang.AgentDef, error) {
	looked := []string{"in the same file"}
	for _, dir := range []string{callerDir, filepath.Join(root, "agents")} {
		for _, candidate := range candidates(dir, name) {
			looked = append(looked, candidate)
			if isFile(candidate) {
				found := exactCase(candidate)
				agents, err := l.Load(found)
				if err != nil {
					return nil, err
				}
				return pick(agents, name, found, false)
			}
		}
	}
	d := diag.Newf("I could not find an agent called %s", name).AddRelated("I looked:")
	for _, place := range looked {
		d.AddRelated("  " + place)
	}
	return nil, d.Fixf("create %s.ag next to %s, or write the path yourself: link %s from \"path/to/file.ag\"",
		strings.ToLower(name), caller.Source.Name, name)
}

// checkCycle stops agents that call each other in a circle. chain lists the
// agents running now, outermost first; target is the one about to be called.
func checkCycle(chain []string, target string) *diag.Diagnostic {
	start := -1
	for i, name := range chain {
		if name == target {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	path := append(append([]string{}, chain[start:]...), target)
	shown := strings.Join(path, " -> ")
	message := fmt.Sprintf("these agents are calling each other in a circle (%s)", shown)
	if len(path) == 3 {
		message = fmt.Sprintf("%s asked %s, and %s asked %s again (%s)", path[0], path[1], path[1], path[0], shown)
	}
	return diag.New(message).Fix("make one of them answer without calling the other")
}

func CheckLinks(l *Linker, root string, agents []*lang.AgentDef) []*diag.Diagnostic {
	var problems []*diag.Diagnostic
	for _, agent := range agents {
		targets, unresolved := resolveLinks(l, root, agent)
		problems = append(problems, unresolved...)
		problems = append(problems, checkLinkedCalls(agent, targets)...)
	}
	diag.SortByPlace(problems)
	return problems
}

// resolveLinks finds the agent that each `link` of an agent points to, and the problems of the
// ones that cannot be found.
func resolveLinks(l *Linker, root string, agent *lang.AgentDef) (map[string]*lang.AgentDef, []*diag.Diagnostic) {
	targets := map[string]*lang.AgentDef{}
	var problems []*diag.Diagnostic
	for _, decl := range agent.Links {
		target, err := l.Resolve(root, agent, decl.Name, decl.Path, decl.HasPath)
		if err != nil {
			if d, ok := diag.From(err); ok {
				problems = append(problems, d.Located(agent.Source.Name, decl.Span.Line, decl.Span.Col, agent.Source.Text))
			}
			continue
		}
		targets[decl.Name] = target
	}
	return targets, problems
}

// checkLinkedCalls checks each call that an agent makes to an agent it links to against what
// that one accepts.
func checkLinkedCalls(agent *lang.AgentDef, targets map[string]*lang.AgentDef) []*diag.Diagnostic {
	var problems []*diag.Diagnostic
	for _, body := range runnableBodies(agent) {
		for _, call := range lang.CollectCalls(body) {
			target, ok := targets[call.Target]
			if !ok {
				continue
			}
			args := tools.Args{}
			for _, arg := range call.Args {
				args[arg.Key] = value.Nothing
			}
			if d := CheckMessage(target, call.Action, args); d != nil {
				problems = append(problems, d.Located(agent.Source.Name, call.Span.Line, call.Span.Col, agent.Source.Text))
			}
		}
	}
	return problems
}

// runnableBodies are all the statements that an agent runs: its handlers and its `on start`.
func runnableBodies(agent *lang.AgentDef) [][]lang.Stmt {
	var bodies [][]lang.Stmt
	for _, handler := range agent.Handlers {
		bodies = append(bodies, handler.Body)
	}
	if agent.Start != nil {
		bodies = append(bodies, agent.Start.Body)
	}
	return bodies
}

// callKey carries the Call through the context, because a tool receives only
// a context. The link tool needs it to know which agents are running.
type callKey struct{}

func withCall(ctx context.Context, call *Call) context.Context {
	return context.WithValue(ctx, callKey{}, call)
}

func callFrom(ctx context.Context) *Call {
	if call, ok := ctx.Value(callKey{}).(*Call); ok {
		return call
	}
	return &Call{TaskID: NewTaskID()}
}

// linkTool is a linked agent used like a tool: `Weather.ask city: "Lisbon"`.
type linkTool struct {
	rt     *Runtime
	caller *lang.AgentDef
	decl   *lang.LinkDecl
}

func (t *linkTool) Name() string { return t.decl.Name }

func (t *linkTool) target() (*lang.AgentDef, error) {
	return t.rt.Linker.Resolve(t.rt.Config.Root, t.caller, t.decl.Name, t.decl.Path, t.decl.HasPath)
}

func (t *linkTool) Actions(context.Context) ([]lang.ActionInfo, error) {
	target, err := t.target()
	if err != nil {
		return nil, err
	}
	var actions []lang.ActionInfo
	for _, accept := range target.Accepts {
		info := lang.ActionInfo{Name: accept.Message, Description: accept.Description}
		for _, param := range accept.Params {
			info.Params = append(info.Params, lang.ParamInfo{Name: param, Required: true})
		}
		actions = append(actions, info)
	}
	return actions, nil
}

func (t *linkTool) Call(ctx context.Context, action string, args tools.Args) (value.Value, error) {
	target, err := t.target()
	if err != nil {
		return value.Nothing, err
	}
	call := callFrom(ctx)
	if d := checkCycle(call.Chain, target.Name); d != nil {
		return value.Nothing, d
	}
	if d := CheckMessage(target, action, args); d != nil {
		return value.Nothing, d
	}
	agent, err := NewAgent(t.rt, target, call.TaskID)
	if err != nil {
		return value.Nothing, err
	}
	result, err := agent.Handle(ctx, call, action, args)
	if err != nil {
		return value.Nothing, wrapLinked(target, action, err)
	}
	return result, nil
}

// wrapLinked reports a failure inside a linked agent: one sentence for the
// caller, and under it the problem as the linked agent saw it, with its own
// file and line.
func wrapLinked(target *lang.AgentDef, action string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	d := diag.Newf("%s could not answer `%s`", target.Name, action)
	d.AddRelated(target.Name + " could not answer:")
	text := err.Error()
	if inner, ok := diag.From(err); ok {
		// A failure that may pass is still one for the caller, and says so once, on the sentence of the caller.
		plain := *inner
		plain.Retry = nil
		text = plain.Render()
		d.Retry = inner.Retry
	}
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		d.AddRelated("  " + line)
	}
	return d
}
