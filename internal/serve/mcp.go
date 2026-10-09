package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Ribeiro/metagente-go/internal/applog"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// The agents, served as tools of the Model Context Protocol: each message an agent
// accepts is a tool, so a program that speaks MCP (an editor, a desktop assistant,
// the `tool x from mcp` of another agent) can call it.
//
// It is served over standard input and output, and over HTTP behind the door of the
// A2A server (see mcphttp.go). A program that starts this one and talks to it
// through its pipes has no network to cross and nobody to impersonate, and the one
// who started it is who decides who may use it. That is why the first asks for no
// token and opens no port; the second asks for everything the A2A server asks for.

// MCPConfig is how the MCP server is set up.
type MCPConfig struct {
	// Version is the one the server says it is.
	Version string
	// RequestTimeout is the most a tool call may take. 5 minutes when zero.
	RequestTimeout time.Duration
	// MaxInFlight is the most calls running at once. 16 when zero.
	MaxInFlight int
	// MaxConversations is the most conversations held at once. 256 when zero.
	MaxConversations int
	// MaxCallDepth is how many agents may be running in a call, counting the ones
	// that called this one through MCP (requirement D2). 8 when zero.
	MaxCallDepth int
	// Log keeps the details of failures inside the server. May be nil.
	Log *applog.Log

	// running and room, when set, are the places for calls and for conversations of
	// another server, shared with this one; MaxInFlight and MaxConversations are then
	// not used. makeRoom is asked to let go of what expired before a conversation is
	// refused for want of a place.
	running  places
	room     places
	makeRoom func()
}

// MCPServer serves agents as tools.
type MCPServer struct {
	cfg     MCPConfig
	agents  []Agent
	server  *sdk.Server
	running places
	room    places

	// stopping ends when the server stops, and cancels the calls that are running:
	// a session waits for its calls before it ends.
	stopping context.Context
	stop     context.CancelFunc

	mu    sync.Mutex
	slots map[convKey]*convSlot

	// letting go is one at a time: a session that ends while the server stops takes its conversations out of slots before it
	// closes them, and Close must not return before they are closed, though it finds slots empty.
	letting sync.Mutex
}

// convKey says whose conversation it is: one for each agent in each session, so what
// an agent remembers in one session is never known in another.
type convKey struct {
	session *sdk.ServerSession
	agent   string
}

type convSlot struct {
	conv Conversation
	busy chan struct{} // holding a place in it means using the conversation
}

func (s *convSlot) acquire(ctx context.Context) error {
	select {
	case s.busy <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *convSlot) release() { <-s.busy }

// NewMCP makes an MCP server for the agents.
func NewMCP(cfg MCPConfig, agents []Agent) (m *MCPServer, err error) {
	if len(agents) == 0 {
		return nil, errors.New("there is no agent to serve")
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 5 * time.Minute
	}
	if cfg.MaxInFlight <= 0 {
		cfg.MaxInFlight = 16
	}
	if cfg.MaxConversations <= 0 {
		cfg.MaxConversations = 256
	}
	if cfg.MaxCallDepth <= 0 {
		cfg.MaxCallDepth = 8
	}
	if cfg.Version == "" {
		cfg.Version = "0.0.0"
	}
	m = &MCPServer{cfg: cfg, agents: agents, running: cfg.running, room: cfg.room, slots: map[convKey]*convSlot{}}
	m.stopping, m.stop = context.WithCancel(context.Background())
	if m.running == nil {
		m.running = newPlaces(cfg.MaxInFlight)
	}
	if m.room == nil {
		m.room = newPlaces(cfg.MaxConversations)
	}
	// The SDK refuses a tool it does not like by panicking; here that is an error.
	defer func() {
		if r := recover(); r != nil {
			m, err = nil, fmt.Errorf("an agent cannot be served as a tool: %v", r)
		}
	}()
	m.server = m.build()
	return m, nil
}

// ToolNames are the names of the tools, in the order they were made.
func (m *MCPServer) ToolNames() []string {
	tools := planTools(m.agents)
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.name
	}
	return names
}

// Server is the server of the SDK, for a caller that wants to connect it to a
// transport of its own.
func (m *MCPServer) Server() *sdk.Server { return m.server }

// Run serves on the transport until the other side goes away or the context ends,
// and then lets go of every conversation.
func (m *MCPServer) Run(ctx context.Context, transport sdk.Transport) error {
	defer m.Close()
	return m.server.Run(ctx, transport)
}

// RunIO serves on a pair of streams: standard input and output of the process.
//
// A session ends when the other side closes its streams, and that is not a failure
// whatever shape the error of the SDK takes: it is how a program that started this
// one says that it is done.
func (m *MCPServer) RunIO(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := &eofReader{Reader: in}
	err := m.Run(ctx, &sdk.IOTransport{Reader: io.NopCloser(reader), Writer: nopWriteCloser{out}})
	if err != nil && reader.seen.Load() {
		return nil
	}
	return err
}

