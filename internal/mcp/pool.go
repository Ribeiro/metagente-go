// Package mcp lets agents use tool servers that speak the Model Context
// Protocol. The protocol itself is the work of the official Go SDK; what lives
// here is what Metagente adds around it:
//
//   - a server is started or reached only after the person approved it (T1);
//   - a program receives a minimal environment and never a secret (E1, E6);
//   - one session is shared by all the agents of a process, and calls on it
//     run at the same time, up to a limit (E3);
//   - closing the pool ends every program it started (E4).
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"metagente/internal/clip"
	"metagente/internal/config"
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/secret"
	"metagente/internal/tools"
	"metagente/internal/value"
)

// Spec says which tool server a tool talks to.
type Spec struct {
	// Command is a command line to start, or an https:// address to connect to.
	Command string
	// Env are the names of the variables a program receives besides the minimal
	// set (`tool x from mcp "cmd" env "A"`).
	Env []string
	// Credential is the NAME of the variable whose token is sent as a bearer
	// token to a server that is an address. A program never gets one.
	Credential string
}

func (s Spec) key() string {
	env := append([]string(nil), s.Env...)
	sort.Strings(env)
	return s.Command + "\x00" + strings.Join(env, ",") + "\x00" + s.Credential
}

// Options is what the pool needs from the runtime.
type Options struct {
	// Root is the project folder, where programs are started.
	Root   string
	Limits config.Limits
	// Hidden are variables that hold secrets. A program never receives them.
	Hidden []string
	// Allow says whether a server may be started or reached. A pool without it
	// refuses everything.
	Allow func(Spec) error
	// Getenv reads the variables of this process; it is os.Getenv when nil.
	Getenv func(string) string
	// TerminateAfter is how long a program has to end by itself when its
	// session is closed. It is 5 seconds when zero.
	TerminateAfter time.Duration
}

// Pool shares the sessions with tool servers among the agents of one process.
type Pool struct {
	opts Options
	// callLimit is how many calls may run at the same time on one server (E3).
	callLimit int

	mu      sync.Mutex
	servers map[string]*server
	closed  bool
}

// NewPool creates a pool. Nothing is started until a tool is used.
func NewPool(opts Options) *Pool {
	size := opts.Limits.MaxMCPCalls
	if size < 1 {
		size = 1
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	if opts.TerminateAfter <= 0 {
		opts.TerminateAfter = 5 * time.Second
	}
	return &Pool{opts: opts, callLimit: size, servers: map[string]*server{}}
}

func (p *Pool) server(spec Spec) *server {
	key := spec.key()
	p.mu.Lock()
	defer p.mu.Unlock()
	if s, ok := p.servers[key]; ok {
		return s
	}
	s := &server{pool: p, spec: spec, slots: make(chan struct{}, p.callLimit)}
	p.servers[key] = s
	return s
}

// Tool returns the tool an agent sees for a declaration.
func (p *Pool) Tool(name string, spec Spec) tools.Tool {
	return &tool{pool: p, name: name, srv: p.server(spec)}
}

// Close ends every session, and with it every program the pool started.
func (p *Pool) Close() error {
	p.mu.Lock()
	p.closed = true
	servers := make([]*server, 0, len(p.servers))
	for _, s := range p.servers {
		servers = append(servers, s)
	}
	p.mu.Unlock()

	var wg sync.WaitGroup
	for _, s := range servers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.disconnect(nil)
		}()
	}
	wg.Wait()
	return nil
}

func (p *Pool) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// ---------- one server ----------

type server struct {
	pool *Pool
	spec Spec

	// slots are the places for calls that run at the same time on this server.
	slots chan struct{}

	// connecting makes one connection attempt at a time; calls themselves do
	// not take it.
	connecting sync.Mutex
	state      sync.Mutex
	session    *sdk.ClientSession
	// pid is the number of the program of the session, and of the group it leads. Zero
	// when there is none, as for a server that is an address.
	pid int

	listMu sync.Mutex
	listed []*sdk.Tool
	have   bool
}

// current returns the open session, if there is one.
func (s *server) current() *sdk.ClientSession {
	s.state.Lock()
	defer s.state.Unlock()
	return s.session
}

