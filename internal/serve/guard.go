package serve

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Guard is the door of the server. Everything that comes in goes through it
// before any agent hears of it, and what it checks is, in order: that the request
// was meant for this server (Host, S2), that it did not come from a page in a
// browser (Origin, S3), that it has the token (S1), and that it is the kind of
// request the server answers (method, type, size, S4 and S6).
//
// The door answers with as little as it can. A refusal never says what was wrong
// with the token, and nothing here ever says that another site may call the
// server: there is no CORS.
type Guard struct {
	// Token is the bearer token that opens the door.
	Token string
	// Hosts are the values of the Host header the server answers to.
	Hosts []string
	// MaxBody is the most bytes a request may carry. 1 MiB when zero.
	MaxBody int64
	// Now is the clock; time.Now when nil.
	Now func() time.Time
	// NoThrottle turns off the slowing down of a place that fails often. It is for
	// a server behind a proxy: every request comes from the address of the proxy,
	// so a stranger who could fail often enough would stop everyone. The proxy has to
	// limit the rate itself.
	NoThrottle bool
	// MaxFailures is how many wrong tokens a place may try in a minute before it is
	// stopped for a minute. 10 when zero.
	MaxFailures int

	hostSet  map[string]bool
	mu       sync.Mutex
	failures map[string]*failure
}

const (
	defaultMaxFailures = 10
	failureWindow      = time.Minute
	blockFor           = time.Minute
	maxTrackedClients  = 1024
	defaultMaxBody     = 1 << 20
)

type failure struct {
	count        int
	since        time.Time
	blockedUntil time.Time
}

// NewGuard makes the door for a token and the hosts the server answers to.
func NewGuard(token string, hosts []string) *Guard {
	g := &Guard{Token: token, Hosts: hosts, MaxBody: defaultMaxBody, Now: time.Now, hostSet: map[string]bool{}, failures: map[string]*failure{}}
	for _, h := range hosts {
		g.hostSet[strings.ToLower(h)] = true
	}
	return g
}

// LoopbackHosts are the Host values of a server on this computer, the ones a
// person types.
func LoopbackHosts(port int) []string {
	p := strconv.Itoa(port)
	return []string{"127.0.0.1:" + p, "localhost:" + p, "[::1]:" + p}
}

// Protect is the door of the endpoint that takes JSON-RPC: a POST of JSON with
// the token.
func (g *Guard) Protect(next http.Handler) http.Handler {
	return g.protect(next, http.MethodPost, true)
}

// ProtectGet is the door of a page that is read, with the token.
func (g *Guard) ProtectGet(next http.Handler) http.Handler {
	return g.protect(next, http.MethodGet, false)
}

