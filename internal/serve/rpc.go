package serve

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode"

	"github.com/Ribeiro/metagente-go/internal/clip"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/value"
)

// The A2A methods this server answers are SendMessage and the ones whose answer
// is that there is nothing to show: it keeps no task, so GetTask and CancelTask
// find none. An agent answers inside the request, with a Message when it worked
// and a Task that has already failed when it did not. Streaming, push
// notifications and the extended card are not offered, and say so.

// Codes of JSON-RPC and of A2A.
const (
	codeServer            = -32000
	codeParse             = -32700
	codeInvalidRequest    = -32600
	codeMethodNotFound    = -32601
	codeInvalidParams     = -32602
	codeTaskNotFound      = -32001
	codePushUnsupported   = -32003
	codeUnsupported       = -32004
	codeContentType       = -32005
	codeNoExtendedCard    = -32007
	codeVersion           = -32009
	maxParts              = 8
	maxArguments          = 32
	maxChain              = 32
	maxNameLength         = 64
	maxRequestIDLength    = 256
	roleUser              = "ROLE_USER"
	roleAgent             = "ROLE_AGENT"
	stateFailed           = "TASK_STATE_FAILED"
	messageFailureTooLong = "the agent took too long to answer"
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

// wireError is an error as A2A 1.0 writes it: the code, the message, and an ErrorInfo
// that names the reason in the domain of the protocol, which is what a client of an
// SDK reads to tell one error from another.
type wireError struct {
	Code    int              `json:"code"`
	Message string           `json:"message"`
	Data    []map[string]any `json:"data,omitempty"`
}

const (
	errorInfoType  = "type.googleapis.com/google.rpc.ErrorInfo"
	protocolDomain = "a2a-protocol.org"
)

// errorReasons are the reasons that A2A 1.0 gives to its codes.
var errorReasons = map[int]string{
	codeParse:           "PARSE_ERROR",
	codeInvalidRequest:  "INVALID_REQUEST",
	codeMethodNotFound:  "METHOD_NOT_FOUND",
	codeInvalidParams:   "INVALID_PARAMS",
	codeServer:          "SERVER_ERROR",
	codeTaskNotFound:    "TASK_NOT_FOUND",
	codePushUnsupported: "PUSH_NOTIFICATION_NOT_SUPPORTED",
	codeUnsupported:     "UNSUPPORTED_OPERATION",
	codeContentType:     "CONTENT_TYPE_NOT_SUPPORTED",
	codeNoExtendedCard:  "EXTENDED_AGENT_CARD_NOT_CONFIGURED",
	codeVersion:         "VERSION_NOT_SUPPORTED",
}

func (e *rpcError) wire() *wireError {
	if e == nil {
		return nil
	}
	out := &wireError{Code: e.Code, Message: e.Message}
	if reason, ok := errorReasons[e.Code]; ok {
		out.Data = []map[string]any{{"@type": errorInfoType, "reason": reason, "domain": protocolDomain}}
	}
	return out
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any, e *rpcError) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	writeJSON(w, http.StatusOK, rpcResponse{JSONRPC: "2.0", ID: id, Result: result, Error: e.wire()})
}

// supportedVersion says whether the version a client asked for, in the header
// A2A-Version, is one this server speaks: 1.0, or any 1.x. A request that names none
// is taken as one for this server, so a client that does not send it still works.
func supportedVersion(asked string) bool {
	asked = strings.TrimSpace(asked)
	return asked == "" || asked == "1" || strings.HasPrefix(asked, "1.")
}

