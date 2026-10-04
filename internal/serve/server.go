package serve

import (
	"context"
	"errors"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"time"

	"metagente/internal/applog"
)

// Config is how a server is set up.
type Config struct {
	// Token opens the door. See CheckToken.
	Token string
	// Hosts are the values of the Host header the server answers to.
	Hosts []string
	// BaseURL says how a client reaches the server, as the card must write it. It is
	// never taken from the request: by default it is the first of Hosts.
	BaseURL string
	// PublicCard gives the minimal card to anyone; without it, the card is read
	// only with the token.
	PublicCard bool
	// Version is written in the cards.
	Version string
	// AuthFailuresPerMinute is how many wrong tokens a place may try before it is
	// stopped for a minute. 10 when zero.
	AuthFailuresPerMinute int
	// NoThrottle turns off the slowing down of a place that fails often, for a
	// server behind a proxy that limits the rate itself. See Guard.NoThrottle.
	NoThrottle bool

	MaxCallDepth     int           // 8 when zero
	RequestTimeout   time.Duration // 5 minutes when zero
	MaxInFlight      int           // 16 when zero
	MaxConversations int           // 256 when zero
	ConversationTTL  time.Duration // 30 minutes when zero
	MaxBody          int64         // 1 MiB when zero

	// Log keeps the details of failures inside the server. May be nil.
	Log *applog.Log
}

// held is what a conversation keeps, and the agent it belongs to.
type held struct {
	agent string
	conv  Conversation
}

// Server serves agents over A2A. It is an http.Handler, so the one who starts it
// chooses the listener, the TLS and the timeouts.
type Server struct {
	cfg      Config
	guard    *Guard
	agents   map[string]Agent
	contexts *Contexts[*held]
	inflight chan struct{}
}

// New makes a server for the agents. It refuses a token that is not good enough:
// a server with a weak door is worse than no server.
func New(cfg Config, agents []Agent) (*Server, error) {
	if err := CheckToken(cfg.Token); err != nil {
		return nil, err
	}
	if len(cfg.Hosts) == 0 {
		return nil, errors.New("the server needs to know the hosts it answers to")
	}
	if len(agents) == 0 {
		return nil, errors.New("there is no agent to serve")
	}
	if cfg.MaxCallDepth <= 0 {
		cfg.MaxCallDepth = 8
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
	if cfg.ConversationTTL <= 0 {
		cfg.ConversationTTL = 30 * time.Minute
	}
	if cfg.Version == "" {
		cfg.Version = "0.0.0"
	}
	byName := make(map[string]Agent, len(agents))
	for _, agent := range agents {
		name := agent.Name()
		if !plainName(name) {
			return nil, errors.New("an agent has a name that cannot be used in an address")
		}
		if _, twice := byName[name]; twice {
			return nil, errors.New("two agents are called " + name)
		}
		byName[name] = agent
	}
	s := &Server{
		cfg:      cfg,
		guard:    NewGuard(cfg.Token, cfg.Hosts),
		agents:   byName,
		contexts: NewContexts[*held](cfg.MaxConversations, cfg.ConversationTTL),
		inflight: make(chan struct{}, cfg.MaxInFlight),
	}
	if cfg.MaxBody > 0 {
		s.guard.MaxBody = cfg.MaxBody
	}
	s.guard.NoThrottle = cfg.NoThrottle
	s.guard.MaxFailures = cfg.AuthFailuresPerMinute
	s.contexts.OnRemove = func(_ string, h *held) { h.conv.Close() }
	return s, nil
}

// WriteTimeout is how long the listener should allow for writing an answer: the
// longest a request may run, and a little more.
func (s *Server) WriteTimeout() time.Duration { return s.cfg.RequestTimeout + 30*time.Second }

// Names are the agents served, in order.
func (s *Server) Names() []string {
	names := make([]string, 0, len(s.agents))
	for name := range s.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Run keeps the server tidy (it forgets conversations nobody uses) until the
// context ends, and then lets go of everything that is left.
func (s *Server) Run(ctx context.Context) {
	s.contexts.Run(ctx, time.Minute)
	s.contexts.CloseAll()
}

// Close lets go of every conversation.
func (s *Server) Close() { s.contexts.CloseAll() }

type route int

const (
	routeNone route = iota
	routeRPC
	routeCard
)

const cardSuffix = "/.well-known/agent-card.json"

// route tells what a path is for. Only the exact paths of the agents that are
// served count: no cleaning, no trailing slash, no alias.
func (s *Server) route(path string) (route, string) {
	rest, ok := strings.CutPrefix(path, "/agents/")
	if !ok {
		return routeNone, ""
	}
	if name, isCard := strings.CutSuffix(rest, cardSuffix); isCard {
		if _, served := s.agents[name]; served {
			return routeCard, name
		}
		return routeNone, ""
	}
	if _, served := s.agents[rest]; served {
		return routeRPC, rest
	}
	return routeNone, ""
}

// ServeHTTP is the whole server. A path that is not an agent's is answered like
// any other request: with the token, a 404; without it, a 401. A stranger cannot
// tell which agents exist.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	defer s.recoverRequest(w, r)
	kind, name := s.route(r.URL.Path)
	switch kind {
	case routeRPC:
		agent := s.agents[name]
		s.guard.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			s.serveRPC(w, r, agent)
		})).ServeHTTP(w, r)
	case routeCard:
		agent := s.agents[name]
		full := !s.cfg.PublicCard
		serve := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, http.StatusOK, card(agent, s.baseURL()+"/agents/"+name, s.cfg.Version, full))
		})
		if s.cfg.PublicCard {
			s.guard.PublicGet(serve).ServeHTTP(w, r)
		} else {
			s.guard.ProtectGet(serve).ServeHTTP(w, r)
		}
	default:
		s.guard.Protect(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			refuse(w, http.StatusNotFound, "not found")
		})).ServeHTTP(w, r)
	}
}

// baseURL is how the card says the server is reached. It comes from the setup and never
// from the request: a request can say any Host that the server answers to, and the
// card must not change with it.
func (s *Server) baseURL() string {
	if s.cfg.BaseURL != "" {
		return strings.TrimRight(s.cfg.BaseURL, "/")
	}
	return "http://" + s.cfg.Hosts[0]
}

// recoverRequest keeps a failure inside one request from taking the server down
// (requirement P4). The cause goes to the log, and the caller reads one sentence.
func (s *Server) recoverRequest(w http.ResponseWriter, r *http.Request) {
	rec := recover()
	if rec == nil {
		return
	}
	if rec == http.ErrAbortHandler {
		panic(rec)
	}
	if s.cfg.Log != nil {
		_, _ = s.cfg.Log.Panic("http "+r.Method, rec, debug.Stack())
	}
	refuse(w, http.StatusInternalServerError, "something went wrong inside the server")
}
