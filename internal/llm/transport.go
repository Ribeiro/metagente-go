package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"metagente/internal/clip"
	"metagente/internal/diag"
)

// Settings is everything a provider needs.
type Settings struct {
	// BaseURL is where requests go, with no trailing slash.
	BaseURL string
	Model   string
	APIKey  string
	// MaxTokensField is for OpenAI compatible servers: "max_tokens" or
	// "max_completion_tokens". Empty chooses by address.
	MaxTokensField string
	// WorkspaceID is sent as `anthropic-workspace-id` when it is set.
	WorkspaceID string
	// HTTPClient is used by tests. The default never follows redirects and never
	// reads a proxy from the environment.
	HTTPClient *http.Client
	// Retries is how many times a failure that may pass is tried again. Zero
	// means the default of 2; a negative number means never.
	Retries int
	// Wait is the pause before a new try. The default grows 0.5 s, 1 s, 2 s, ...
	Wait func(attempt int, hint time.Duration) time.Duration
	// MaxResponse is the most bytes read from an answer. Zero means 8 MiB.
	MaxResponse int64
}

func (s Settings) retries() int {
	switch {
	case s.Retries == 0:
		return 2
	case s.Retries < 0:
		return 0
	}
	return s.Retries
}

func (s Settings) maxResponse() int64 {
	if s.MaxResponse > 0 {
		return s.MaxResponse
	}
	return 8 << 20
}

func defaultWait(attempt int, hint time.Duration) time.Duration {
	wait := 500 * time.Millisecond << attempt
	if hint > wait {
		wait = hint
	}
	if wait > 10*time.Second {
		wait = 10 * time.Second
	}
	return wait
}

// newHTTPClient is the client for models: no proxy from the environment, a limit
// to connect, and no redirects. A redirect could carry the key to an address
// nobody approved, and a model provider has no reason to redirect.
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

// parseBase checks the address a key will be sent to. The key goes over
// https; plain http is for a server on this very machine.
func parseBase(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, diag.Newf("the address of the language model, %s, is not valid", shownAddress(raw)).
			Fix("write it like: base_url = \"https://api.example.com\" in the [llm] section of metagente.toml")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		// Not repeated: a password or a token may be in it.
		return nil, diag.New("the address of the language model may not hold a user name, a query or a fragment").
			Fix("give only the address, for example https://api.example.com/v1")
	}
	if u.Scheme == "http" && !isLoopback(u.Hostname()) {
		return nil, diag.Newf("the address of the language model, `%s`, would send your key without encryption", raw).
			Fix("use https://, or run the model on this same computer (localhost)")
	}
	return u, nil
}