// serveRPC answers one JSON-RPC request for one agent. The door has already
// checked who is asking, and that the body is JSON and not too large.
func (s *Server) serveRPC(w http.ResponseWriter, r *http.Request, agent Agent) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			refuse(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		writeRPC(w, nil, nil, &rpcError{codeParse, "the request could not be read"})
		return
	}
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		writeRPC(w, nil, nil, &rpcError{codeInvalidRequest, "batches are not supported"})
		return
	}
	var req rpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeRPC(w, nil, nil, &rpcError{codeParse, "the request is not valid JSON"})
		return
	}
	if !validID(req.ID) {
		writeRPC(w, nil, nil, &rpcError{codeInvalidRequest, "the request needs an id that is a text, a number or null"})
		return
	}
	if req.JSONRPC != "2.0" || req.Method == "" {
		writeRPC(w, req.ID, nil, &rpcError{codeInvalidRequest, "this is not a JSON-RPC 2.0 request"})
		return
	}

	note := noteOf(r.Context())
	note.setAgent(agent.Name())
	note.setRPC(req.Method)
	if !supportedVersion(r.Header.Get("A2A-Version")) {
		writeRPC(w, req.ID, nil, &rpcError{codeVersion, "this server speaks version 1.0 of A2A, and not the one the request asked for"})
		return
	}

	switch req.Method {
	case "SendMessage":
		s.serveSend(w, r, agent, req)
	case "GetTask", "CancelTask":
		writeRPC(w, req.ID, nil, &rpcError{codeTaskNotFound, "task not found: this server keeps no tasks"})
	case "ListTasks":
		writeRPC(w, req.ID, map[string]any{"tasks": []any{}, "nextPageToken": "", "pageSize": 0, "totalSize": 0}, nil)
	case "SendStreamingMessage", "SubscribeToTask":
		writeRPC(w, req.ID, nil, &rpcError{codeUnsupported, "this server does not stream"})
	case "CreateTaskPushNotificationConfig", "GetTaskPushNotificationConfig", "ListTaskPushNotificationConfig", "DeleteTaskPushNotificationConfig":
		writeRPC(w, req.ID, nil, &rpcError{codePushUnsupported, "this server does not send push notifications"})
	case "GetExtendedAgentCard":
		writeRPC(w, req.ID, nil, &rpcError{codeNoExtendedCard, "there is no extended agent card"})
	default:
		writeRPC(w, req.ID, nil, &rpcError{codeMethodNotFound, "method not found"})
	}
}

// serveSend runs a message and writes how it ended: the answer, or a 503 when all the
// places for requests at the same time are taken, or nothing when the caller has gone.
func (s *Server) serveSend(w http.ResponseWriter, r *http.Request, agent Agent, req rpcRequest) {
	result, e := s.sendMessage(r.Context(), agent, req.Params)
	noteOf(r.Context()).setResult(outcomeOf(result, e))
	switch {
	case e == errServerBusy:
		w.Header().Set("Retry-After", "1")
		refuse(w, http.StatusServiceUnavailable, e.Message)
	case e == nil && result == nil:
		// the caller left; there is nobody to answer
	default:
		writeRPC(w, req.ID, result, e)
	}
}

// validID accepts what a JSON-RPC id may be, and a short one: it is repeated in
// the answer.
func validID(raw json.RawMessage) bool {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > maxRequestIDLength {
		return false
	}
	switch raw[0] {
	case '"':
		var text string
		return json.Unmarshal(raw, &text) == nil
	case 'n':
		return string(raw) == "null"
	}
	var number json.Number
	return json.Unmarshal(raw, &number) == nil
}

type wireMessage struct {
	MessageID string         `json:"messageId"`
	Role      string         `json:"role"`
	ContextID string         `json:"contextId"`
	TaskID    string         `json:"taskId"`
	Parts     []wirePart     `json:"parts"`
	Metadata  map[string]any `json:"metadata"`
}

// wirePart is one part of a message. Only text and data are understood; a part
// that carries a file is refused, not ignored.
type wirePart struct {
	Text *string         `json:"text"`
	Data json.RawMessage `json:"data"`
	Raw  json.RawMessage `json:"raw"`
	URL  json.RawMessage `json:"url"`
}

// errServerBusy is the answer when the places for requests that run at the same time are
// all taken. It is answered with HTTP 503, not as an error of the protocol, so that a
// proxy or a client that knows nothing of A2A knows that it may try again.
var errServerBusy = &rpcError{codeServer, "the server is busy; try again in a moment"}

func invalid(text string) *rpcError { return &rpcError{codeInvalidParams, text} }

// sendMessage runs a message in a conversation and answers with the result. It
// returns neither result nor error when the caller has gone away.
func (s *Server) sendMessage(parent context.Context, agent Agent, raw json.RawMessage) (any, *rpcError) {
	msg, e := parseMessage(raw)
	if e != nil {
		return nil, e
	}
	skill, args, e := s.pickSkill(agent, msg)
	if e != nil {
		return nil, e
	}
	noteOf(parent).setMessage(skill)
	chain, e := s.checkChain(agent, msg)
	if e != nil {
		return nil, e
	}
	release, e := s.acquire()
	if e != nil {
		return nil, e
	}
	defer release()
	call := Call{ID: newToken("task"), Chain: chain}
	noteOf(parent).setTask(call.ID)
	return s.converse(parent, agent, msg.ContextID, call, skill, args)
}

