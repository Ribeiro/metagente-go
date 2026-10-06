package serve

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Ribeiro/metagente-go/internal/clip"
)

// AccessLog writes one structured line (log/slog, key=value) for each request that
// comes in (requirement P5): when, from where, to which host it was addressed, what was
// asked, how it was answered and how long it took; which client it was, when its token has a
// name; and, for a request that ran an agent, which agent, which message, which task and how it ended.
//
// It writes nothing that could be a secret. Not the headers, so not the token; not
// the query; not the body. The values that come from the request are written the
// way slog writes them, quoted when they need it, so that a request that tries to
// forge a line of the log with a line break only writes a strange path.
func AccessLog(next http.Handler, out io.Writer) http.Handler {
	return accessLog(next, slog.New(slog.NewTextHandler(out, nil)), time.Now)
}

const (
	maxLoggedPath = 200
	maxLoggedHost = 100
)

func accessLog(next http.Handler, logger *slog.Logger, now func() time.Time) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := now()
		note := &accessNote{}
		r = r.WithContext(context.WithValue(r.Context(), noteKey{}, note))
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			// A request that panicked is logged too, and the panic goes on to the one
			// that catches it.
			attrs := []slog.Attr{
				slog.String("remote", clientKey(r.RemoteAddr)),
				slog.String("host", loggedHost(r)),
				slog.String("method", r.Method),
				slog.String("path", loggedPath(r)),
				slog.Int("status", rec.status),
				slog.Int64("bytes", rec.bytes),
				slog.Int64("duration_ms", now().Sub(start).Milliseconds()),
			}
			logger.LogAttrs(r.Context(), slog.LevelInfo, "request", append(attrs, note.attrs()...)...)
		}()
		next.ServeHTTP(rec, r)
	})
}

// loggedPath is the path of the request, and no more of it than fits in a line.
func loggedPath(r *http.Request) string {
	path := r.URL.Path
	if len(path) > maxLoggedPath {
		path = path[:maxLoggedPath] + "..."
	}
	return strings.ToValidUTF8(path, "?")
}

// loggedHost is the header Host of the request: it tells a request that came through the proxy
// from one that went around it, since behind a proxy all of them come from the same place. It
// comes from outside, so it is cleaned of what is not text and cut like the path is.
func loggedHost(r *http.Request) string {
	return clip.Collapse(r.Host, maxLoggedHost)
}

// ---------- what the server tells the log about a request ----------

type noteKey struct{}

// accessNote is what the server learns about a request while it answers, and the log
// writes after it did. A request that never reaches an agent leaves it empty.
type accessNote struct {
	mu      sync.Mutex
	client  string
	agent   string
	rpc     string
	message string
	task    string
	result  string
}

// noteOf is the note of a request, or nil when no log was installed. A nil note takes
// everything that is told to it and keeps nothing.
func noteOf(ctx context.Context) *accessNote {
	n, _ := ctx.Value(noteKey{}).(*accessNote)
	return n
}

func (n *accessNote) set(field *string, value string) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	*field = value
}

// setClient is the name of the token that opened the door; a token without a name says nothing.
func (n *accessNote) setClient(v string) {
	n.setField(func(a *accessNote) *string { return &a.client }, v)
}
func (n *accessNote) setAgent(v string) {
	n.setField(func(a *accessNote) *string { return &a.agent }, v)
}
func (n *accessNote) setRPC(v string) { n.setField(func(a *accessNote) *string { return &a.rpc }, v) }
func (n *accessNote) setMessage(v string) {
	n.setField(func(a *accessNote) *string { return &a.message }, v)
}
func (n *accessNote) setTask(v string) { n.setField(func(a *accessNote) *string { return &a.task }, v) }
func (n *accessNote) setResult(v string) {
	n.setField(func(a *accessNote) *string { return &a.result }, v)
}

func (n *accessNote) setField(field func(*accessNote) *string, v string) {
	if n == nil {
		return
	}
	n.set(field(n), v)
}

// attrs are the fields that were filled, in a fixed order.
func (n *accessNote) attrs() []slog.Attr {
	n.mu.Lock()
	defer n.mu.Unlock()
	var attrs []slog.Attr
	for _, f := range []struct{ key, value string }{
		{"client", n.client}, {"agent", n.agent}, {"rpc", n.rpc}, {"message", n.message}, {"task", n.task}, {"result", n.result},
	} {
		if f.value != "" {
			attrs = append(attrs, slog.String(f.key, f.value))
		}
	}
	return attrs
}

// How a message ended, as the log says it.
const (
	resultOK     = "ok"     // the agent answered
	resultFailed = "failed" // the request was understood and the agent could not answer
	resultError  = "error"  // the request was refused: it could not be understood or run
	resultBusy   = "busy"   // all the places for requests at the same time were taken
	resultLeft   = "left"   // the caller went away before the answer
)

// outcomeOf says how a SendMessage ended.
func outcomeOf(result any, e *rpcError) string {
	switch {
	case e == errServerBusy:
		return resultBusy
	case e != nil:
		return resultError
	case result == nil:
		return resultLeft
	}
	if m, ok := result.(map[string]any); ok {
		if _, failed := m["task"]; failed {
			return resultFailed
		}
	}
	return resultOK
}

type statusRecorder struct {
	http.ResponseWriter
	status      int
	bytes       int64
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status, s.wroteHeader = code, true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(p []byte) (int, error) {
	s.wroteHeader = true
	n, err := s.ResponseWriter.Write(p)
	s.bytes += int64(n)
	return n, err
}

// Listening describes the address a listener is on, for the line that tells where
// the server is.
func Listening(ln net.Listener) (host string, port int) {
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.IP.String(), addr.Port
	}
	return ln.Addr().String(), 0
}
