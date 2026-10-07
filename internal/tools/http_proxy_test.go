package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
)

// fakeProxy is a web proxy for the tests. A plain request it answers itself,
// saying what it was asked; a CONNECT it either refuses with refuse, or joins to
// tunnel, whatever name was asked for.
type fakeProxy struct {
	*httptest.Server
	refuse int
	tunnel string

	mu   sync.Mutex
	seen []string // the addresses it was asked for
	auth []string // the Proxy-Authorization it got
}

func newFakeProxy(t *testing.T) *fakeProxy {
	t.Helper()
	p := &fakeProxy{}
	p.Server = httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(p.Close)
	return p
}

func (p *fakeProxy) serve(w http.ResponseWriter, r *http.Request) {
	target := r.URL.String()
	if r.Method == http.MethodConnect {
		target = r.Host
	}
	p.mu.Lock()
	p.seen = append(p.seen, target)
	p.auth = append(p.auth, r.Header.Get("Proxy-Authorization"))
	p.mu.Unlock()
	if p.refuse != 0 {
		w.WriteHeader(p.refuse)
		return
	}
	if r.Method != http.MethodConnect {
		fmt.Fprintf(w, "through the proxy: %s", target)
		return
	}
	upstream, err := net.Dial("tcp", p.tunnel)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusOK)
	conn, _, err := http.NewResponseController(w).Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	go func() { io.Copy(upstream, conn); upstream.Close() }()
	go func() { io.Copy(conn, upstream); conn.Close() }()
}

func (p *fakeProxy) asked() (seen, auth []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.seen...), append([]string(nil), p.auth...)
}

// viaProxy is `tool http` through the proxy, with names found as dns says.
func viaProxy(t *testing.T, decl lang.ToolDecl, proxy *url.URL, dns map[string]string) *HTTP {
	t.Helper()
	decl.Name, decl.Kind = "http", lang.ToolHTTP
	h := NewHTTP(&decl, config.Default().Limits, proxy)
	h.lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		addr, ok := dns[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{netip.MustParseAddr(addr)}, nil
	}
	return h
}