func (s *server) connect(ctx context.Context) (*sdk.ClientSession, error) {
	if session := s.current(); session != nil {
		return session, nil
	}
	s.connecting.Lock()
	defer s.connecting.Unlock()
	if session := s.current(); session != nil {
		return session, nil
	}
	if s.pool.isClosed() {
		return nil, diag.New("the tool servers were already shut down")
	}
	if s.pool.opts.Allow == nil {
		return nil, diag.New("this runtime was not set up to use tool servers")
	}
	if err := s.pool.opts.Allow(s.spec); err != nil {
		return nil, err
	}
	transport, cmd, err := s.transport()
	if err != nil {
		return nil, err
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "metagente", Version: "dev"}, &sdk.ClientOptions{
		ToolListChangedHandler: func(context.Context, *sdk.ToolListChangedRequest) { s.forgetList() },
	})

	type outcome struct {
		session *sdk.ClientSession
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		// The session must outlive the call that opened it, so the connection
		// does not follow the context of that call. The call can still give up.
		session, err := client.Connect(context.WithoutCancel(ctx), transport, nil)
		done <- outcome{session, err}
	}()
	select {
	case result := <-done:
		// The program was started by now, if it was going to be.
		pid := processOf(cmd)
		if result.err != nil {
			endGroup(pid, groupGrace)
			return nil, s.cannotStart(result.err)
		}
		s.state.Lock()
		s.session = result.session
		s.pid = pid
		s.state.Unlock()
		return result.session, nil
	case <-ctx.Done():
		go func() {
			result := <-done
			if result.session != nil {
				_ = result.session.Close()
			}
			endGroup(processOf(cmd), groupGrace)
		}()
		return nil, ctx.Err()
	}
}

func (s *server) cannotStart(err error) error {
	what := "start"
	if lang.IsURL(s.spec.Command) {
		what = "reach"
	}
	return diag.Newf("I could not %s the tool server `%s`: %s", what, s.spec.Command, s.redact(err.Error())).
		Fix("run the same command in a terminal to see what it says, and check that the program is installed")
}

// disconnect closes the session, unless another one has replaced the one given
// (a nil argument closes whatever is open).
func (s *server) disconnect(only *sdk.ClientSession) {
	s.state.Lock()
	session := s.session
	if only != nil && session != only {
		s.state.Unlock()
		return
	}
	pid := s.pid
	s.session = nil
	s.pid = 0
	s.state.Unlock()
	s.forgetList()
	if session != nil {
		_ = session.Close()
	}
	// The program is gone, or going; what it started may not be.
	endGroup(pid, groupGrace)
}

// groupGrace is how long what is left of the group has to end by itself after it is asked to.
const groupGrace = 2 * time.Second

// processOf is the number of the program of a command, or zero if it never started.
func processOf(cmd *exec.Cmd) int {
	if cmd == nil || cmd.Process == nil {
		return 0
	}
	return cmd.Process.Pid
}

func (s *server) forgetList() {
	s.listMu.Lock()
	s.listed, s.have = nil, false
	s.listMu.Unlock()
}

func (s *server) transport() (sdk.Transport, *exec.Cmd, error) {
	if lang.IsURL(s.spec.Command) {
		client := newHTTPClient()
		if s.spec.Credential != "" {
			bearer, err := s.bearer()
			if err != nil {
				return nil, nil, err
			}
			client.Transport = bearer.wrap(client.Transport)
		}
		return &sdk.StreamableClientTransport{Endpoint: s.spec.Command, HTTPClient: client}, nil, nil
	}
	words := lang.SplitCommand(s.spec.Command)
	if len(words) == 0 {
		return nil, nil, diag.New("the tool server command is empty")
	}
	env, err := ChildEnv(s.spec.Env, s.pool.opts.Hidden, s.pool.opts.Getenv)
	if err != nil {
		return nil, nil, err
	}
	cmd := exec.Command(words[0], words[1:]...)
	cmd.Dir = s.pool.opts.Root
	cmd.Env = env
	// The program leads a group of its own, so that what it starts can be ended with it.
	cmd.SysProcAttr = groupAttr()
	return &sdk.CommandTransport{Command: cmd, TerminateDuration: s.pool.opts.TerminateAfter}, cmd, nil
}

// token is the token to send, or "" when this server has no credential.
func (s *server) token() string {
	if s.spec.Credential == "" {
		return ""
	}
	return strings.TrimSpace(s.pool.opts.Getenv(s.spec.Credential))
}

func (s *server) redact(text string) string { return secret.Redact(text, s.token()) }

// bearerTransport adds the token to the requests to one host, and to no other.
type bearerTransport struct {
	host  string
	token string
	next  http.RoundTripper
}

func (b *bearerTransport) wrap(next http.RoundTripper) http.RoundTripper {
	b.next = next
	return b
}

