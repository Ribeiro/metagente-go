// Package remote lets an agent call an agent that runs somewhere else, over the
// A2A protocol (JSON-RPC over HTTP): `remote Bob at "https://host"`, called like
// a tool: `Bob.ask city: "Lisbon"`.
//
// Only the client is here. It is written on net/http because what matters most
// is under our control: the address was approved by the person (T1), no
// redirect is followed, an answer has a size limit, the card of an agent cannot
// send the calls to another address, and a call that is given up is cancelled
// on the other side too.
package remote

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"metagente/internal/clip"
	"metagente/internal/diag"
	"metagente/internal/lang"
	"metagente/internal/secret"
	"metagente/internal/tools"
	"metagente/internal/value"
)

// protocolVersion is the version of A2A this client speaks.
const protocolVersion = "1.0"

// notAnA2AAgent is what to check when an address does not answer like an agent.
const notAnA2AAgent = "check that the address belongs to an A2A agent"

// Spec says which agent a `remote` line names.
type Spec struct {
	Name string
	URL  string
	// Credential is the NAME of the variable whose token is sent as a bearer
	// token with every call (not with the public card). Empty: none is sent.
	Credential string
}

// Options is what the pool needs from the runtime.
type Options struct {
	// Allow says whether an address may be reached. A pool without it refuses
	// everything.
	Allow func(Spec) error
	// MaxResponse is the most bytes read from one answer. Zero means 5 MiB.
	MaxResponse int64
	// PollEvery is the pause between two looks at a task that is not finished.
	// Zero means 150 ms.
	PollEvery time.Duration
	// HTTPClient is used by tests. The default never follows redirects and never
	// reads a proxy from the environment.
	HTTPClient *http.Client
	// Getenv reads the variables of the process; it is os.Getenv when nil.
	Getenv func(string) string
}

// Pool holds the remote agents of one process.
type Pool struct {
	opts   Options
	client *http.Client

	mu     sync.Mutex
	agents map[string]*agent
}

// NewPool creates a pool. Nothing is reached until a tool is called.
func NewPool(opts Options) *Pool {
	client := opts.HTTPClient
	if client == nil {
		client = &http.Client{
			Transport: &http.Transport{
				Proxy:               nil,
				DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
				TLSHandshakeTimeout: 10 * time.Second,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if opts.MaxResponse <= 0 {
		opts.MaxResponse = 5 << 20
	}
	if opts.PollEvery <= 0 {
		opts.PollEvery = 150 * time.Millisecond
	}
	if opts.Getenv == nil {
		opts.Getenv = os.Getenv
	}
	return &Pool{opts: opts, client: client, agents: map[string]*agent{}}
}

// Tool returns the tool an agent sees for a `remote` line.
func (p *Pool) Tool(name string, spec Spec) tools.Tool {
	key := spec.Name + "\x00" + spec.URL + "\x00" + spec.Credential
	p.mu.Lock()
	defer p.mu.Unlock()
	a, ok := p.agents[key]
	if !ok {
		a = &agent{pool: p, spec: spec}
		p.agents[key] = a
	}
	return &tool{name: name, agent: a}
}

// trailKey carries the agents that are running, outermost first, so the other
// side can see how deep the calls go (requirement D2).
type trailKey struct{}

// WithTrail attaches the chain of agents running now to a context.
func WithTrail(ctx context.Context, chain []string) context.Context {
	return context.WithValue(ctx, trailKey{}, append([]string(nil), chain...))
}

func trailFrom(ctx context.Context) []string {
	chain, _ := ctx.Value(trailKey{}).([]string)
	return chain
}

// ---------- one remote agent ----------

type skill struct {
	ID          string
	Description string
	// TextOnly is true when the card says that the skill takes text and nothing that is JSON.
	TextOnly bool
}

type card struct {
	endpoint string
	skills   []skill
}

type agent struct {
	pool *Pool
	spec Spec

	mu   sync.Mutex
	card *card
	next int
}

func (a *agent) host() string {
	u, err := url.Parse(a.spec.URL)
	if err != nil {
		return a.spec.URL
	}
	return u.Host
}

func (a *agent) unreachable(err error) error {
	reason := "the connection failed"
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		reason = "it took too long"
	}
	return diag.Newf("I could not reach %s (remote agent %s): %s", a.host(), a.spec.Name, reason).
		Fix("check the address, and that the other agent is being served (metagente serve)")
}

// loadCard returns the card of the agent, asking for it only once. A failure is not
// kept, so the next call tries again.
func (a *agent) loadCard(ctx context.Context) (*card, error) {
	if err := a.guard(); err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.card != nil {
		return a.card, nil
	}
	base, err := a.approvedAddress()
	if err != nil {
		return nil, err
	}
	doc, err := a.fetchCard(ctx, base)
	if err != nil {
		return nil, err
	}
	c, err := a.cardFrom(doc, base)
	if err != nil {
		return nil, err
	}
	a.card = c
	return c, nil
}

// approvedAddress is the address of the agent as the person wrote it, checked.
func (a *agent) approvedAddress() (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(a.spec.URL, "/"))
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return nil, diag.Newf("the address of the remote agent %s is not valid", a.spec.Name).
			Fix("write it like: remote Bob at \"https://host:8080\"")
	}
	return base, nil
}

