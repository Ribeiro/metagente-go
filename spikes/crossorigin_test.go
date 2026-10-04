package spikes

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func send(t *testing.T, url, host string, headers map[string]string, body string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode
}

func merge(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// V5 (S4): the standard library protection, on its own.
func TestV5StandardLibraryCrossOriginProtection(t *testing.T) {
	protection := http.NewCrossOriginProtection()
	if err := protection.AddTrustedOrigin("https://trusted.example"); err != nil {
		t.Fatal(err)
	}
	var reached atomic.Int32
	server := httptest.NewServer(protection.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusNoContent)
	})))
	defer server.Close()

	for _, tt := range []struct {
		name    string
		headers map[string]string
		want    int
	}{
		{"a program that sends no browser headers", nil, http.StatusNoContent},
		{"a browser on the same origin", map[string]string{"Sec-Fetch-Site": "same-origin"}, http.StatusNoContent},
		{"a browser on another site", map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusForbidden},
		{"an unknown Origin", map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"a trusted Origin", map[string]string{"Origin": "https://trusted.example"}, http.StatusNoContent},
	} {
		got := send(t, server.URL, "", tt.headers, "{}")
		t.Logf("%-42s -> %d", tt.name, got)
		if got != tt.want {
			t.Errorf("%s: got %d, want %d", tt.name, got, tt.want)
		}
	}
}

// S2, S3, S4, S6 on the MCP HTTP handler. The release notes of the SDK say the
// cross-origin protection is OFF unless the option is set, so it is set here.
func TestS2S3S4S6TheMCPHTTPHandlerRefusesWhatWeNeedRefused(t *testing.T) {
	spike := newSpikeServer()
	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return spike },
		&mcp.StreamableHTTPOptions{
			CrossOriginProtection: http.NewCrossOriginProtection(),
			MaxRequestBodyBytes:   1 << 20,
		})
	server := httptest.NewServer(handler)
	defer server.Close()

	const ping = `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	base := map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream"}

	control := send(t, server.URL, "", base, ping)
	t.Logf("control request (nothing wrong with it): %d", control)
	if control == 403 || control == 413 || control == 415 {
		t.Fatalf("the control request was already refused with %d, so the checks below prove nothing", control)
	}

	for _, tt := range []struct {
		name    string
		host    string
		headers map[string]string
		body    string
	}{
		{"request from another site (S4)", "", merge(base, map[string]string{"Sec-Fetch-Site": "cross-site"}), ping},
		{"unknown Origin (S4)", "", merge(base, map[string]string{"Origin": "https://evil.example"}), ping},
		{"wrong Content-Type (S2)", "", merge(base, map[string]string{"Content-Type": "text/plain"}), ping},
		{"Host of a rebinding attack (S3)", "evil.example", base, ping},
		{"body over 1 MiB (S6)", "", base, strings.Repeat("x", 2<<20)},
	} {
		got := send(t, server.URL, tt.host, tt.headers, tt.body)
		t.Logf("%-36s -> %d", tt.name, got)
		if got < 400 || got >= 500 {
			t.Errorf("%s: expected a 4xx refusal, got %d", tt.name, got)
		}
	}
}