func (b *bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.EqualFold(req.URL.Host, b.host) {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+b.token)
	}
	return b.next.RoundTrip(req)
}

// bearer prepares the token for the address of this server, refusing to send it
// without encryption anywhere but to this same computer.
func (s *server) bearer() (*bearerTransport, error) {
	token := s.token()
	if token == "" {
		return nil, diag.Newf("the credential for the tool server %s is not set: the variable %s is empty", s.spec.Command, s.spec.Credential).
			Fixf("set it in the terminal that runs Metagente, for example: export %s=...", s.spec.Credential)
	}
	u, err := url.Parse(s.spec.Command)
	if err != nil {
		return nil, diag.Newf("the address of the tool server is not valid")
	}
	if u.Scheme != "https" && !isLoopback(u.Hostname()) {
		return nil, diag.Newf("the credential for the tool server at %s would travel without encryption", u.Host).
			Fix("use https:// for the address, or run the server on this same computer")
	}
	return &bearerTransport{host: u.Host, token: token}, nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newHTTPClient is the client for tool servers that are addresses: no proxy
// from the environment, a limit to connect, and no redirects (a redirect could
// send the request somewhere nobody approved).
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			Proxy:               nil,
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// tools returns what the server offers, asking it only when needed.
func (s *server) tools(ctx context.Context) ([]*sdk.Tool, error) {
	session, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	s.listMu.Lock()
	if s.have {
		listed := s.listed
		s.listMu.Unlock()
		return listed, nil
	}
	s.listMu.Unlock()

	var listed []*sdk.Tool
	for item, err := range session.Tools(ctx, nil) {
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, diag.Newf("the tool server `%s` did not say what it can do: %v", s.spec.Command, err)
		}
		listed = append(listed, item)
	}
	s.listMu.Lock()
	s.listed, s.have = listed, true
	s.listMu.Unlock()
	return listed, nil
}

// ---------- the tool an agent sees ----------

type tool struct {
	pool *Pool
	name string
	srv  *server
}

func (t *tool) Name() string { return t.name }

func (t *tool) Actions(ctx context.Context) ([]lang.ActionInfo, error) {
	listed, err := t.srv.tools(ctx)
	if err != nil {
		return nil, err
	}
	actions := make([]lang.ActionInfo, 0, len(listed))
	for _, item := range listed {
		actions = append(actions, lang.ActionInfo{
			Name:        item.Name,
			Description: item.Description,
			Params:      paramsOf(item.InputSchema),
			Schema:      item.InputSchema,
			Mutates:     mutates(item),
		})
	}
	sort.Slice(actions, func(i, j int) bool { return actions[i].Name < actions[j].Name })
	return actions, nil
}

func (t *tool) Call(ctx context.Context, action string, args tools.Args) (value.Value, error) {
	listed, err := t.srv.tools(ctx)
	if err != nil {
		return value.Nothing, err
	}
	names := make([]string, 0, len(listed))
	found := false
	for _, item := range listed {
		names = append(names, item.Name)
		found = found || item.Name == action
	}
	if !found {
		sort.Strings(names)
		return value.Nothing, tools.UnknownAction(t.name, action, names)
	}

	// At most MaxMCPCalls calls at the same time on each server: one that is slow does
	// not hold back the calls to the others.
	select {
	case t.srv.slots <- struct{}{}:
		defer func() { <-t.srv.slots }()
	case <-ctx.Done():
		return value.Nothing, ctx.Err()
	}

	session, err := t.srv.connect(ctx)
	if err != nil {
		return value.Nothing, err
	}
	arguments := make(map[string]any, len(args))
	for key, v := range args {
		arguments[key] = v.ToJSON()
	}
	result, err := session.CallTool(ctx, &sdk.CallToolParams{Name: action, Arguments: arguments})
	if err != nil {
		if ctx.Err() != nil {
			return value.Nothing, ctx.Err()
		}
		if errors.Is(err, sdk.ErrConnectionClosed) || !alive(session) {
			// The next call starts the server again.
			t.srv.disconnect(session)
			return value.Nothing, diag.Newf("the tool server of `%s` stopped while answering `%s.%s`", t.name, t.name, action).
				Fix("call it again to start the server anew; if it keeps stopping, run its command in a terminal to see why")
		}
		return value.Nothing, diag.Newf("the tool server of `%s` could not answer `%s.%s`: %s", t.name, t.name, action, t.srv.redact(err.Error()))
	}
	return t.answer(action, result)
}