// ProtectMCP is the door of the MCP endpoint: the same as the one of JSON-RPC, and a
// DELETE, with the token, that ends a session. A GET, which would open a stream of
// messages from the server, is refused: this server sends none (the specification
// of MCP lets a server answer it with 405).
func (g *Guard) ProtectMCP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		safeHeaders(w)
		if !g.admit(w, r) {
			return
		}
		if r.URL.RawQuery != "" {
			refuse(w, http.StatusBadRequest, "query strings are not accepted")
			return
		}
		switch r.Method {
		case http.MethodPost:
			if !g.acceptBody(w, r) {
				return
			}
		case http.MethodDelete:
			r.Body = http.MaxBytesReader(w, r.Body, g.bodyLimit())
		default:
			w.Header().Set("Allow", "POST, DELETE")
			refuse(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PublicGet is the door of a page anyone may read: no token, but still only for
// this host, only to GET, and never to a page of a browser.
func (g *Guard) PublicGet(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		safeHeaders(w)
		if !g.hostAllowed(r.Host) {
			hangUp(w)
			refuse(w, http.StatusMisdirectedRequest, "unexpected host")
			return
		}
		if madeByABrowser(r) {
			hangUp(w)
			refuse(w, http.StatusForbidden, "browser requests are not accepted")
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			refuse(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if r.URL.RawQuery != "" {
			refuse(w, http.StatusBadRequest, "query strings are not accepted")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (g *Guard) protect(next http.Handler, method string, jsonBody bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		safeHeaders(w)
		// The order is part of the security: who is asking comes first, and only
		// then anything that tells something about the server.
		if g.admit(w, r) && g.wellFormed(w, r, method, jsonBody) {
			next.ServeHTTP(w, r)
		}
	})
}

// admit decides whether the one who asks may be answered at all: the request must
// be meant for this server, must not come from a page in a browser, and must carry
// the token. It answers the refusal itself, and says whether to go on.
func (g *Guard) admit(w http.ResponseWriter, r *http.Request) bool {
	if !g.hostAllowed(r.Host) {
		hangUp(w)
		refuse(w, http.StatusMisdirectedRequest, "unexpected host")
		return false
	}
	if madeByABrowser(r) {
		hangUp(w)
		refuse(w, http.StatusForbidden, "browser requests are not accepted")
		return false
	}
	return g.authenticate(w, r)
}

// authenticate checks the token, and slows down a place that gets it wrong too
// often. The answer to a wrong token is always the same one, and a place that is
// being slowed down is not even asked for the token.
func (g *Guard) authenticate(w http.ResponseWriter, r *http.Request) bool {
	client := addressGroup(r.RemoteAddr)
	if wait, blocked := g.blocked(client); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait/time.Second)+1))
		hangUp(w)
		refuse(w, http.StatusTooManyRequests, "too many failed attempts; wait and try again")
		return false
	}
	if !g.authentic(r) {
		g.fail(client)
		w.Header().Set("WWW-Authenticate", "Bearer")
		hangUp(w)
		refuse(w, http.StatusUnauthorized, "unauthorized")
		return false
	}
	g.succeed(client)
	return true
}

// wellFormed checks that the request is the kind the server answers, once it is
// known who is asking: no query string, the right method and, for a request that
// carries JSON, the right type and a body that is not too large.
func (g *Guard) wellFormed(w http.ResponseWriter, r *http.Request, method string, jsonBody bool) bool {
	if r.URL.RawQuery != "" {
		refuse(w, http.StatusBadRequest, "query strings are not accepted")
		return false
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		refuse(w, http.StatusMethodNotAllowed, "method not allowed")
		return false
	}
	return !jsonBody || g.acceptBody(w, r)
}