// What an agent card says that matters here. A card names what an agent handles,
// not the values each skill takes.
type cardInterface struct {
	URL      string `json:"url"`
	Binding  string `json:"protocolBinding"`
	Protocol string `json:"protocolVersion"`
}

type cardSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	InputModes  []string `json:"inputModes"`
}

type cardDocument struct {
	Interfaces        []cardInterface `json:"supportedInterfaces"`
	Skills            []cardSkill     `json:"skills"`
	DefaultInputModes []string        `json:"defaultInputModes"`
}

// fetchCard asks for the card at the approved address and reads it.
func (a *agent) fetchCard(ctx context.Context, base *url.URL) (*cardDocument, error) {
	raw, status, err := a.get(ctx, base.String()+"/.well-known/agent-card.json")
	if err != nil {
		return nil, a.cardRequestError(ctx, err)
	}
	if status != http.StatusOK {
		return nil, diag.Newf("%s answered %d when I asked for the agent card of %s", base.Host, status, a.spec.Name).
			Fix("the address should be where the agent is served, for example http://127.0.0.1:8080")
	}
	var doc cardDocument
	if json.Unmarshal(raw, &doc) != nil {
		return nil, diag.Newf("%s did not give me a readable agent card", base.Host).
			Fix(notAnA2AAgent)
	}
	return &doc, nil
}

// cardRequestError explains why the card could not be asked for.
func (a *agent) cardRequestError(ctx context.Context, err error) error {
	var big tooLarge
	var auth authFailure
	switch {
	case errors.As(err, &auth):
		return auth.error
	case ctx.Err() != nil:
		return ctx.Err()
	case errors.As(err, &big):
		return diag.Newf("the agent card of %s is larger than %d bytes", a.spec.Name, big.limit)
	}
	return a.unreachable(err)
}

// cardFrom takes from a card what is needed to call the agent: where to send the
// calls, and what it handles.
func (a *agent) cardFrom(doc *cardDocument, base *url.URL) (*card, error) {
	in, err := a.jsonRPCInterface(doc)
	if err != nil {
		return nil, err
	}
	endpoint, err := a.endpointOf(in, base)
	if err != nil {
		return nil, err
	}
	return &card{endpoint: endpoint.String(), skills: skillsOf(doc.Skills, doc.DefaultInputModes)}, nil
}

