package serve

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// jsonLogger writes the entries as JSON, one in each line, and leaves out the time, which
// is not one a test can know.
func jsonLogger(out io.Writer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(groups) == 0 {
			return slog.Attr{}
		}
		return a
	}}))
}

var fixedClock = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }

func request(method, target string) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader("a body with a secret"))
	r.RemoteAddr = "192.0.2.10:5555"
	return r
}

// entries logs the requests through a logger of JSON and returns what it wrote.
func entries(t *testing.T, handler http.Handler, builds ...func() *http.Request) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	logged := accessLog(handler, jsonLogger(&out), fixedClock)
	for _, build := range builds {
		logged.ServeHTTP(httptest.NewRecorder(), build())
	}
	if out.Len() == 0 {
		return nil
	}
	var all []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("a line that is not an entry: %q", line)
		}
		all = append(all, entry)
	}
	return all
}

func one(t *testing.T, handler http.Handler, build func() *http.Request) map[string]any {
	t.Helper()
	all := entries(t, handler, build)
	if len(all) != 1 {
		t.Fatalf("%d entries for one request", len(all))
	}
	return all[0]
}

// req: P5
func TestEachRequestIsOneStructuredEntryWithWhatWasAskedAndHowItWasAnswered(t *testing.T) {
	entry := one(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "short")
	}), func() *http.Request { return request("POST", "/agents/Bob") })
	want := map[string]any{"msg": "request", "level": "INFO", "remote": "192.0.2.10", "method": "POST",
		"path": "/agents/Bob", "status": float64(418), "bytes": float64(5), "duration_ms": float64(0)}
	for key, value := range want {
		if entry[key] != value {
			t.Errorf("%s = %v, want %v", key, entry[key], value)
		}
	}
	for _, key := range []string{"agent", "rpc", "message", "task", "result"} {
		if _, there := entry[key]; there {
			t.Errorf("%s is in an entry of a request that never reached an agent", key)
		}
	}
}

func TestARequestThatWritesNothingWasAnswered200(t *testing.T) {
	entry := one(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), func() *http.Request { return request("GET", "/") })
	if entry["status"] != float64(200) || entry["bytes"] != float64(0) {
		t.Errorf("entry = %v", entry)
	}
}

// req: P5, P3
func TestTheLogNeverHoldsTheTokenTheQueryTheHeadersOrTheBody(t *testing.T) {
	const token = "tok-live-0123456789-0123456789-0123456789"
	var out bytes.Buffer
	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}), &out)
	r := request("POST", "/agents/Bob?token="+token+"&x=1")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Cookie", "session=secret-cookie")
	r.Header.Set("User-Agent", "agent-of-"+token)
	handler.ServeHTTP(httptest.NewRecorder(), r)
	line := out.String()
	for _, leak := range []string{token, "tok-live", "secret", "Bearer", "Authorization", "x=1", "token=", "ookie", "agent-of"} {
		if strings.Contains(line, leak) {
			t.Errorf("the log tells %q:\n%s", leak, line)
		}
	}
	if !strings.Contains(line, "method=POST path=/agents/Bob status=200") {
		t.Errorf("the entry is not the text form of slog:\n%s", line)
	}
}

// req: P5
func TestAPathThatTriesToForgeAnEntryOnlyWritesAStrangePath(t *testing.T) {
	forged := "/a\nlevel=INFO msg=request remote=10.0.0.1 method=POST path=/agents/Bob status=200\x00\xff"
	build := func() *http.Request {
		r := request("GET", "/a")
		r.URL.Path = forged
		return r
	}
	entry := one(t, http.NotFoundHandler(), build)
	if entry["remote"] != "192.0.2.10" || !strings.Contains(entry["path"].(string), "msg=request remote=10.0.0.1") {
		t.Errorf("entry = %v", entry)
	}
	// ...and in the form of text, the entry is still one line.
	var out bytes.Buffer
	AccessLog(http.NotFoundHandler(), &out).ServeHTTP(httptest.NewRecorder(), build())
	if strings.Count(out.String(), "\n") != 1 || !strings.HasSuffix(out.String(), "\n") {
		t.Errorf("the path made more than one line:\n%q", out.String())
	}
}

func TestAVeryLongPathIsCut(t *testing.T) {
	entry := one(t, http.NotFoundHandler(), func() *http.Request { return request("GET", "/"+strings.Repeat("a", 5000)) })
	path := entry["path"].(string)
	if len(path) > maxLoggedPath+3 || !strings.HasSuffix(path, "...") {
		t.Errorf("a path of %d bytes", len(path))
	}
}