// acceptBody lets a JSON body through, and makes sure that it is read no further
// than the limit: a body that lies about its length is cut when it is read.
func (g *Guard) acceptBody(w http.ResponseWriter, r *http.Request) bool {
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != mediaJSON {
		refuse(w, http.StatusUnsupportedMediaType, "content type must be application/json")
		return false
	}
	limit := g.bodyLimit()
	if r.ContentLength > limit {
		refuse(w, http.StatusRequestEntityTooLarge, "request too large")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	return true
}

func (g *Guard) bodyLimit() int64 {
	if g.MaxBody > 0 {
		return g.MaxBody
	}
	return defaultMaxBody
}

func (g *Guard) now() time.Time {
	if g.Now == nil {
		return time.Now()
	}
	return g.Now()
}

func (g *Guard) hostAllowed(host string) bool { return g.hostSet[strings.ToLower(host)] }

// madeByABrowser is true for a request that carries what only a browser adds. The clients of this
// server are programs, so a page in a browser is only ever someone else's page trying to reach a
// server on this computer.
//
// Two things tell a browser: the header Origin, and the metadata of the fetch, which a browser adds
// to every request and a page can neither leave out nor change: Sec-Fetch-Site, Sec-Fetch-Dest and
// Sec-Fetch-User. Sec-Fetch-Mode alone does not tell it. The fetch of Node.js sends it in every
// request, and the clients of the official SDKs in JavaScript are made on that fetch, while a
// browser sends it together with Sec-Fetch-Site and is caught by that one.
func madeByABrowser(r *http.Request) bool {
	if _, ok := r.Header["Origin"]; ok {
		return true
	}
	for name := range r.Header {
		switch strings.ToLower(name) {
		case "sec-fetch-site", "sec-fetch-dest", "sec-fetch-user":
			return true
		}
	}
	return false
}

// authentic compares the token without the time depending on how much of it was
// right. Both sides are hashed first, so even their lengths do not show.
func (g *Guard) authentic(r *http.Request) bool {
	given, ok := bearer(r.Header.Get("Authorization"))
	a := sha256.Sum256([]byte(given))
	b := sha256.Sum256([]byte(g.Token))
	same := subtle.ConstantTimeCompare(a[:], b[:]) == 1
	return ok && same
}

// bearer takes the token out of `Bearer <token>`. The scheme is not case
// sensitive; the rest is taken as it is.
func bearer(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}

// addressGroup is who a place is, for the limits that count places: an IPv4 address, or
// the network of 64 bits of an IPv6 one. A single network of IPv6 has billions of
// addresses, and they all belong to whoever has the network.
func addressGroup(remoteAddr string) string {
	host := clientKey(remoteAddr)
	ip := net.ParseIP(host)
	if ip == nil || ip.To4() != nil {
		return host
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

func clientKey(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// blocked says whether a client has failed too often, and for how much longer it
// has to wait.
func (g *Guard) blocked(client string) (time.Duration, bool) {
	if g.NoThrottle {
		return 0, false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	f := g.failures[client]
	if f == nil {
		return 0, false
	}
	now := g.now()
	if now.Before(f.blockedUntil) {
		return f.blockedUntil.Sub(now), true
	}
	if !f.blockedUntil.IsZero() || now.Sub(f.since) > failureWindow {
		delete(g.failures, client)
	}
	return 0, false
}

func (g *Guard) maxFailures() int {
	if g.MaxFailures > 0 {
		return g.MaxFailures
	}
	return defaultMaxFailures
}

func (g *Guard) fail(client string) {
	if g.NoThrottle {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	f := g.failures[client]
	if f == nil || now.Sub(f.since) > failureWindow {
		if f == nil && !g.makeRoom(now) {
			return // every place remembered is stopped; this one cannot be counted
		}
		f = &failure{since: now}
		g.failures[client] = f
	}
	f.count++
	if f.count >= g.maxFailures() {
		f.blockedUntil = now.Add(blockFor)
	}
}

func (g *Guard) succeed(client string) {
	if g.NoThrottle {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.failures, client)
}

// makeRoom keeps the list of clients that failed from growing without end: what
// has run out goes first, and then the ones that are not stopped. A client that is
// stopped is never forgotten before its time, or failing from many other addresses
// would set it free. It says false when every place is taken by a stopped client:
// then the new one is not counted, which only means that it is not stopped yet.
func (g *Guard) makeRoom(now time.Time) bool {
	if len(g.failures) < maxTrackedClients {
		return true
	}
	for key, f := range g.failures {
		if !now.Before(f.blockedUntil) && now.Sub(f.since) > failureWindow {
			delete(g.failures, key)
		}
	}
	for key, f := range g.failures {
		if len(g.failures) < maxTrackedClients {
			break
		}
		if !now.Before(f.blockedUntil) {
			delete(g.failures, key)
		}
	}
	return len(g.failures) < maxTrackedClients
}

// hangUp closes the connection after the answer. A client that the door turned away
// has no reason to keep it, and a connection kept idle is one of the few that the
// server may hold (max_connections), so a stranger could otherwise fill them all.
func hangUp(w http.ResponseWriter) { w.Header().Set("Connection", "close") }

func safeHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

// refuse answers a refusal with a fixed, short text.
func refuse(w http.ResponseWriter, code int, text string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = fmt.Fprintln(w, text)
}