// parseMessage reads the message of a SendMessage and checks what can be checked
// before anything is run: who sends it, that it names no task (the server keeps
// none) and that the id of the conversation could be one of its own.
func parseMessage(raw json.RawMessage) (*wireMessage, *rpcError) {
	var params struct {
		Message *wireMessage `json:"message"`
	}
	if err := json.Unmarshal(raw, &params); err != nil || params.Message == nil {
		return nil, invalid("the request needs a message")
	}
	msg := params.Message
	switch {
	case msg.Role != roleUser:
		return nil, invalid("the role of the message has to be " + roleUser)
	case msg.TaskID != "":
		return nil, &rpcError{codeTaskNotFound, "task not found: this server keeps no tasks"}
	case len(msg.ContextID) > 128:
		return nil, invalid("unknown or expired conversation")
	}
	return msg, nil
}

// checkChain reads the agents already running in the call, and refuses a call that
// goes deeper than the limit or comes back to this agent (requirement D2).
func (s *Server) checkChain(agent Agent, msg *wireMessage) ([]string, *rpcError) {
	chain, e := chainOf(msg.Metadata)
	if e != nil {
		return nil, e
	}
	return chain, checkDepth(chain, agent, s.cfg.MaxCallDepth)
}

// checkDepth refuses a call that would go deeper than the limit, or come back to an agent
// that is already running in it (requirement D2).
func checkDepth(chain []string, agent Agent, limit int) *rpcError {
	if len(chain) >= limit {
		return &rpcError{codeServer, "the agents are calling each other too deep"}
	}
	for _, name := range chain {
		if name == agent.Name() {
			return &rpcError{codeServer, "the agents are calling each other in a circle"}
		}
	}
	return nil
}

// acquire takes one of the places for requests that run at the same time. A
// server that has none left says so instead of piling requests up.
func (s *Server) acquire() (release func(), e *rpcError) {
	select {
	case s.inflight <- struct{}{}:
		return func() { <-s.inflight }, nil
	default:
		return nil, errServerBusy
	}
}

// converse runs the message in its conversation, starting one when the message
// names none, and turns what happened into the answer.
func (s *Server) converse(parent context.Context, agent Agent, contextID string, call Call, skill string, args map[string]value.Value) (any, *rpcError) {
	ctx, cancel := context.WithTimeout(parent, s.cfg.RequestTimeout)
	defer cancel()

	if contextID == "" {
		id, early, e := s.start(ctx, agent, call)
		if early != nil || e != nil {
			return early, e
		}
		contextID = id
	}
	var result value.Value
	var runErr error
	err := s.contexts.With(ctx, contextID, func(h *held) error {
		if h.agent != agent.Name() {
			return ErrUnknownContext // a conversation belongs to one agent
		}
		result, runErr = h.conv.Run(ctx, call, skill, args)
		return nil
	})
	return s.outcome(parent, ctx, contextID, result, runErr, err)
}

// start opens a conversation. It answers by itself (the second result, or the
// third when it is a request that cannot be served) when there is nothing to run.
func (s *Server) start(ctx context.Context, agent Agent, call Call) (id string, early any, e *rpcError) {
	id, err := s.contexts.OpenWith(func(id string) (*held, error) {
		conv, err := agent.Begin(ctx, id, call)
		if err != nil {
			return nil, err
		}
		return &held{agent: agent.Name(), conv: conv}, nil
	})
	switch {
	case errors.Is(err, ErrTooManyContexts):
		return "", nil, &rpcError{codeServer, "the server holds as many conversations as it may; try again later"}
	case err != nil:
		return "", s.failure("", failureText(ctx, err)), nil
	}
	return id, nil, nil
}

// outcome is the answer to a message that was run, or that could not be.
func (s *Server) outcome(parent, ctx context.Context, contextID string, result value.Value, runErr, err error) (any, *rpcError) {
	if err != nil {
		switch {
		case errors.Is(err, ErrUnknownContext):
			return nil, invalid("unknown or expired conversation")
		case parent.Err() != nil:
			return nil, nil
		}
		return s.failure(contextID, failureText(ctx, err)), nil
	}
	if parent.Err() != nil {
		return nil, nil
	}
	if runErr != nil {
		return s.failure(contextID, failureText(ctx, runErr)), nil
	}
	return map[string]any{"message": map[string]any{
		"messageId": newToken("msg"),
		"role":      roleAgent,
		"contextId": contextID,
		"parts":     resultParts(result),
	}}, nil
}