func TestARequestThatPanicsIsLoggedAndThePanicGoesOn(t *testing.T) {
	var out bytes.Buffer
	handler := AccessLog(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { panic("boom") }), &out)
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), request("GET", "/x"))
	}()
	if !strings.Contains(out.String(), "path=/x") || strings.Contains(out.String(), "boom") {
		t.Errorf("log = %q", out.String())
	}
}

func TestManyRequestsAtOnceMakeWholeEntries(t *testing.T) {
	var out bytes.Buffer
	handler := accessLog(http.NotFoundHandler(), jsonLogger(&out), fixedClock)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			handler.ServeHTTP(httptest.NewRecorder(), request("GET", fmt.Sprintf("/path/%d", i)))
		}(i)
	}
	wg.Wait()
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil || entry["status"] != float64(404) {
			t.Fatalf("a damaged entry: %q", line)
		}
		seen[entry["path"].(string)] = true
	}
	if len(seen) != 50 {
		t.Errorf("%d different entries for 50 requests", len(seen))
	}
}

// ---------- what the server tells the log ----------

func TestANilNoteTakesEverythingAndKeepsNothing(t *testing.T) {
	var n *accessNote
	n.setAgent("Bob")
	n.setRPC("SendMessage")
	n.setMessage("echo")
	n.setTask("task-1")
	n.setResult(resultOK)
	if noteOf(context.Background()) != nil {
		t.Error("a request without a log has a note")
	}
}

// req: P5
func TestTheLogSaysWhichAgentWhichMessageWhichTaskAndHowItEnded(t *testing.T) {
	s := newTestServer(t, nil)
	send := func(body string, mutate func(*http.Request)) func() *http.Request {
		return func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, bobPath, strings.NewReader(body))
			r.Host = "127.0.0.1:8080"
			r.RemoteAddr = "192.0.2.10:5555"
			r.Header.Set("Authorization", "Bearer "+goodToken)
			r.Header.Set("Content-Type", "application/json")
			if mutate != nil {
				mutate(r)
			}
			return r
		}
	}
	got := entries(t, s,
		send(rpcBody("SendMessage", message(dataPart("echo", `{"text":"x"}`), "")), nil),
		send(rpcBody("SendMessage", message(dataPart("fail", `{}`), "")), nil),
		send(rpcBody("SendMessage", message(dataPart("nope", `{}`), "")), nil),
		send(rpcBody("GetTask", `{"id":"t"}`), nil),
		send(rpcBody("SendMessage", message(dataPart("echo", `{"text":"x"}`), "")), func(r *http.Request) { r.Header.Del("Authorization") }),
	)
	if len(got) != 5 {
		t.Fatalf("%d entries", len(got))
	}
	ok, failed, refused, other, door := got[0], got[1], got[2], got[3], got[4]

	if ok["agent"] != "Bob" || ok["rpc"] != "SendMessage" || ok["message"] != "echo" || ok["result"] != "ok" ||
		!strings.HasPrefix(ok["task"].(string), "task-") {
		t.Errorf("a message that worked: %v", ok)
	}
	if failed["message"] != "fail" || failed["result"] != "failed" || failed["task"] == nil {
		t.Errorf("a message whose agent could not answer: %v", failed)
	}
	if refused["agent"] != "Bob" || refused["result"] != "error" || refused["message"] != nil || refused["task"] != nil {
		t.Errorf("a message that was refused before it ran: %v", refused)
	}
	if other["rpc"] != "GetTask" || other["result"] != nil || other["agent"] != "Bob" {
		t.Errorf("another method: %v", other)
	}
	if door["status"] != float64(401) || door["agent"] != nil || door["rpc"] != nil {
		t.Errorf("a request the door refused told what it was after: %v", door)
	}
}

func TestTheOutcomeOfAMessageIsNamed(t *testing.T) {
	for name, tt := range map[string]struct {
		result any
		e      *rpcError
		want   string
	}{
		"a message": {map[string]any{"message": map[string]any{}}, nil, resultOK},
		"a task":    {map[string]any{"task": map[string]any{}}, nil, resultFailed},
		"an error":  {nil, &rpcError{codeInvalidParams, "x"}, resultError},
		"busy":      {nil, errServerBusy, resultBusy},
		"nobody":    {nil, nil, resultLeft},
	} {
		if got := outcomeOf(tt.result, tt.e); got != tt.want {
			t.Errorf("%s: %s, want %s", name, got, tt.want)
		}
	}
}