// jsonRPCInterface finds the way of talking that Metagente uses, in a version it
// speaks.
func (a *agent) jsonRPCInterface(doc *cardDocument) (cardInterface, error) {
	for _, in := range doc.Interfaces {
		if !strings.EqualFold(in.Binding, "JSONRPC") {
			continue
		}
		if v := in.Protocol; v != "" && v != "1" && !strings.HasPrefix(v, "1.") {
			return in, diag.Newf("agent %s speaks A2A %s, and Metagente speaks A2A %s", a.spec.Name, v, protocolVersion).
				Fix("ask the other side to offer A2A 1.0")
		}
		return in, nil
	}
	return cardInterface{}, diag.Newf("agent %s does not offer the JSON-RPC way of talking, which is the one Metagente uses", a.spec.Name)
}

// endpointOf is the address the calls go to. It is written by the other side, so
// it may only point to the very address the person approved, never to somewhere
// else.
func (a *agent) endpointOf(in cardInterface, base *url.URL) (*url.URL, error) {
	endpoint, err := url.Parse(in.URL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") {
		return nil, diag.Newf("the agent card of %s has no address to send tasks to", a.spec.Name)
	}
	if !strings.EqualFold(endpoint.Host, base.Host) {
		return nil, diag.Newf("the agent card of %s sends the calls to %s, which is not the address that was approved (%s)",
			a.spec.Name, endpoint.Host, base.Host).
			Fix("declare the remote agent at the address the card names, and approve that one")
	}
	return endpoint, nil
}

// skillsOf lists what the agent handles, by name.
func skillsOf(listed []cardSkill, defaultModes []string) []skill {
	skills := make([]skill, 0, len(listed))
	for _, s := range listed {
		if s.ID == "" {
			continue
		}
		description := s.Description
		if description == "" {
			description = s.Name
		}
		modes := s.InputModes
		if len(modes) == 0 {
			modes = defaultModes
		}
		skills = append(skills, skill{ID: s.ID, Description: description, TextOnly: takesOnlyText(modes)})
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].ID < skills[j].ID })
	return skills
}

// token is the token to send, or "" when this agent has no credential.
func (a *agent) token() string {
	if a.spec.Credential == "" {
		return ""
	}
	return strings.TrimSpace(a.pool.opts.Getenv(a.spec.Credential))
}

// redact takes the token out of a text the other side wrote: an agent that
// echoes what it was sent would otherwise show it.
func (a *agent) redact(text string) string { return secret.Redact(text, a.token()) }

// authorize adds the credential to a call. It refuses to send it without
// encryption to an address that is not on this machine.
func (a *agent) authorize(req *http.Request) error {
	if a.spec.Credential == "" {
		return nil
	}
	token := a.token()
	if token == "" {
		return diag.Newf("the credential for %s is not set: the variable %s is empty", a.spec.Name, a.spec.Credential).
			Fixf("set it in the terminal that runs Metagente, for example: export %s=...", a.spec.Credential)
	}
	if req.URL.Scheme != "https" && !isLoopback(req.URL.Hostname()) {
		return diag.Newf("the credential for %s would travel without encryption to %s", a.spec.Name, req.URL.Host).
			Fix("use https:// for the address of the remote agent, or run it on this same computer")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (a *agent) guard() error {
	if a.pool.opts.Allow == nil {
		return diag.New("this runtime was not set up to call remote agents")
	}
	return a.pool.opts.Allow(a.spec)
}

// get asks for the card. The token goes with it: a server may keep its card for
// those who have the token, and the card is asked for at the very address that
// was approved, so the token goes nowhere else.
func (a *agent) get(ctx context.Context, address string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, 0, err
	}
	if err := a.authorize(req); err != nil {
		return nil, 0, authFailure{err}
	}
	return a.do(req)
}

// authFailure carries a problem with the credential through the functions that
// only know about network errors.
type authFailure struct{ error }

func (a *agent) do(req *http.Request) ([]byte, int, error) {
	resp, err := a.pool.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	limit := a.pool.opts.MaxResponse
	raw, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, 0, err
	}
	if int64(len(raw)) > limit {
		return nil, resp.StatusCode, tooLarge{limit}
	}
	return raw, resp.StatusCode, nil
}

type tooLarge struct{ limit int64 }