// pickSkill works out which message to run, and with which values, from a
// request that says it with a data part {"skill": ..., "arguments": {...}} or,
// for a client that only knows how to send text, with the text.
func (s *Server) pickSkill(agent Agent, msg *wireMessage) (string, map[string]value.Value, *rpcError) {
	texts, data, e := readParts(msg.Parts)
	if e != nil {
		return "", nil, e
	}
	skill, arguments, e := chooseSkill(agent, msg, data)
	if e != nil {
		return "", nil, e
	}
	chosen, ok := skillByID(agent, skill)
	if !ok {
		return "", nil, invalid("this agent does not handle `" + clip.Collapse(skill, 40) + "`; it handles: " + strings.Join(skillIDs(agent), ", "))
	}
	arguments, e = bindText(chosen, arguments, texts, data)
	if e != nil {
		return "", nil, e
	}
	values, e := toValues(arguments)
	if e != nil {
		return "", nil, e
	}
	if e := checkValues(agent, chosen, values); e != nil {
		return "", nil, e
	}
	return chosen.ID, values, nil
}

// checkValues refuses a message that lacks a value its skill takes, or carries one it does
// not, before the agent hears of it: the request is what is wrong, and the caller is told
// so with the code of a request that is not valid, not with a task that failed.
func checkValues(agent Agent, skill Skill, values map[string]value.Value) *rpcError {
	takes := map[string]bool{}
	for _, param := range skill.Params {
		takes[param] = true
		if _, given := values[param]; !given {
			return invalid(fmt.Sprintf("the message `%s` of agent %s needs a value for `%s`", skill.ID, agent.Name(), param))
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !takes[name] {
			what := "nothing"
			if len(skill.Params) > 0 {
				what = strings.Join(skill.Params, ", ")
			}
			return invalid(fmt.Sprintf("the message `%s` of agent %s does not take `%s`; it takes: %s", skill.ID, agent.Name(), name, what))
		}
	}
	return nil
}

// readParts separates the text of a message from its data. There may be many
// texts and one data part, and nothing else.
func readParts(parts []wirePart) (texts []string, data map[string]any, e *rpcError) {
	if len(parts) == 0 || len(parts) > maxParts {
		return nil, nil, invalid("a message needs between 1 and 8 parts")
	}
	for _, part := range parts {
		text, object, e := readPart(part)
		switch {
		case e != nil:
			return nil, nil, e
		case object == nil:
			texts = append(texts, text)
		case data != nil:
			return nil, nil, invalid("send one data part, not several")
		default:
			data = object
		}
	}
	return texts, data, nil
}

// readPart reads one part: a text, or the object of a data part. A part that
// carries a file is refused, not ignored.
func readPart(part wirePart) (text string, object map[string]any, e *rpcError) {
	if len(part.Raw) > 0 || len(part.URL) > 0 {
		return "", nil, &rpcError{codeContentType, "files are not supported; send text or data"}
	}
	hasText, hasData := part.Text != nil, len(part.Data) > 0
	if hasText == hasData { // both, or neither
		return "", nil, invalid("each part has to be either text or data")
	}
	if hasText {
		return *part.Text, nil, nil
	}
	if err := json.Unmarshal(part.Data, &object); err != nil || object == nil {
		return "", nil, invalid("the data of a part has to be an object")
	}
	return "", object, nil
}

// chooseSkill says which message is meant, and the values the request already
// carries for it: the data part names the skill, or the metadata does, or the
// agent has only one.
func chooseSkill(agent Agent, msg *wireMessage, data map[string]any) (string, map[string]any, *rpcError) {
	switch {
	case data != nil && data["skill"] != nil:
		return skillOfData(data)
	case msg.Metadata != nil && msg.Metadata["skill"] != nil:
		name, ok := msg.Metadata["skill"].(string)
		if !ok {
			return "", nil, invalid("`skill` has to be a text")
		}
		return name, data, nil
	case len(agent.Skills()) == 1:
		return agent.Skills()[0].ID, data, nil
	}
	return "", nil, invalid("say which message to run: send a data part like {\"skill\": \"" + firstSkill(agent) + "\", \"arguments\": {}}")
}

// skillOfData reads {"skill": ..., "arguments": {...}}.
func skillOfData(data map[string]any) (string, map[string]any, *rpcError) {
	name, ok := data["skill"].(string)
	if !ok {
		return "", nil, invalid("`skill` has to be a text")
	}
	given, present := data["arguments"]
	if !present || given == nil {
		return name, nil, nil
	}
	object, ok := given.(map[string]any)
	if !ok {
		return "", nil, invalid("`arguments` has to be an object")
	}
	return name, object, nil
}

// bindText gives the text of a client that sent only text to the one value the
// message takes. It leaves alone a request that carried data.
func bindText(chosen Skill, arguments map[string]any, texts []string, data map[string]any) (map[string]any, *rpcError) {
	if arguments == nil {
		arguments = map[string]any{}
	}
	if len(texts) == 0 || data != nil {
		return arguments, nil
	}
	switch len(chosen.Params) {
	case 0:
		return arguments, nil
	case 1:
		arguments[chosen.Params[0]] = strings.Join(texts, "\n")
		return arguments, nil
	}
	return nil, invalid("`" + chosen.ID + "` takes several values; send a data part with them")
}

// toValues checks the number and the names of the values, and makes them values.
func toValues(arguments map[string]any) (map[string]value.Value, *rpcError) {
	if len(arguments) > maxArguments {
		return nil, invalid("too many values")
	}
	values := make(map[string]value.Value, len(arguments))
	for name, v := range arguments {
		if !plainName(name) {
			return nil, invalid("a value has a name that is not valid")
		}
		values[name] = value.FromJSON(v)
	}
	return values, nil
}

func skillByID(agent Agent, id string) (Skill, bool) {
	for _, s := range agent.Skills() {
		if s.ID == id {
			return s, true
		}
	}
	return Skill{}, false
}

func skillIDs(agent Agent) []string {
	ids := make([]string, 0, len(agent.Skills()))
	for _, s := range agent.Skills() {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	return ids
}

func firstSkill(agent Agent) string {
	if ids := skillIDs(agent); len(ids) > 0 {
		return ids[0]
	}
	return "message"
}

// chainOf reads the agents already running in the call from the metadata, where
// a Metagente client puts them. They come from a caller that holds the token, so
// they are taken as the word of a peer, but they are still checked for shape.
func chainOf(metadata map[string]any) ([]string, *rpcError) {
	meta, _ := metadata["metagente"].(map[string]any)
	return parseChain(meta["chain"])
}

// parseChain reads a chain of agents: a list of names, or nothing.
func parseChain(list any) ([]string, *rpcError) {
	if list == nil {
		return nil, nil
	}
	items, ok := list.([]any)
	if !ok || len(items) > maxChain {
		return nil, invalid("the chain of agents is not valid")
	}
	chain := make([]string, 0, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok || !plainName(name) {
			return nil, invalid("the chain of agents is not valid")
		}
		chain = append(chain, name)
	}
	return chain, nil
}

// plainName is a name made of letters, digits and underscores, and short.
func plainName(name string) bool {
	if name == "" || len(name) > maxNameLength {
		return false
	}
	for _, c := range name {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			return false
		}
	}
	return true
}

// failureText is what a caller from outside is told about a failure: the plain
// sentence of the problem and what to do about it, never a place in a file of
// this computer (requirement P1).
func failureText(ctx context.Context, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
		return messageFailureTooLong
	case errors.Is(err, context.Canceled):
		return "the call was cancelled"
	}
	if d, ok := diag.From(err); ok {
		return d.Public()
	}
	return "the agent could not answer"
}

// failure is a task that already ended, as the answer to a request that could be
// read but whose agent could not answer.
func (s *Server) failure(contextID, text string) map[string]any {
	task := map[string]any{
		"id": newToken("task"),
		"status": map[string]any{
			"state": stateFailed,
			"message": map[string]any{
				"messageId": newToken("msg"),
				"role":      roleAgent,
				"parts":     []any{map[string]any{"text": text}},
			},
		},
	}
	if contextID != "" {
		task["contextId"] = contextID
	}
	return map[string]any{"task": task}
}

// resultParts is what an agent replied, as parts: text as it is, anything else as
// data, so a record keeps its fields.
func resultParts(v value.Value) []any {
	switch v.Kind {
	case value.KindText:
		return []any{map[string]any{"text": v.Text}}
	case value.KindNothing:
		return []any{map[string]any{"text": ""}}
	}
	return []any{map[string]any{"data": v.ToJSON(), "mediaType": mediaJSON}}
}

func newToken(prefix string) string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		panic("the system could not give random bytes")
	}
	return prefix + "-" + hex.EncodeToString(raw)
}