// shownAddress is an address as it may be repeated in a message: not when it
// could hold a secret.
func shownAddress(raw string) string {
	if strings.ContainsAny(raw, "@?#") {
		return "(not shown, because it may hold a secret)"
	}
	return "`" + raw + "`"
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// transport posts JSON to a provider.
type transport struct {
	client      *http.Client
	host        string
	secrets     []string
	retries     int
	wait        func(int, time.Duration) time.Duration
	maxResponse int64
}

func newTransport(s Settings, u *url.URL) *transport {
	client := s.HTTPClient
	if client == nil {
		client = newHTTPClient()
	}
	wait := s.Wait
	if wait == nil {
		wait = defaultWait
	}
	return &transport{
		client:      client,
		host:        u.Host,
		secrets:     []string{s.APIKey},
		retries:     s.retries(),
		wait:        wait,
		maxResponse: s.maxResponse(),
	}
}

func (t *transport) post(ctx context.Context, address string, headers map[string]string, body []byte) ([]byte, error) {
	for attempt := 0; ; attempt++ {
		raw, hint, failure := t.once(ctx, address, headers, body)
		if failure == nil {
			return raw, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if !failure.Retry || attempt >= t.retries {
			return nil, failure
		}
		select {
		case <-time.After(t.wait(attempt, hint)):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (t *transport) once(ctx context.Context, address string, headers map[string]string, body []byte) ([]byte, time.Duration, *Error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, address, bytes.NewReader(body))
	if err != nil {
		return nil, 0, &Error{Message: "the address of the language model is not valid"}
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		if value != "" {
			req.Header.Set(key, value)
		}
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, 0, t.connectionError(err)
	}
	defer resp.Body.Close()

	raw, failure := t.readAnswer(resp)
	if failure != nil {
		return nil, 0, failure
	}
	if resp.StatusCode/100 == 2 {
		return raw, 0, nil
	}
	hint, failure := t.statusError(resp, raw)
	return nil, hint, failure
}

// connectionError says what went wrong with the connection. A Go error about a request repeats its
// address, which is fine here, but it can repeat more than we want to show, so only the cause is said.
func (t *transport) connectionError(err error) *Error {
	reason := "the connection failed"
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		reason = "it took too long"
	}
	return &Error{Message: fmt.Sprintf("I could not reach %s: %s", t.host, reason), Retry: true}
}

// readAnswer reads the body of an answer, no more than the limit.
func (t *transport) readAnswer(resp *http.Response) ([]byte, *Error) {
	raw, err := io.ReadAll(io.LimitReader(resp.Body, t.maxResponse+1))
	if err != nil {
		return nil, &Error{Message: fmt.Sprintf("the connection with %s was cut while it answered", t.host), Retry: true}
	}
	if int64(len(raw)) > t.maxResponse {
		return nil, &Error{Message: fmt.Sprintf("the answer of %s is larger than %d bytes", t.host, t.maxResponse)}
	}
	return raw, nil
}

// statusError is the error of an answer that is not a success, and how long the server asked to wait.
func (t *transport) statusError(resp *http.Response, raw []byte) (time.Duration, *Error) {
	if resp.StatusCode/100 == 3 {
		return 0, &Error{Status: resp.StatusCode,
			Message: fmt.Sprintf("%s answered %d: it redirected the request, and Metagente never follows a redirect with a key", t.host, resp.StatusCode)}
	}
	why := errorText(raw)
	if why == "" {
		why = "no details were given"
	}
	message := Redact(fmt.Sprintf("%s answered %d: %s", t.host, resp.StatusCode, clip.Collapse(why, 300)), t.secrets...)
	return retryAfter(resp), &Error{Status: resp.StatusCode, Message: message, Retry: worthRetrying(resp.StatusCode), Hint: hintFor(resp.StatusCode, why)}
}

// worthRetrying is true for the answers that mean "not now" and not "no".
func worthRetrying(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 529:
		return true
	}
	return false
}

// retryAfter is how long the server asked to wait, in the header that says it; zero if it did not.
func retryAfter(resp *http.Response) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	return 0
}

// hintFor says what to do about the answers people meet most when they set up.
func hintFor(status int, why string) string {
	switch {
	case status == http.StatusUnauthorized:
		return "check the [llm] settings in metagente.toml: the provider does not accept this key, so the variable may hold one that was deleted or mistyped"
	case status == http.StatusBadRequest && strings.Contains(why, "anthropic-workspace-id"):
		return "this key is not tied to one workspace: add workspace_id = \"wrkspc_...\" to the [llm] section of metagente.toml (the id is in the Console, under Settings, Workspaces), or use a key created inside a workspace"
	case status == http.StatusForbidden:
		return "the key is valid but not allowed to do this: check its permissions and the workspace it belongs to"
	case status == http.StatusNotFound:
		return "check that the model name in the [llm] section of metagente.toml exists for this provider"
	}
	return ""
}

// errorText reads the reason a provider gives, in the shapes the common ones use.
func errorText(raw []byte) string {
	var shape struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(raw, &shape) != nil {
		return ""
	}
	if len(shape.Error) > 0 {
		var text string
		if json.Unmarshal(shape.Error, &text) == nil && text != "" {
			return text
		}
		var object struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(shape.Error, &object) == nil && object.Message != "" {
			return object.Message
		}
	}
	return shape.Message
}
