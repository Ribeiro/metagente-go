package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/diag"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

const (
	maxRedirects   = 5
	connectTimeout = 10 * time.Second
)

// blockedError is the refusal of the network guard.
type blockedError struct {
	address string
	reason  string
}

func (e *blockedError) Error() string {
	return "connection to " + e.address + " refused: " + e.reason
}

// notAllowedError is the refusal of the domain allow list.
type notAllowedError struct{ host string }

func (e *notAllowedError) Error() string {
	return "the domain " + e.host + " is not in the allow list"
}

var errTooManyRedirects = errors.New("too many redirects")

// reservedPrefixes are ranges that are not the public internet and that
// netip does not call private.
var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), // shared address space of carriers
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

// refusal says why an address may not be reached, or returns "" when it may.
//
// Link-local addresses (where cloud metadata services live), multicast and
// unspecified addresses are never allowed. Loopback, private networks and
// the reserved ranges are refused unless the agent declared
// `tool http allow private`.
func refusal(addr netip.Addr, allowPrivate bool) string {
	switch {
	case addr.IsUnspecified():
		return "it is not a real address"
	case addr.IsMulticast():
		return "it is a multicast address"
	case addr.IsLinkLocalUnicast():
		return "it is a link-local address, where cloud metadata services live"
	}
	if allowPrivate {
		return ""
	}
	switch {
	case addr.IsLoopback():
		return "it is this computer"
	case addr.IsPrivate():
		return "it is on a private network"
	}
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(addr) {
			return "it is in a reserved range of addresses"
		}
	}
	return ""
}

// checkAddress is the test of the guard, on the address the system is about to
// connect to ("ip:port"). It runs after the name was resolved, so a name that
// points to an internal address is refused as well.
func checkAddress(address string, allowPrivate bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return &blockedError{address: address, reason: "the address could not be read"}
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return &blockedError{address: address, reason: "the address could not be read"}
	}
	if why := refusal(addr.Unmap(), allowPrivate); why != "" {
		return &blockedError{address: addr.Unmap().String(), reason: why}
	}
	return nil
}

// HTTP is `tool http`: web requests that cannot reach internal networks
// unless the agent says so (H1), may be limited to some domains (H2), and have
// a ceiling on the size of the answer (H3).
type HTTP struct {
	decl     *lang.ToolDecl
	client   *http.Client
	maxBytes int64
}

// NewHTTP creates the tool.
func NewHTTP(decl *lang.ToolDecl, limits config.Limits) *HTTP {
	h := &HTTP{decl: decl, maxBytes: limits.MaxHTTPBytes}
	dialer := &net.Dialer{
		Timeout: connectTimeout,
		Control: func(network, address string, _ syscall.RawConn) error {
			return checkAddress(address, decl.AllowPrivate)
		},
	}
	// Proxy stays nil on purpose: a proxy taken from the environment would
	// receive the connection instead of the guard seeing the real address.
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         dialer.DialContext,
		TLSHandshakeTimeout: connectTimeout,
	}
	h.client = &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errTooManyRedirects
			}
			if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
				return errors.New("the redirect leads to something that is not a web address")
			}
			if !h.hostAllowed(req.URL.Hostname()) {
				return &notAllowedError{host: req.URL.Hostname()}
			}
			return nil
		},
	}
	return h
}

func (h *HTTP) Name() string { return h.decl.Name }

func (h *HTTP) Actions(context.Context) ([]lang.ActionInfo, error) {
	return lang.BuiltinActions(h.decl), nil
}

// hostAllowed applies the domains of `allow`. With no domains, any host is
// allowed (the guard still refuses internal addresses). `*.example.com`
// matches the subdomains of example.com, not example.com itself.
func (h *HTTP) hostAllowed(host string) bool {
	if len(h.decl.Allow) == 0 {
		return true
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, pattern := range h.decl.Allow {
		pattern = strings.ToLower(pattern)
		if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
			if strings.HasSuffix(host, "."+suffix) {
				return true
			}
		} else if host == pattern {
			return true
		}
	}
	return false
}