func (e tooLarge) Error() string { return fmt.Sprintf("the answer is larger than %d bytes", e.limit) }

// rpc makes one JSON-RPC call and returns its result.
func (a *agent) rpc(ctx context.Context, endpoint, method string, params any) (json.RawMessage, error) {
	a.mu.Lock()
	a.next++
	id := a.next
	a.mu.Unlock()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if err != nil {
		return nil, diag.New("I could not write the request for the remote agent")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, diag.New("the address of the remote agent is not valid")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", protocolVersion)
	if err := a.authorize(req); err != nil {
		return nil, err
	}
	raw, status, err := a.do(req)
	if err != nil {
		var big tooLarge
		if errors.As(err, &big) {
			return nil, diag.Newf("the answer of agent %s is larger than %d bytes", a.spec.Name, big.limit).
				Fix("ask for less, or raise max_http_bytes in the [limits] section of metagente.toml")
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, a.unreachable(err)
	}
	var answer struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	parsed := json.Unmarshal(raw, &answer) == nil
	switch {
	case parsed && answer.Error != nil:
		return nil, diag.Newf("agent %s refused the request: %s", a.spec.Name, a.redact(clip.Collapse(answer.Error.Message, 300))).
			Fix("check the message name and values; the card of the agent lists what it handles")
	case status/100 == 3:
		return nil, diag.Newf("%s answered %d: it redirected the request, and Metagente never follows a redirect", a.host(), status)
	case status == http.StatusServiceUnavailable:
		return nil, diag.Newf("agent %s is busy and cannot take the call now", a.spec.Name).Fix("try again in a moment")
	case status/100 != 2:
		return nil, diag.Newf("%s answered %d", a.host(), status).
			Fix(notAnA2AAgent)
	case !parsed || len(answer.Result) == 0:
		return nil, diag.Newf("agent %s answered without a result", a.spec.Name).
			Fix(notAnA2AAgent)
	}
	return answer.Result, nil
}

func newID(prefix string) string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return prefix + "-0"
	}
	return prefix + "-" + hex.EncodeToString(raw)
}

// ---------- the tool an agent sees ----------

type tool struct {
	name  string
	agent *agent
}

func (t *tool) Name() string { return t.name }

func (t *tool) Actions(ctx context.Context) ([]lang.ActionInfo, error) {
	c, err := t.agent.loadCard(ctx)
	if err != nil {
		return nil, err
	}
	// The card names what the agent handles, not the values each skill takes.
	open := map[string]any{"type": "object", "additionalProperties": true}
	actions := make([]lang.ActionInfo, len(c.skills))
	for i, s := range c.skills {
		actions[i] = lang.ActionInfo{Name: s.ID, Description: s.Description, Schema: open, Mutates: true}
	}
	return actions, nil
}

func (t *tool) Call(ctx context.Context, action string, args tools.Args) (value.Value, error) {
	a := t.agent
	c, err := a.loadCard(ctx)
	if err != nil {
		return value.Nothing, err
	}
	ids := make([]string, len(c.skills))
	var chosen *skill
	for i := range c.skills {
		ids[i] = c.skills[i].ID
		if c.skills[i].ID == action {
			chosen = &c.skills[i]
		}
	}
	if chosen == nil {
		return value.Nothing, tools.UnknownAction(t.name, action, ids)
	}
	part, err := t.messagePart(chosen, action, args)
	if err != nil {
		return value.Nothing, err
	}
	params := map[string]any{
		"message": map[string]any{
			"messageId": newID("msg"),
			"role":      "ROLE_USER",
			"parts":     []any{part},
			"metadata": map[string]any{
				"skill":     action,
				"metagente": map[string]any{"chain": trailFrom(ctx)},
			},
		},
		// The other side is asked to answer at once, with the task if it is not done. A server that is asked to wait
		// until the task ends keeps the caller in this very request, without the number of the task, and if the
		// caller gives up there is nothing to cancel on the other side and the task goes on working for nothing.
		"configuration": map[string]any{"returnImmediately": true},
	}
	result, err := a.rpc(ctx, c.endpoint, "SendMessage", params)
	if err != nil {
		return value.Nothing, err
	}
	var sent struct {
		Message json.RawMessage `json:"message"`
		Task    json.RawMessage `json:"task"`
	}
	if json.Unmarshal(result, &sent) != nil {
		return value.Nothing, diag.Newf("agent %s answered with neither a task nor a message", a.spec.Name)
	}
	if len(sent.Message) > 0 {
		var message struct {
			Parts []json.RawMessage `json:"parts"`
		}
		_ = json.Unmarshal(sent.Message, &message)
		return valueOf(message.Parts), nil
	}
	if len(sent.Task) == 0 {
		return value.Nothing, diag.Newf("agent %s answered with neither a task nor a message", a.spec.Name)
	}
	return t.finish(ctx, c.endpoint, action, sent.Task)
}

