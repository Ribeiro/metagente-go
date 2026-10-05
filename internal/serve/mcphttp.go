package serve

import (
	"context"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// MCPPath is where the agents are served as MCP tools, when they are.
const MCPPath = "/mcp"

// MCP over HTTP (requirement S10). The agents are the same tools as on standard input
// and output, served with the streamable HTTP of the SDK at MCPPath, behind the door
// of the A2A server: the Host, no browser, the token, the size of the body, and the
// same limits of calls and conversations, which both protocols share.
//
// What it does not do, on purpose:
//
//   - It sends nothing on its own. An answer comes in the answer to the POST, as JSON,
//     and a GET, which would open a stream for messages of the server, is refused.
//   - It keeps no messages to send again: a client that lost an answer asks again.
//
// A session is a conversation with each agent, as on standard input and output. The
// server issues its id, a session that is not used for the time of a conversation
// ends, a DELETE ends it at once, and there are no more sessions at once than there
// may be conversations. When a session ends, what its agents kept is let go.

// noteExtra is the key, in the information about the token that the SDK gives to a
// tool, of the note of the access log of the request.
const noteExtra = "metagente.note"

type mcpHTTP struct {
	mcp         *MCPServer
	handler     http.Handler
	maxSessions int

	mu      sync.Mutex
	opening int // sessions that are being made, not counted by the SDK yet
}

// addMCP makes the MCP server of the agents, sharing the places of this one.
func (s *Server) addMCP(agents []Agent) error {
	m, err := NewMCP(MCPConfig{
		Version:        s.cfg.Version,
		RequestTimeout: s.cfg.RequestTimeout,
		MaxCallDepth:   s.cfg.MaxCallDepth,
		Log:            s.cfg.Log,
		running:        s.inflight,
		room:           s.room,
		makeRoom:       func() { s.contexts.Sweep() },
	}, agents)
	if err != nil {
		return err
	}
	server := m.Server()
	handler := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return server }, &sdk.StreamableHTTPOptions{
		JSONResponse:   true,
		SessionTimeout: s.cfg.ConversationTTL,
		// The door checks the Host against the names the server answers to, which is
		// stricter than this check of the SDK, and right behind a proxy, where it is not.
		DisableLocalhostProtection:   true,
		MaxRequestBodyBytes:          s.guard.bodyLimit(),
		PropagateRequestCancellation: true,
	})
	s.mcp = &mcpHTTP{mcp: m, handler: carryNote(handler), maxSessions: s.cfg.MaxConversations}
	return nil
}

// carryNote gives the tools the note of the access log of their request. The SDK runs
// a tool in the context of its session, not of the request, and the information about
// the token is what it passes from the request to the tool. The token was checked by
// the door before this, so the verifier only says who it is.
func carryNote(next http.Handler) http.Handler {
	verify := func(_ context.Context, _ string, r *http.Request) (*auth.TokenInfo, error) {
		return &auth.TokenInfo{UserID: "bearer", Extra: map[string]any{noteExtra: noteOf(r.Context())}}, nil
	}
	return auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(next)
}

// mcpNote is the note of the access log of the request of a call, or nil.
func mcpNote(req *sdk.CallToolRequest) *accessNote {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return nil
	}
	note, _ := req.Extra.TokenInfo.Extra[noteExtra].(*accessNote)
	return note
}

// ServeHTTP is behind the door: who asks, the method, the type and the size of the
// body were checked.
func (h *mcpHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	noteOf(r.Context()).setRPC("mcp")
	if r.Method == http.MethodPost && r.Header.Get("Mcp-Session-Id") == "" {
		if !h.reserve() {
			noteOf(r.Context()).setResult(resultBusy)
			w.Header().Set("Retry-After", "1")
			refuse(w, http.StatusServiceUnavailable, "the server holds as many sessions as it may; try again later")
			return
		}
		defer h.unreserve()
	}
	h.handler.ServeHTTP(w, r)
}

// reserve keeps the place of a session that a request is about to open.
func (h *mcpHTTP) reserve() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sessions()+h.opening >= h.maxSessions {
		return false
	}
	h.opening++
	return true
}

func (h *mcpHTTP) unreserve() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.opening--
}

func (h *mcpHTTP) sessions() int {
	n := 0
	for range h.mcp.Server().Sessions() {
		n++
	}
	return n
}

// close ends every session, and lets go of every conversation of MCP.
func (h *mcpHTTP) close() {
	h.mcp.stop() // first, or a session would wait for its calls to end
	for session := range h.mcp.Server().Sessions() {
		_ = session.Close()
	}
	h.mcp.Close()
}
