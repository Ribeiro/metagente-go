package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Ribeiro/metagente-go/internal/config"
	"github.com/Ribeiro/metagente-go/internal/lang"
	"github.com/Ribeiro/metagente-go/internal/value"
)

func newHTTP(decl lang.ToolDecl, maxBytes int64) *HTTP {
	decl.Name, decl.Kind = "http", lang.ToolHTTP
	limits := config.Default().Limits
	if maxBytes > 0 {
		limits.MaxHTTPBytes = maxBytes
	}
	return NewHTTP(&decl, limits)
}

// testServer answers a few routes and counts the requests that reach it.
func testServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"greeting":"hi","count":3}`)
	})
	mux.HandleFunc("/echo", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("X-Content-Type-Seen", r.Header.Get("Content-Type"))
		w.Write(body)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(strings.Repeat("x", 1000)))
	})
	mux.HandleFunc("/loop", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	mux.HandleFunc("/to-metadata", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	})
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return server, &hits
}

func TestGetAndPostReturnStatusTextAndJSON(t *testing.T) {
	server, _ := testServer(t)
	h := newHTTP(lang.ToolDecl{AllowPrivate: true}, 0)
	ctx := context.Background()

	got, err := h.Call(ctx, "get", args("url", server.URL+"/hello"))
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	status, _ := got.Field("status")
	if status.Number != 200 {
		t.Errorf("status = %v", status.Display())
	}
	greeting := fieldPath(t, got, "json", "greeting")
	count := fieldPath(t, got, "json", "count")
	if greeting.Text != "hi" || count.Number != 3 {
		t.Errorf("json = %v %v", greeting.Display(), count.Display())
	}

	posted, err := h.Call(ctx, "post", Args{"url": value.Text(server.URL + "/echo"), "body": value.Text("ping")})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if text, _ := posted.Field("text"); text.Text != "ping" {
		t.Errorf("echo = %q", text.Text)
	}
}

func fieldPath(t *testing.T, v value.Value, names ...string) value.Value {
	t.Helper()
	for _, name := range names {
		next, ok := v.Field(name)
		if !ok {
			t.Fatalf("no field %q in %s", name, v.Display())
		}
		v = next
	}
	return v
}

func TestARecordBodyIsSentAsJSON(t *testing.T) {
	server, _ := testServer(t)
	h := newHTTP(lang.ToolDecl{AllowPrivate: true}, 0)
	body := value.Record(map[string]value.Value{"n": value.Number(1), "ok": value.Bool(true)})
	got, err := h.Call(context.Background(), "post", Args{"url": value.Text(server.URL + "/echo"), "body": body})
	if err != nil {
		t.Fatal(rendered(t, err))
	}
	if text, _ := got.Field("text"); text.Text != `{"n":1,"ok":true}` {
		t.Errorf("the server received %q", text.Text)
	}
}

// req: H1
func TestInternalAddressesAreRefusedByDefaultBeforeAnythingIsSent(t *testing.T) {
	server, hits := testServer(t)
	h := newHTTP(lang.ToolDecl{}, 0)

	_, err := h.Call(context.Background(), "get", args("url", server.URL+"/hello"))
	mustContain(t, rendered(t, err),
		"`http` refused to connect to 127.0.0.1: it is this computer",
		"tool http allow private")
	if n := hits.Load(); n != 0 {
		t.Errorf("the server received %d requests; the guard must stop the connection first", n)
	}
}

// req: H1
func TestAllowPrivateStillRefusesTheMetadataAddressEvenThroughARedirect(t *testing.T) {
	server, hits := testServer(t)
	h := newHTTP(lang.ToolDecl{AllowPrivate: true}, 0)

	_, err := h.Call(context.Background(), "get", args("url", server.URL+"/to-metadata"))
	mustContain(t, rendered(t, err), "refused to connect to 169.254.169.254", "link-local")
	if n := hits.Load(); n != 1 {
		t.Errorf("only the first request should have reached the server, got %d", n)
	}
}

// req: H1
func TestTheGuardOnAddresses(t *testing.T) {
	refusedByDefault := []string{
		"127.0.0.1:80", "[::1]:80", "10.1.2.3:80", "172.16.0.1:80", "192.168.1.1:80",
		"169.254.169.254:80", "[fe80::1]:80", "0.0.0.0:80", "224.0.0.1:80", "100.64.0.1:80",
		"198.18.0.1:80", "[::ffff:127.0.0.1]:80", "[fd00::1]:80", "[2001:db8::1]:80",
	}
	for _, address := range refusedByDefault {
		if err := checkAddress(address, false); err == nil {
			t.Errorf("%s should be refused", address)
		}
	}
	public := []string{"8.8.8.8:443", "1.1.1.1:443", "[2606:4700:4700::1111]:443", "93.184.216.34:80"}
	for _, address := range public {
		if err := checkAddress(address, false); err != nil {
			t.Errorf("%s should be allowed: %v", address, err)
		}
	}
	// `allow private` opens loopback and private networks, and nothing else.
	for _, address := range []string{"127.0.0.1:80", "10.0.0.1:80", "[::1]:80", "192.168.0.9:80", "[::ffff:10.0.0.1]:80"} {
		if err := checkAddress(address, true); err != nil {
			t.Errorf("%s should be allowed with allow private: %v", address, err)
		}
	}
	for _, address := range []string{"169.254.169.254:80", "[fe80::1]:80", "0.0.0.0:80", "224.0.0.1:80"} {
		if err := checkAddress(address, true); err == nil {
			t.Errorf("%s must stay refused even with allow private", address)
		}
	}
	if err := checkAddress("not an address", false); err == nil {
		t.Error("an address that cannot be read must be refused")
	}
}

// req: H2
func TestOnlyTheDeclaredDomainsCanBeReached(t *testing.T) {
	server, hits := testServer(t)
	h := newHTTP(lang.ToolDecl{AllowPrivate: true, Allow: []string{"api.example.com", "*.corp.example"}}, 0)

	for _, address := range []string{"http://other.example/x", server.URL + "/hello"} {
		_, err := h.Call(context.Background(), "get", args("url", address))
		mustContain(t, rendered(t, err), "may only reach the domains this agent declared", "tool http allow")
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("a refused domain must not be contacted, the server saw %d requests", n)
	}
}

// req: H2
func TestHostAllowed(t *testing.T) {
	h := newHTTP(lang.ToolDecl{Allow: []string{"api.example.com", "*.corp.example"}}, 0)
	tests := []struct {
		host string
		want bool
	}{
		{"api.example.com", true},
		{"API.Example.COM", true},
		{"api.example.com.", true},
		{"x.api.example.com", false},
		{"example.com", false},
		{"a.corp.example", true},
		{"a.b.corp.example", true},
		{"corp.example", false}, // the wildcard is for the subdomains only
		{"evilcorp.example", false},
		{"127.0.0.1", false},
	}
	for _, tt := range tests {
		if got := h.hostAllowed(tt.host); got != tt.want {
			t.Errorf("hostAllowed(%q) = %v, want %v", tt.host, got, tt.want)
		}
	}
	open := newHTTP(lang.ToolDecl{}, 0)
	if !open.hostAllowed("anything.example") {
		t.Error("without an allow list every public host is allowed")
	}
}

// req: H3
func TestAnAnswerOverTheLimitIsRefused(t *testing.T) {
	server, _ := testServer(t)
	h := newHTTP(lang.ToolDecl{AllowPrivate: true}, 100)
	_, err := h.Call(context.Background(), "get", args("url", server.URL+"/big"))
	mustContain(t, rendered(t, err), "larger than the 100 bytes", "max_http_bytes")
}

// req: H3
func TestACircleOfRedirectsIsStopped(t *testing.T) {
	server, hits := testServer(t)
	h := newHTTP(lang.ToolDecl{AllowPrivate: true}, 0)
	_, err := h.Call(context.Background(), "get", args("url", server.URL+"/loop"))
	mustContain(t, rendered(t, err), "redirected more than 5 times")
	if n := hits.Load(); n > int64(maxRedirects)+1 {
		t.Errorf("followed %d redirects", n)
	}
}

func TestAnAddressWithoutASchemeIsRefused(t *testing.T) {
	h := newHTTP(lang.ToolDecl{}, 0)
	for _, address := range []string{"example.org", "ftp://example.org/file", "http://", "//example.org"} {
		_, err := h.Call(context.Background(), "get", args("url", address))
		mustContain(t, rendered(t, err), "http:// or https://")
	}
}

func TestAClosedPortIsExplained(t *testing.T) {
	h := newHTTP(lang.ToolDecl{AllowPrivate: true}, 0)
	_, err := h.Call(context.Background(), "get", args("url", "http://127.0.0.1:1/nothing"))
	mustContain(t, rendered(t, err), "I could not reach http://127.0.0.1:1/nothing", "internet connection")
}

func TestReadOnlyRefusesPostButAllowsGet(t *testing.T) {
	server, _ := testServer(t)
	h := newHTTP(lang.ToolDecl{AllowPrivate: true, ReadOnly: true}, 0)
	_, err := h.Call(context.Background(), "post", Args{"url": value.Text(server.URL + "/echo")})
	mustContain(t, rendered(t, err), "`http.post` is not available because `tool http` was declared readonly")
	if _, err := h.Call(context.Background(), "get", args("url", server.URL+"/hello")); err != nil {
		t.Errorf("get should still work: %v", rendered(t, err))
	}
	_, err = h.Call(context.Background(), "put", Args{})
	mustContain(t, rendered(t, err), "`http` can do: get")
}