// messagePart is what the call is written as. An agent that says it takes only text is sent text:
// the one value of the call, or a line `name: value` for each of several. Any other agent is sent
// the block of data that names the skill and its values, which is what the agents of Metagente read.
func (t *tool) messagePart(s *skill, action string, args tools.Args) (map[string]any, error) {
	if !s.TextOnly {
		arguments := make(map[string]any, len(args))
		for key, v := range args {
			arguments[key] = v.ToJSON()
		}
		return map[string]any{
			"data":      map[string]any{"skill": action, "arguments": arguments},
			"mediaType": "application/json",
		}, nil
	}
	if len(args) == 0 {
		return nil, diag.Newf("agent %s takes only text, and the call to `%s` has no value to send as text", t.agent.spec.Name, action).
			Fixf("give it one, for example: %s.%s text: \"hello\"", t.name, action)
	}
	return map[string]any{"text": callText(args), "mediaType": "text/plain"}, nil
}

// callText writes the values of a call as text: the value itself when there is one, and a line
// `name: value` for each of several, in the order of their names.
func callText(args tools.Args) string {
	if len(args) == 1 {
		for _, v := range args {
			return v.Display()
		}
	}
	names := make([]string, 0, len(args))
	for name := range args {
		names = append(names, name)
	}
	sort.Strings(names)
	lines := make([]string, len(names))
	for i, name := range names {
		lines[i] = name + ": " + args[name].Display()
	}
	return strings.Join(lines, "\n")
}

// takesOnlyText says whether a list of input modes lets in text and nothing that is JSON. A card
// writes a mode as a type of media (`text/plain`, `application/json`) or, as the sample agent of
// the SDK in JavaScript does, as a word (`text`). A card that says nothing is not taken for text.
func takesOnlyText(modes []string) bool {
	text, data := false, false
	for _, mode := range modes {
		m := strings.ToLower(strings.TrimSpace(mode))
		if m == "text" || strings.HasPrefix(m, "text/") {
			text = true
		}
		if m == "json" || m == "application/json" || strings.HasSuffix(m, "+json") {
			data = true
		}
	}
	return text && !data
}

// finish follows a task until it ends, looking at it again every PollEvery.
func (t *tool) finish(ctx context.Context, endpoint, action string, raw json.RawMessage) (value.Value, error) {
	a := t.agent
	for {
		task, ok := readTask(raw)
		if !ok {
			return value.Nothing, diag.Newf("agent %s answered with a task I could not read", a.spec.Name)
		}
		if v, done, err := a.settled(task, action); done {
			return v, err
		}
		polled, err := a.pollTask(ctx, endpoint, task.ID)
		if err != nil {
			return value.Nothing, err
		}
		raw = polled
	}
}

// taskView is what is needed from a task: where it is, what it says and what it
// produced.
type taskView struct {
	ID      string
	State   string
	Message json.RawMessage
	Parts   []json.RawMessage // the parts of the first artifact
}