// alive asks the server whether it still answers. It is used after an error,
// to tell a server that said no from a server that is gone.
func alive(session *sdk.ClientSession) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return session.Ping(ctx, &sdk.PingParams{}) == nil
}

// answer turns what the server sent into a value: its text, joined; or the
// structured answer as a record when there is no text.
func (t *tool) answer(action string, result *sdk.CallToolResult) (value.Value, error) {
	limit := t.pool.opts.Limits.MaxMCPResultBytes
	var parts []string
	size := int64(0)
	for _, content := range result.Content {
		var part string
		switch c := content.(type) {
		case *sdk.TextContent:
			part = c.Text
		default:
			part = "[content that is not text]"
		}
		size += int64(len(part))
		parts = append(parts, part)
	}
	text := strings.Join(parts, "\n")

	if result.IsError {
		message := strings.TrimSpace(text)
		if message == "" {
			message = "it gave no reason"
		}
		return value.Nothing, diag.Newf("`%s.%s` failed: %s", t.name, action, clip.Lines(message, 600))
	}
	if limit > 0 && size > limit {
		return value.Nothing, diag.Newf("the answer of `%s.%s` is larger than the limit of %d bytes", t.name, action, limit).
			Fix("ask the tool for less, or raise `max_mcp_result_bytes` in the [limits] section of metagente.toml")
	}
	if len(parts) == 0 && result.StructuredContent != nil {
		raw, err := json.Marshal(result.StructuredContent)
		if err == nil {
			var decoded any
			if json.Unmarshal(raw, &decoded) == nil {
				return value.FromJSON(decoded), nil
			}
		}
	}
	return value.Text(text), nil
}

// mutates says whether a tool may change things. A server that does not say
// that a tool is read only is taken at its word: it may change things.
func mutates(item *sdk.Tool) bool {
	return item.Annotations == nil || !item.Annotations.ReadOnlyHint
}

// paramsOf reads the names an action takes from the JSON schema the server
// published for it.
func paramsOf(schema any) []lang.ParamInfo {
	raw, err := json.Marshal(schema)
	if err != nil {
		return nil
	}
	var shape struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if json.Unmarshal(raw, &shape) != nil {
		return nil
	}
	required := map[string]bool{}
	for _, name := range shape.Required {
		required[name] = true
	}
	names := make([]string, 0, len(shape.Properties))
	for name := range shape.Properties {
		names = append(names, name)
	}
	sort.Strings(names)
	params := make([]lang.ParamInfo, len(names))
	for i, name := range names {
		params[i] = lang.ParamInfo{Name: name, Required: required[name]}
	}
	return params
}

// ---------- the environment of a program ----------

// baseEnv are the variables every program receives when they are set here:
// enough to find programs and a home, nothing that identifies the person to
// someone else.
func baseEnv() []string {
	names := []string{"PATH", "HOME", "USER", "LANG", "LC_ALL", "LC_CTYPE", "TMPDIR", "TEMP", "TMP"}
	if runtime.GOOS == "windows" {
		names = append(names, "SYSTEMROOT", "SYSTEMDRIVE", "WINDIR", "COMSPEC", "PATHEXT",
			"USERPROFILE", "APPDATA", "LOCALAPPDATA", "HOMEDRIVE", "HOMEPATH")
	}
	return names
}

func isHidden(name string, hidden []string) bool {
	upper := strings.ToUpper(name)
	if strings.HasPrefix(upper, "METAGENTE_") {
		return true
	}
	for _, h := range hidden {
		if strings.ToUpper(h) == upper {
			return true
		}
	}
	return false
}

// ChildEnv builds the environment of a program: the minimal set, plus the
// variables the agent file named. A variable that holds a secret is refused
// even when it is named (requirements E1 and E6).
func ChildEnv(declared, hidden []string, getenv func(string) string) ([]string, error) {
	values := map[string]string{}
	for _, name := range baseEnv() {
		if v := getenv(name); v != "" && !isHidden(name, hidden) {
			values[name] = v
		}
	}
	for _, name := range declared {
		if isHidden(name, hidden) {
			return nil, diag.Newf("`%s` holds a secret, and secrets can never be passed to a tool server", name).
				Fix("remove it from the `env` list; give the tool server its own credential instead")
		}
		if v := getenv(name); v != "" {
			values[name] = v
		}
	}
	env := make([]string, 0, len(values))
	for name, v := range values {
		env = append(env, fmt.Sprintf("%s=%s", name, v))
	}
	sort.Strings(env)
	return env, nil
}