// eofReader notes that the input came to its end.
type eofReader struct {
	io.Reader
	seen atomic.Bool
}

func (r *eofReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if errors.Is(err, io.EOF) {
		r.seen.Store(true)
	}
	return n, err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// Close cancels the calls that are running and lets go of every conversation.
func (m *MCPServer) Close() {
	m.stop()
	m.forget(nil)
}

// forget lets go of the conversations of a session that ended, or of every
// conversation when session is nil.
func (m *MCPServer) forget(session *sdk.ServerSession) {
	m.letting.Lock()
	defer m.letting.Unlock()
	m.mu.Lock()
	var gone []*convSlot
	for key, s := range m.slots {
		if session == nil || key.session == session {
			gone = append(gone, s)
			delete(m.slots, key)
			m.room.give()
		}
	}
	m.mu.Unlock()
	for _, s := range gone {
		s.conv.Close()
	}
}

// followSession lets go of the conversations of a session when it ends. A session
// over HTTP ends when the client says so, when it was not used for the time of a
// conversation, or when the server stops; without this, what its agents kept would
// stay until the server stops.
func (m *MCPServer) followSession(_ context.Context, req *sdk.InitializedRequest) {
	session := req.Session
	go func() {
		_ = session.Wait()
		m.forget(session)
	}()
}

// ---------- the tools ----------

type mcpTool struct {
	name  string
	agent Agent
	skill Skill
}

func (m *MCPServer) build() *sdk.Server {
	server := sdk.NewServer(&sdk.Implementation{Name: "metagente", Version: m.cfg.Version},
		&sdk.ServerOptions{InitializedHandler: m.followSession})
	for _, t := range planTools(m.agents) {
		server.AddTool(&sdk.Tool{
			Name:        t.name,
			Description: toolDescription(t.agent, t.skill),
			InputSchema: inputSchema(t.skill),
		}, m.handler(t.agent, t.skill))
	}
	return server
}

// planTools names the tool of each message of each agent: Agent__message, in the
// characters a tool name may have, no longer than 64, and not the same as another.
func planTools(agents []Agent) []mcpTool {
	used := map[string]bool{}
	var tools []mcpTool
	for _, agent := range agents {
		for _, skill := range agent.Skills() {
			name := uniqueToolName(used, toolName(agent.Name(), skill.ID))
			tools = append(tools, mcpTool{name: name, agent: agent, skill: skill})
		}
	}
	return tools
}

const maxToolName = 64

func toolName(agent, skill string) string {
	var b strings.Builder
	for _, r := range agent + "__" + skill {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		if ok {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if len(name) > maxToolName {
		name = name[:maxToolName]
	}
	return name
}

func uniqueToolName(used map[string]bool, name string) string {
	candidate := name
	for i := 2; used[candidate]; i++ {
		suffix := "_" + strconv.Itoa(i)
		candidate = name[:min(len(name), maxToolName-len(suffix))] + suffix
	}
	used[candidate] = true
	return candidate
}

func toolDescription(agent Agent, skill Skill) string {
	text := describe(skill)
	if goal := strings.TrimSpace(agent.Goal()); goal != "" {
		text += " (Agent " + agent.Name() + ": " + goal + ")"
	}
	return text
}

// inputSchema says that a message takes the values it names, all of them, and no
// others. What each value may be is not said: the interface of an agent names its
// values, not their types.
func inputSchema(skill Skill) map[string]any {
	properties := make(map[string]any, len(skill.Params))
	for _, param := range skill.Params {
		properties[param] = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(skill.Params) > 0 {
		schema["required"] = skill.Params
	}
	return schema
}

// ---------- a call ----------

func (m *MCPServer) handler(agent Agent, skill Skill) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (result *sdk.CallToolResult, err error) {
		defer m.recoverCall(&result, &err, agent.Name()+"."+skill.ID)
		return m.call(ctx, req, agent, skill), nil
	}
}

// recoverCall keeps a failure inside one call from taking the server down
// (requirement P4): the cause goes to the log and the caller reads one sentence.
func (m *MCPServer) recoverCall(result **sdk.CallToolResult, err *error, where string) {
	recovered := recover()
	if recovered == nil {
		return
	}
	if m.cfg.Log != nil {
		_, _ = m.cfg.Log.Panic("mcp "+where, recovered, debug.Stack())
	}
	*result, *err = toolError("something went wrong inside the server"), nil
}

func (m *MCPServer) call(ctx context.Context, req *sdk.CallToolRequest, agent Agent, skill Skill) *sdk.CallToolResult {
	note := mcpNote(req)
	note.setAgent(agent.Name())
	note.setMessage(skill.ID)
	var chain []string
	values, e := argumentsOf(req.Params.Arguments)
	if e == nil {
		chain, e = callChain(req, agent, m.cfg.MaxCallDepth)
	}
	if e != nil {
		note.setResult(resultError)
		return toolError(e.Message)
	}
	if !m.running.take() {
		note.setResult(resultBusy)
		return toolError(errServerBusy.Message)
	}
	defer m.running.give()
	ctx, cancel := context.WithTimeout(ctx, m.cfg.RequestTimeout)
	defer cancel()
	defer context.AfterFunc(m.stopping, cancel)()

	call := Call{ID: newToken("task"), Chain: chain}
	note.setTask(call.ID)
	result, err := m.run(ctx, req.Session, agent, skill, call, values)
	if err != nil {
		note.setResult(mcpOutcome(ctx, err))
		return toolError(mcpFailure(ctx, err))
	}
	note.setResult(resultOK)
	return toolResult(result)
}

// run runs the message in the conversation of the agent in this session.
func (m *MCPServer) run(ctx context.Context, session *sdk.ServerSession, agent Agent, skill Skill, call Call, values map[string]value.Value) (value.Value, error) {
	slot, err := m.slotFor(ctx, session, agent, call)
	if err != nil {
		return value.Value{}, err
	}
	if err := slot.acquire(ctx); err != nil {
		return value.Value{}, err
	}
	defer slot.release()
	return slot.conv.Run(ctx, call, skill.ID, values)
}

// callChain reads the agents already running in a call, which a Metagente that calls
// this one puts in the `_meta` of the call, and refuses a call that goes too deep or
// in a circle. A client that is not Metagente sends none.
func callChain(req *sdk.CallToolRequest, agent Agent, limit int) ([]string, *rpcError) {
	chain, e := parseChain(req.Params.Meta[mcpChainMeta])
	if e != nil {
		return nil, e
	}
	return chain, checkDepth(chain, agent, limit)
}

// mcpChainMeta is the key of the chain in the `_meta` of a call; the client of this
// project (internal/mcp) writes it.
const mcpChainMeta = "metagente/chain"

// mcpOutcome says, for the access log, how a call that did not answer ended.
func mcpOutcome(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, ErrTooManyContexts):
		return resultError // as the A2A server says of the same refusal
	case errors.Is(ctx.Err(), context.Canceled):
		return resultLeft
	}
	return resultFailed
}

// argumentsOf reads the values of a call: an object, or nothing at all.
func argumentsOf(raw json.RawMessage) (map[string]value.Value, *rpcError) {
	arguments := map[string]any{}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && string(trimmed) != "null" {
		if json.Unmarshal(trimmed, &arguments) != nil || arguments == nil {
			return nil, invalid("the values of a call have to be an object")
		}
	}
	return toValues(arguments)
}

// slotFor finds the conversation of an agent in a session, starting it with the
// first call. The first call of a session waits for the others to start theirs; it
// happens once for each agent.
func (m *MCPServer) slotFor(ctx context.Context, session *sdk.ServerSession, agent Agent, call Call) (*convSlot, error) {
	key := convKey{session: session, agent: agent.Name()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[key]; s != nil {
		return s, nil
	}
	if !m.takeRoom() {
		return nil, ErrTooManyContexts
	}
	conv, err := agent.Begin(ctx, newContextID(), call)
	if err != nil {
		m.room.give()
		return nil, err
	}
	s := &convSlot{conv: conv, busy: make(chan struct{}, 1)}
	m.slots[key] = s
	return s, nil
}

// takeRoom takes the place of a conversation, asking the one this server shares
// them with to let go of what expired when there is none.
func (m *MCPServer) takeRoom() bool {
	if m.room.take() {
		return true
	}
	if m.cfg.makeRoom == nil {
		return false
	}
	m.cfg.makeRoom()
	return m.room.take()
}

func mcpFailure(ctx context.Context, err error) string {
	if errors.Is(err, ErrTooManyContexts) {
		return "the server holds as many conversations as it may; try again later"
	}
	return failureText(ctx, err)
}

// toolResult is what an agent replied, as text: a text as it is, anything else as
// JSON, so the program that called can read the fields of a record.
func toolResult(v value.Value) *sdk.CallToolResult {
	text := ""
	switch v.Kind {
	case value.KindText:
		text = v.Text
	case value.KindNothing:
	default:
		if raw, err := json.Marshal(v.ToJSON()); err == nil {
			text = string(raw)
		} else {
			text = v.Display()
		}
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}

// toolError is a failure the caller can read: the problem and what to do about it,
// never a place in a file of this computer (requirement P1).
func toolError(text string) *sdk.CallToolResult {
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: text}}}
}