// readTask reads a task. The state is written as TASK_STATE_COMPLETED or, by older
// agents, as completed; both become COMPLETED.
func readTask(raw json.RawMessage) (taskView, bool) {
	var task struct {
		ID     string `json:"id"`
		Status struct {
			State   string          `json:"state"`
			Message json.RawMessage `json:"message"`
		} `json:"status"`
		Artifacts []struct {
			Parts []json.RawMessage `json:"parts"`
		} `json:"artifacts"`
	}
	if json.Unmarshal(raw, &task) != nil {
		return taskView{}, false
	}
	view := taskView{
		ID:      task.ID,
		State:   strings.TrimPrefix(strings.ToUpper(task.Status.State), "TASK_STATE_"),
		Message: task.Status.Message,
	}
	if len(task.Artifacts) > 0 {
		view.Parts = task.Artifacts[0].Parts
	}
	return view, true
}

// settled says whether the task has ended, and what it came to.
func (a *agent) settled(task taskView, action string) (v value.Value, done bool, err error) {
	switch task.State {
	case "COMPLETED":
		return valueOf(task.Parts), true, nil
	case "FAILED", "REJECTED", "CANCELED", "CANCELLED":
		return value.Nothing, true, a.failedTask(task, action)
	case "INPUT_REQUIRED", "AUTH_REQUIRED":
		return value.Nothing, true, diag.Newf("agent %s needs more from you before it can answer, which Metagente does not support yet", a.spec.Name)
	}
	return value.Nothing, false, nil
}

// failedTask is the problem for a task that ended badly, with the words of the
// other side under it (taken out of the token, in case it echoes it).
func (a *agent) failedTask(task taskView, action string) error {
	d := diag.Newf("agent %s could not answer `%s`", a.spec.Name, action)
	if len(task.Message) > 0 {
		for _, line := range strings.Split(strings.TrimSpace(textOf(task.Message)), "\n") {
			d.AddRelated("  " + a.redact(line))
		}
	}
	return d
}

// pollTask waits, and then asks how the task is. If the caller gives up while it
// waits, the task is cancelled on the other side too.
func (a *agent) pollTask(ctx context.Context, endpoint, id string) (json.RawMessage, error) {
	select {
	case <-time.After(a.pool.opts.PollEvery):
	case <-ctx.Done():
		a.cancel(endpoint, id)
		return nil, ctx.Err()
	}
	polled, err := a.rpc(ctx, endpoint, "GetTask", map[string]any{"id": id})
	if err != nil {
		if ctx.Err() != nil {
			a.cancel(endpoint, id)
		}
		return nil, err
	}
	return polled, nil
}

// cancel asks the other side to stop working on a task nobody waits for any more.
// It is a courtesy: nothing depends on the answer.
func (a *agent) cancel(endpoint, id string) {
	if id == "" {
		return
	}
	ctx, stop := context.WithTimeout(context.Background(), 2*time.Second)
	defer stop()
	_, _ = a.rpc(ctx, endpoint, "CancelTask", map[string]any{"id": id})
}

// textOf joins the texts of a message.
func textOf(message json.RawMessage) string {
	var m struct {
		Parts []json.RawMessage `json:"parts"`
	}
	if json.Unmarshal(message, &m) != nil {
		return ""
	}
	var texts []string
	for _, part := range m.Parts {
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(part, &p) == nil && p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

// valueOf is the value of the parts of an artifact: a data part as it is, else
// the texts joined.
func valueOf(parts []json.RawMessage) value.Value {
	var texts []string
	for _, part := range parts {
		var p struct {
			Data *json.RawMessage `json:"data"`
			Text string           `json:"text"`
		}
		if json.Unmarshal(part, &p) != nil {
			continue
		}
		if p.Data != nil {
			var decoded any
			if json.Unmarshal(*p.Data, &decoded) == nil {
				return value.FromJSON(decoded)
			}
		}
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	if len(texts) == 0 {
		return value.Nothing
	}
	return value.Text(strings.Join(texts, "\n"))
}