func proxyURL(t *testing.T, p *fakeProxy) *url.URL {
	t.Helper()
	u, err := url.Parse(p.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestARequestGoesThroughTheProxyOfTheConfigurationWithItsPassword(t *testing.T) {
	p := newFakeProxy(t)
	proxy := proxyURL(t, p)
	proxy.User = url.UserPassword("ana", "s3cret")
	// The proxy itself is on this computer, which the guard would refuse as a destination.
	h := viaProxy(t, lang.ToolDecl{}, proxy, map[string]string{"api.example.com": "93.184.215.14"})

	got, err := h.Call(context.Background(), "get", args("url", "http://api.example.com/weather?city=Recife"))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if text := fieldPath(t, got, "text"); text.Text != "through the proxy: http://api.example.com/weather?city=Recife" {
		t.Errorf("text = %s", text.Display())
	}
	_, auth := p.asked()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("ana:s3cret"))
	if len(auth) != 1 || auth[0] != want {
		t.Errorf("Proxy-Authorization = %q", auth)
	}
}

func TestAnHTTPSRequestIsTunnelledThroughTheProxy(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"host":%q}`, r.Host)
	}))
	t.Cleanup(server.Close)
	p := newFakeProxy(t)
	p.tunnel = server.Listener.Addr().String()
	h := viaProxy(t, lang.ToolDecl{}, proxyURL(t, p), map[string]string{"example.com": "93.184.215.14"})
	// The certificate of the test server is for example.com.
	h.client.Transport.(*http.Transport).TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig

	got, err := h.Call(context.Background(), "get", args("url", "https://example.com/"))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if host := fieldPath(t, got, "json", "host"); host.Text != "example.com" {
		t.Errorf("host = %s", host.Display())
	}
	if seen, _ := p.asked(); len(seen) != 1 || seen[0] != "example.com:443" {
		t.Errorf("the proxy was asked for %q", seen)
	}
}

func TestThroughAProxyTheGuardStillRefusesInternalDestinations(t *testing.T) {
	p := newFakeProxy(t)
	dns := map[string]string{
		"intranet.example.com": "10.1.2.3",
		"metadata.example.com": "169.254.169.254",
		"mapped.example.com":   "::ffff:192.168.0.10",
	}
	h := viaProxy(t, lang.ToolDecl{}, proxyURL(t, p), dns)
	for address, want := range map[string]string{
		"http://10.0.0.1/":                "refused to connect to 10.0.0.1: it is on a private network",
		"http://[::1]:8080/":              "refused to connect to ::1: it is this computer",
		"http://localhost/":               "refused to connect to localhost: it is this computer",
		"http://api.localhost./":          "refused to connect to api.localhost: it is this computer",
		"http://intranet.example.com/":    "intranet.example.com (10.1.2.3): it is on a private network",
		"http://mapped.example.com/":      "mapped.example.com (192.168.0.10): it is on a private network",
		"http://metadata.example.com/":    "where cloud metadata services live",
		"https://169.254.169.254/latest/": "where cloud metadata services live",
	} {
		_, err := h.Call(context.Background(), "get", args("url", address))
		if shown := rendered(t, err); !strings.Contains(shown, want) {
			t.Errorf("%s: %s", address, shown)
		}
	}
	if seen, _ := p.asked(); len(seen) != 0 {
		t.Errorf("the proxy was asked for %q", seen)
	}

	// `allow private` lets private networks through the proxy, never link-local addresses.
	h = viaProxy(t, lang.ToolDecl{AllowPrivate: true}, proxyURL(t, p), dns)
	for _, address := range []string{"http://intranet.example.com/", "http://localhost/", "http://10.0.0.1/"} {
		if _, err := h.Call(context.Background(), "get", args("url", address)); err != nil {
			t.Errorf("%s: %s", address, rendered(t, err))
		}
	}
	if _, err := h.Call(context.Background(), "get", args("url", "http://metadata.example.com/")); err == nil {
		t.Error("a link-local address was reached through the proxy")
	}
}

func TestANameThisComputerCannotFindIsLeftToTheProxy(t *testing.T) {
	p := newFakeProxy(t)
	h := viaProxy(t, lang.ToolDecl{}, proxyURL(t, p), nil)
	if _, err := h.Call(context.Background(), "get", args("url", "http://only-the-proxy-knows.example/")); err != nil {
		t.Fatal(rendered(t, err))
	}
	if seen, _ := p.asked(); len(seen) != 1 {
		t.Errorf("the proxy was asked for %q", seen)
	}
}

func TestARedirectThroughTheProxyIsCheckedLikeTheFirstRequest(t *testing.T) {
	p := newFakeProxy(t)
	p.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.7/admin", http.StatusFound)
	})
	h := viaProxy(t, lang.ToolDecl{}, proxyURL(t, p), map[string]string{"api.example.com": "93.184.215.14"})
	_, err := h.Call(context.Background(), "get", args("url", "http://api.example.com/"))
	if shown := rendered(t, err); !strings.Contains(shown, "refused to connect to 10.0.0.7") {
		t.Error(shown)
	}
}

func TestAProxyThatRefusesIsExplained(t *testing.T) {
	dns := map[string]string{"api.example.com": "93.184.215.14"}
	for _, c := range []struct {
		address string
		status  int
		want    string
	}{
		{"https://api.example.com/", http.StatusProxyAuthRequired, "asked for a user and password"},
		{"http://api.example.com/", http.StatusProxyAuthRequired, "asked for a user and password"},
		{"https://api.example.com/", http.StatusForbidden, "refused the connection: it answered 403"},
	} {
		p := newFakeProxy(t)
		p.refuse = c.status
		h := viaProxy(t, lang.ToolDecl{}, proxyURL(t, p), dns)
		_, err := h.Call(context.Background(), "get", args("url", c.address))
		shown := rendered(t, err)
		if !strings.Contains(shown, c.want) || !strings.Contains(shown, p.Listener.Addr().String()) {
			t.Errorf("%s with %d: %s", c.address, c.status, shown)
		}
	}
	// A plain request the proxy refuses for another reason is an answer like any other.
	p := newFakeProxy(t)
	p.refuse = http.StatusForbidden
	got, err := viaProxy(t, lang.ToolDecl{}, proxyURL(t, p), dns).Call(context.Background(), "get", args("url", "http://api.example.com/"))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if status := fieldPath(t, got, "status"); status.Number != 403 {
		t.Errorf("status = %s", status.Display())
	}
}

func TestAProxyThatDoesNotAnswerIsNamed(t *testing.T) {
	p := newFakeProxy(t)
	proxy := proxyURL(t, p)
	p.Close()
	h := viaProxy(t, lang.ToolDecl{}, proxy, map[string]string{"api.example.com": "93.184.215.14"})
	_, err := h.Call(context.Background(), "get", args("url", "https://api.example.com/"))
	shown := rendered(t, err)
	if !strings.Contains(shown, "the proxy "+proxy.Host+" did not answer") || !strings.Contains(shown, "[network]") {
		t.Error(shown)
	}
}

func TestWithoutAProxyTheOneOfTheEnvironmentIsNotUsed(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.example.com:3128")
	t.Setenv("HTTPS_PROXY", "http://proxy.example.com:3128")
	// The proxy of the environment skips this computer, so a request would not show
	// it: the transport itself must have no proxy.
	transport := newHTTP(lang.ToolDecl{}, 0).client.Transport.(*http.Transport)
	if transport.Proxy != nil {
		t.Error("the transport takes a proxy")
	}
}