func (h *HTTP) Call(ctx context.Context, action string, args Args) (value.Value, error) {
	switch action {
	case "get":
		address, err := NeedText("http", "get", args, "url")
		if err != nil {
			return value.Nothing, err
		}
		return h.do(ctx, http.MethodGet, address, nil, "")
	case "post":
		if h.decl.ReadOnly {
			return value.Nothing, readOnlyError("http", "post")
		}
		address, err := NeedText("http", "post", args, "url")
		if err != nil {
			return value.Nothing, err
		}
		body, contentType, err := encodeBody(args["body"])
		if err != nil {
			return value.Nothing, err
		}
		return h.do(ctx, http.MethodPost, address, body, contentType)
	}
	return value.Nothing, UnknownAction("http", action, actionNames(lang.BuiltinActions(h.decl)))
}

func encodeBody(v value.Value) (io.Reader, string, error) {
	switch v.Kind {
	case value.KindNothing:
		return nil, "", nil
	case value.KindText:
		return strings.NewReader(v.Text), "", nil
	default:
		encoded, err := json.Marshal(v.ToJSON())
		if err != nil {
			return nil, "", diag.New("I could not turn the body into JSON").
				Fix("send a text, or a record or list made of texts, numbers and yes/no values")
		}
		return bytes.NewReader(encoded), "application/json", nil
	}
}

func (h *HTTP) do(ctx context.Context, method, address string, body io.Reader, contentType string) (value.Value, error) {
	parsed, err := url.Parse(address)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return value.Nothing, diag.Newf("`%s` is not a web address", address).
			Fix("start the address with http:// or https://")
	}
	if !h.hostAllowed(parsed.Hostname()) {
		return value.Nothing, notAllowed(parsed.Hostname())
	}
	req, err := http.NewRequestWithContext(ctx, method, address, body)
	if err != nil {
		return value.Nothing, diag.Newf("`%s` is not a web address", address).
			Fix("start the address with http:// or https://")
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("User-Agent", "metagente")

	resp, err := h.client.Do(req)
	if err != nil {
		return value.Nothing, h.requestError(address, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, h.maxBytes+1))
	if err != nil {
		return value.Nothing, diag.Newf("%s answered, but I could not read the answer as text", address)
	}
	if int64(len(data)) > h.maxBytes {
		return value.Nothing, diag.Newf("the answer from %s is larger than the %d bytes an answer may have here", address, h.maxBytes).
			Fix("ask for less data, or raise max_http_bytes in the [limits] section of metagente.toml")
	}
	text := strings.ToValidUTF8(string(data), "\uFFFD")
	parsedJSON := value.Nothing
	var j any
	if json.Unmarshal(data, &j) == nil {
		parsedJSON = value.FromJSON(j)
	}
	return value.Record(map[string]value.Value{
		"status": value.Number(float64(resp.StatusCode)),
		"text":   value.Text(text),
		"json":   parsedJSON,
	}), nil
}

func notAllowed(host string) error {
	return diag.Newf("`http` may only reach the domains this agent declared, and `%s` is not one of them", host).
		Fixf("add it to the declaration, for example: tool http allow \"%s\"", host)
}

// requestError turns a failure of the request into a sentence.
func (h *HTTP) requestError(address string, err error) error {
	var blocked *blockedError
	var denied *notAllowedError
	var netErr net.Error
	switch {
	case errors.As(err, &blocked):
		return diag.Newf("`http` refused to connect to %s: %s", blocked.address, blocked.reason).
			Fix("if this agent really needs to reach a private network, declare it: tool http allow private")
	case errors.As(err, &denied):
		return notAllowed(denied.host)
	case errors.Is(err, errTooManyRedirects):
		return diag.Newf("the address %s redirected more than %d times", address, maxRedirects).
			Fix("check the address; it may redirect in a circle")
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return diag.Newf("I could not reach %s: the server took too long", address).
			Fix("check the address and your internet connection")
	default:
		why := "the request did not complete"
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			why = "the connection failed"
		}
		return diag.New(fmt.Sprintf("I could not reach %s: %s", address, why)).
			Fix("check the address and your internet connection")
	}
}
