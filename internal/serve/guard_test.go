package serve

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const goodToken = "tok-live-0123456789-0123456789-0123456789"

var hosts = []string{"127.0.0.1:8080", "localhost:8080", "[::1]:8080"}

// reached records whether the request got past the guard.
type reached struct{ count int }

func (r *reached) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.count++
	if _, err := io.ReadAll(req.Body); err != nil {
		http.Error(w, "too large", http.StatusRequestEntityTooLarge)
		return
	}
	_, _ = io.WriteString(w, "reached")
}

func newGuard() *Guard {
	g := NewGuard(goodToken, hosts)
	g.MaxBody = 1024
	g.Now = func() time.Time { return time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC) }
	return g
}

// post is a good request; a test spoils one thing at a time.
func post(mutate func(*http.Request)) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"jsonrpc":"2.0"}`))
	req.Host = "127.0.0.1:8080"
	req.RemoteAddr = "192.0.2.10:5555"
	req.Header.Set("Authorization", "Bearer "+goodToken)
	req.Header.Set("Content-Type", "application/json")
	if mutate != nil {
		mutate(req)
	}
	return req
}

func serveOne(g *Guard, req *http.Request) (*httptest.ResponseRecorder, *reached) {
	next := &reached{}
	rec := httptest.NewRecorder()
	g.Protect(next).ServeHTTP(rec, req)
	return rec, next
}

func TestAGoodRequestGetsThrough(t *testing.T) {
	for _, host := range hosts {
		rec, next := serveOne(newGuard(), post(func(r *http.Request) { r.Host = host }))
		if rec.Code != 200 || next.count != 1 {
			t.Errorf("host %s: code %d, reached %d", host, rec.Code, next.count)
		}
	}
	rec, _ := serveOne(newGuard(), post(func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=utf-8") }))
	if rec.Code != 200 {
		t.Errorf("a charset was refused: %d", rec.Code)
	}
}

// req: S1
func TestWithoutTheRightTokenNothingGetsThrough(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"no header":      func(r *http.Request) { r.Header.Del("Authorization") },
		"empty":          func(r *http.Request) { r.Header.Set("Authorization", "") },
		"wrong token":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+goodToken+"x") },
		"a prefix":       func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+goodToken[:20]) },
		"other scheme":   func(r *http.Request) { r.Header.Set("Authorization", "Basic "+goodToken) },
		"no scheme":      func(r *http.Request) { r.Header.Set("Authorization", goodToken) },
		"two spaces":     func(r *http.Request) { r.Header.Set("Authorization", "Bearer  "+goodToken) },
		"token in query": func(r *http.Request) { r.Header.Del("Authorization"); r.URL.RawQuery = "token=" + goodToken },
		"token as header": func(r *http.Request) {
			r.Header.Del("Authorization")
			r.Header.Set("X-Api-Key", goodToken)
		},
	} {
		rec, next := serveOne(newGuard(), post(mutate))
		if rec.Code != http.StatusUnauthorized || next.count != 0 {
			t.Errorf("%s: code %d, reached %d", name, rec.Code, next.count)
		}
		if rec.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: the refusal does not say how to authenticate", name)
		}
	}
	// The scheme is not case sensitive; the token is.
	rec, _ := serveOne(newGuard(), post(func(r *http.Request) { r.Header.Set("Authorization", "bearer "+goodToken) }))
	if rec.Code != 200 {
		t.Errorf("a lower case scheme was refused: %d", rec.Code)
	}
	rec, _ = serveOne(newGuard(), post(func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.ToUpper(goodToken)) }))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("another case of the token was accepted: %d", rec.Code)
	}
}

// req: S1
func TestARefusalNeverTellsWhatWasWrong(t *testing.T) {
	var first string
	for i, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del("Authorization") },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer nope") },
		func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+goodToken[:len(goodToken)-1]) },
	} {
		rec, _ := serveOne(newGuard(), post(mutate))
		body := rec.Body.String()
		if i == 0 {
			first = body
		} else if body != first {
			t.Errorf("the answers differ, so they tell something:\n%q\n%q", first, body)
		}
		if strings.Contains(body, goodToken) || strings.Contains(strings.ToLower(body), "token") {
			t.Errorf("the refusal talks about the token: %q", body)
		}
	}
}

// req: S1
func TestAnUnauthenticatedRequestLearnsNothingAboutTheRestOfTheChecks(t *testing.T) {
	// Wrong method, wrong type and no token: the answer is still 401.
	rec, _ := serveOne(newGuard(), post(func(r *http.Request) {
		r.Method = http.MethodGet
		r.Header.Del("Authorization")
		r.Header.Set("Content-Type", "text/plain")
	}))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("code %d", rec.Code)
	}
}

// req: S1
func TestManyWrongTokensFromOnePlaceAreSlowedDown(t *testing.T) {
	g := newGuard()
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	g.Now = func() time.Time { return now }
	wrong := func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }
	for i := 0; i < defaultMaxFailures; i++ {
		if rec, _ := serveOne(g, post(wrong)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("try %d: code %d", i, rec.Code)
		}
	}
	rec, next := serveOne(g, post(nil)) // even the right token waits
	if rec.Code != http.StatusTooManyRequests || next.count != 0 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("code %d, reached %d, Retry-After %q", rec.Code, next.count, rec.Header().Get("Retry-After"))
	}
	other, _ := serveOne(g, post(func(r *http.Request) { r.RemoteAddr = "192.0.2.99:1" }))
	if other.Code != 200 {
		t.Errorf("another place was slowed down too: %d", other.Code)
	}
	now = now.Add(blockFor + time.Second)
	if rec, _ := serveOne(g, post(nil)); rec.Code != 200 {
		t.Errorf("after the wait: code %d", rec.Code)
	}
}

// A request without any token guesses nothing, so it is refused without being counted. A client of
// MCP that looks for OAuth first sends six such requests each time it starts (check 1 of
// validation/README.md, with mcp-remote), and on this computer every client is the same place:
// counted, they stopped the client itself, which came next with the right token.
func TestARequestWithoutATokenIsRefusedButNotCounted(t *testing.T) {
	g := newGuard()
	paths := []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration", "/mcp"}
	for i := 0; i < defaultMaxFailures*3; i++ {
		probe := post(func(r *http.Request) { r.Header.Del("Authorization") })
		probe.Method, probe.URL.Path = http.MethodGet, paths[i%len(paths)]
		rec, next := serveOne(g, probe)
		if rec.Code != http.StatusUnauthorized || next.count != 0 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Fatalf("probe %d: code %d, reached %d", i, rec.Code, next.count)
		}
	}
	if rec, next := serveOne(g, post(nil)); rec.Code != http.StatusOK || next.count != 1 {
		t.Fatalf("the right token after the probes: code %d", rec.Code)
	}

	// A header that is there counts, whatever is in it: it is a try.
	for name, header := range map[string]string{"wrong": "Bearer wrong", "other scheme": "Basic d3Jvbmc=", "empty bearer": "Bearer "} {
		h := newGuard()
		for i := 0; i < defaultMaxFailures; i++ {
			serveOne(h, post(func(r *http.Request) { r.Header.Set("Authorization", header) }))
		}
		if rec, _ := serveOne(h, post(nil)); rec.Code != http.StatusTooManyRequests {
			t.Errorf("%s: ten of them were not counted: code %d", name, rec.Code)
		}
	}
}

func TestTheListOfPlacesThatFailedDoesNotGrowWithoutEnd(t *testing.T) {
	g := newGuard()
	for i := 0; i < maxTrackedClients*3; i++ {
		addr := fmt.Sprintf("10.%d.%d.%d:1", i/65536%256, i/256%256, i%256)
		serveOne(g, post(func(r *http.Request) {
			r.RemoteAddr = addr
			r.Header.Set("Authorization", "Bearer wrong")
		}))
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.failures) > maxTrackedClients {
		t.Errorf("%d places are being remembered", len(g.failures))
	}
}

// A place that is stopped stays stopped for its minute, however many other places fail
// meanwhile: otherwise whoever is stopped could get out by failing from many other
// addresses, which IPv6 gives by the million.
func TestAPlaceThatIsStoppedCannotGetOutByFailingFromOtherAddresses(t *testing.T) {
	g := newGuard()
	wrong := func(addr string) func(*http.Request) {
		return func(r *http.Request) {
			r.RemoteAddr = addr
			r.Header.Set("Authorization", "Bearer wrong")
		}
	}
	for i := 0; i < defaultMaxFailures; i++ {
		serveOne(g, post(wrong("192.0.2.10:1")))
	}
	for i := 0; i < maxTrackedClients*3; i++ {
		serveOne(g, post(wrong(fmt.Sprintf("[2001:db8:%x::1]:1", i)))) // each in a network of its own
	}
	if rec, _ := serveOne(g, post(nil)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the place that was stopped got through: %d", rec.Code)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.failures) > maxTrackedClients {
		t.Errorf("%d places are being remembered", len(g.failures))
	}
}

// A connection that the door turned away is closed, so a stranger cannot keep the
// connections of the server open and idle; one that got through is kept.
func TestTheDoorHangsUpOnWhomItTurnsAway(t *testing.T) {
	g := newGuard()
	for name, mutate := range map[string]func(*http.Request){
		"no token":  func(r *http.Request) { r.Header.Del("Authorization") },
		"wrong one": func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") },
		"host":      func(r *http.Request) { r.Host = "evil.example" },
		"browser":   func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
	} {
		rec, _ := serveOne(g, post(mutate))
		if rec.Code == http.StatusOK || rec.Header().Get("Connection") != "close" {
			t.Errorf("%s: code %d, Connection %q", name, rec.Code, rec.Header().Get("Connection"))
		}
	}
	if rec, _ := serveOne(g, post(nil)); rec.Code != http.StatusOK || rec.Header().Get("Connection") != "" {
		t.Errorf("a good request: code %d, Connection %q", rec.Code, rec.Header().Get("Connection"))
	}
}

// The addresses of one IPv6 network are one place: changing the address inside it does not give
// ten more tries.
func TestTheAddressesOfAnIPv6NetworkShareTheirTries(t *testing.T) {
	g := newGuard()
	for i := 0; i < defaultMaxFailures; i++ {
		serveOne(g, post(func(r *http.Request) {
			r.RemoteAddr = fmt.Sprintf("[2001:db8:7:7::%x]:1", i+1)
			r.Header.Set("Authorization", "Bearer wrong")
		}))
	}
	rec, _ := serveOne(g, post(func(r *http.Request) { r.RemoteAddr = "[2001:db8:7:7::abcd]:1" }))
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("another address of the same network got through: %d", rec.Code)
	}
	other, _ := serveOne(g, post(func(r *http.Request) { r.RemoteAddr = "[2001:db8:7:8::1]:1" }))
	if other.Code != http.StatusOK {
		t.Errorf("another network was slowed down too: %d", other.Code)
	}
}

// req: S2
func TestADifferentHostIsRefusedBeforeAnythingElse(t *testing.T) {
	for _, host := range []string{"evil.example", "evil.example:8080", "127.0.0.1", "127.0.0.1:9999", "127.0.0.1:8080.evil.example", "", "LOCALHOST:8081"} {
		rec, next := serveOne(newGuard(), post(func(r *http.Request) { r.Host = host }))
		if rec.Code != http.StatusMisdirectedRequest || next.count != 0 {
			t.Errorf("host %q: code %d, reached %d", host, rec.Code, next.count)
		}
	}
	rec, _ := serveOne(newGuard(), post(func(r *http.Request) { r.Host = "LocalHost:8080" }))
	if rec.Code != 200 {
		t.Errorf("a host in another case was refused: %d", rec.Code)
	}
}

// req: S3
func TestARequestMadeByABrowserPageIsRefused(t *testing.T) {
	for name, mutate := range map[string]func(*http.Request){
		"origin":         func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") },
		"null origin":    func(r *http.Request) { r.Header.Set("Origin", "null") },
		"same origin":    func(r *http.Request) { r.Header.Set("Origin", "http://127.0.0.1:8080") },
		"fetch site":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") },
		"fetch dest":     func(r *http.Request) { r.Header.Set("Sec-Fetch-Dest", "empty") },
		"fetch user":     func(r *http.Request) { r.Header.Set("Sec-Fetch-User", "?1") },
		"fetch site own": func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "same-origin") },
	} {
		rec, next := serveOne(newGuard(), post(mutate))
		if rec.Code != http.StatusForbidden || next.count != 0 {
			t.Errorf("%s: code %d, reached %d", name, rec.Code, next.count)
		}
	}
}

// req: S3
func TestTheFetchOfNodeJSIsNotTakenForABrowser(t *testing.T) {
	// What the fetch of Node.js sends in every request, and what the clients of the official SDK in
	// JavaScript are made on. A browser sends Sec-Fetch-Mode together with Sec-Fetch-Site, and is refused.
	rec, next := serveOne(newGuard(), post(func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Mode", "cors")
		r.Header.Set("Accept-Language", "*")
		r.Header.Set("User-Agent", "node")
	}))
	if rec.Code != http.StatusOK || next.count != 1 {
		t.Errorf("code %d, reached %d", rec.Code, next.count)
	}
}

// req: S3
func TestNothingEverSaysThatAnotherSiteMayCallThisServer(t *testing.T) {
	for _, req := range []*http.Request{
		post(nil),
		post(func(r *http.Request) { r.Method = http.MethodOptions; r.Header.Set("Origin", "https://evil.example") }),
		post(func(r *http.Request) { r.Method = http.MethodOptions }),
		post(func(r *http.Request) { r.Header.Del("Authorization") }),
	} {
		rec, _ := serveOne(newGuard(), req)
		for name := range rec.Header() {
			if strings.HasPrefix(strings.ToLower(name), "access-control-") {
				t.Errorf("%s %d sent %s", req.Method, rec.Code, name)
			}
		}
	}
	rec, _ := serveOne(newGuard(), post(func(r *http.Request) { r.Method = http.MethodOptions }))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a preflight got %d, want 405", rec.Code)
	}
}

// req: S4
func TestOnlyAJSONPostIsAccepted(t *testing.T) {
	for name, tt := range map[string]struct {
		mutate func(*http.Request)
		code   int
	}{
		"get":           {func(r *http.Request) { r.Method = http.MethodGet }, http.StatusMethodNotAllowed},
		"put":           {func(r *http.Request) { r.Method = http.MethodPut }, http.StatusMethodNotAllowed},
		"delete":        {func(r *http.Request) { r.Method = http.MethodDelete }, http.StatusMethodNotAllowed},
		"form":          {func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, http.StatusUnsupportedMediaType},
		"text":          {func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }, http.StatusUnsupportedMediaType},
		"multipart":     {func(r *http.Request) { r.Header.Set("Content-Type", "multipart/form-data; boundary=x") }, http.StatusUnsupportedMediaType},
		"no type":       {func(r *http.Request) { r.Header.Del("Content-Type") }, http.StatusUnsupportedMediaType},
		"json prefix":   {func(r *http.Request) { r.Header.Set("Content-Type", "application/jsonp") }, http.StatusUnsupportedMediaType},
		"query":         {func(r *http.Request) { r.URL.RawQuery = "a=b" }, http.StatusBadRequest},
		"json variants": {func(r *http.Request) { r.Header.Set("Content-Type", "Application/JSON") }, http.StatusOK},
	} {
		rec, _ := serveOne(newGuard(), post(tt.mutate))
		if rec.Code != tt.code {
			t.Errorf("%s: code %d, want %d", name, rec.Code, tt.code)
		}
		if tt.code == http.StatusMethodNotAllowed && rec.Header().Get("Allow") != "POST" {
			t.Errorf("%s: Allow = %q", name, rec.Header().Get("Allow"))
		}
	}
}

// req: S6
func TestABodyOverTheLimitIsRefusedBeforeItIsRead(t *testing.T) {
	big := strings.Repeat("x", 2048)
	req := post(func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(big))
		r.ContentLength = int64(len(big))
	})
	rec, next := serveOne(newGuard(), req)
	if rec.Code != http.StatusRequestEntityTooLarge || next.count != 0 {
		t.Errorf("code %d, reached %d", rec.Code, next.count)
	}
	// A body that lies about its length is cut when it is read.
	lying := post(func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(big))
		r.ContentLength = -1
	})
	rec, next = serveOne(newGuard(), lying)
	if rec.Code != http.StatusRequestEntityTooLarge || next.count != 1 {
		t.Errorf("a body of unknown length: code %d, reached %d", rec.Code, next.count)
	}
}

func TestEveryAnswerCarriesTheSafeHeaders(t *testing.T) {
	for _, req := range []*http.Request{post(nil), post(func(r *http.Request) { r.Header.Del("Authorization") }), post(func(r *http.Request) { r.Host = "evil" })} {
		rec, _ := serveOne(newGuard(), req)
		if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("code %d: headers %v", rec.Code, rec.Header())
		}
	}
}

// req: S8
func TestTheCardOfAnAgentFollowsTheSameDoor(t *testing.T) {
	g := newGuard()
	get := func(mutate func(*http.Request)) (*httptest.ResponseRecorder, *reached) {
		req := post(func(r *http.Request) { r.Method = http.MethodGet; r.Body = http.NoBody; r.Header.Del("Content-Type") })
		if mutate != nil {
			mutate(req)
		}
		next := &reached{}
		rec := httptest.NewRecorder()
		g.ProtectGet(next).ServeHTTP(rec, req)
		return rec, next
	}
	if rec, next := get(nil); rec.Code != 200 || next.count != 1 {
		t.Errorf("a good request: %d, %d", rec.Code, next.count)
	}
	if rec, _ := get(func(r *http.Request) { r.Header.Del("Authorization") }); rec.Code != http.StatusUnauthorized {
		t.Errorf("without a token: %d", rec.Code)
	}
	if rec, _ := get(func(r *http.Request) { r.Method = http.MethodPost }); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a post to the card: %d", rec.Code)
	}
	if rec, _ := get(func(r *http.Request) { r.Host = "evil" }); rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("another host: %d", rec.Code)
	}
}

// req: S8
func TestTheMinimalPublicCardNeedsNoTokenButKeepsTheOtherChecks(t *testing.T) {
	g := newGuard()
	serve := func(mutate func(*http.Request)) (*httptest.ResponseRecorder, *reached) {
		req := post(func(r *http.Request) {
			r.Method = http.MethodGet
			r.Body = http.NoBody
			r.Header.Del("Authorization")
			r.Header.Del("Content-Type")
		})
		if mutate != nil {
			mutate(req)
		}
		next := &reached{}
		rec := httptest.NewRecorder()
		g.PublicGet(next).ServeHTTP(rec, req)
		return rec, next
	}
	if rec, next := serve(nil); rec.Code != 200 || next.count != 1 {
		t.Errorf("code %d, reached %d", rec.Code, next.count)
	}
	if rec, _ := serve(func(r *http.Request) { r.Host = "evil" }); rec.Code != http.StatusMisdirectedRequest {
		t.Errorf("another host: %d", rec.Code)
	}
	if rec, _ := serve(func(r *http.Request) { r.Header.Set("Origin", "https://evil.example") }); rec.Code != http.StatusForbidden {
		t.Errorf("a page of a browser: %d", rec.Code)
	}
	if rec, _ := serve(func(r *http.Request) { r.Method = http.MethodPost }); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("a post: %d", rec.Code)
	}
	rec, _ := serve(nil)
	for name := range rec.Header() {
		if strings.HasPrefix(strings.ToLower(name), "access-control-") {
			t.Errorf("the public card sent %s", name)
		}
	}
}

func TestTheLoopbackHostsAreTheOnesAPersonTypes(t *testing.T) {
	got := strings.Join(LoopbackHosts(8080), " ")
	if got != "127.0.0.1:8080 localhost:8080 [::1]:8080" {
		t.Errorf("hosts = %s", got)
	}
}

// req: S1
func TestBehindAProxyNobodyCanStopEveryoneBySendingWrongTokens(t *testing.T) {
	g := newGuard()
	g.NoThrottle = true
	wrong := func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }
	for i := 0; i < defaultMaxFailures*4; i++ {
		if rec, _ := serveOne(g, post(wrong)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("try %d: code %d", i, rec.Code)
		}
	}
	if rec, next := serveOne(g, post(nil)); rec.Code != 200 || next.count != 1 {
		t.Errorf("the right token was refused after the wrong ones: code %d", rec.Code)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.failures) != 0 {
		t.Errorf("%d places are being remembered although nothing is slowed down", len(g.failures))
	}
}

// req: S6
func TestTenWrongTokensAMinuteIsTheDefaultAndItCanBeChanged(t *testing.T) {
	if defaultMaxFailures != 10 {
		t.Fatalf("the default is %d wrong tokens a minute, the specification says 10", defaultMaxFailures)
	}
	g := newGuard()
	g.MaxFailures = 3
	wrong := func(r *http.Request) { r.Header.Set("Authorization", "Bearer wrong") }
	for i := 0; i < 3; i++ {
		if rec, _ := serveOne(g, post(wrong)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("try %d: code %d", i, rec.Code)
		}
	}
	if rec, _ := serveOne(g, post(nil)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 3 wrong tokens with a limit of 3: code %d", rec.Code)
	}

	// With the default, the tenth wrong one is still answered as wrong, and the next waits.
	d := newGuard()
	for i := 0; i < 10; i++ {
		if rec, _ := serveOne(d, post(wrong)); rec.Code != http.StatusUnauthorized {
			t.Fatalf("try %d: code %d", i, rec.Code)
		}
	}
	if rec, _ := serveOne(d, post(nil)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("after 10 wrong tokens: code %d", rec.Code)
	}
}

// req: S1, P5
func TestEachTokenOpensTheDoorAndTheLogSaysWhoseItIs(t *testing.T) {
	mac, notebook, phone := GenerateToken(), GenerateToken(), GenerateToken()
	g := NewGuardFor([]Credential{{"mac", mac}, {"notebook", notebook}, {"phone", phone}}, hosts)
	logged := func(token string) (int, map[string]any) {
		var code int
		entry := one(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := httptest.NewRecorder()
			g.Protect(&reached{}).ServeHTTP(rec, r)
			code = rec.Code
			w.WriteHeader(rec.Code)
		}), func() *http.Request {
			return post(func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) })
		})
		return code, entry
	}
	for name, token := range map[string]string{"mac": mac, "notebook": notebook, "phone": phone} {
		code, entry := logged(token)
		if code != 200 || entry["client"] != name {
			t.Errorf("%s: code %d, client %v", name, code, entry["client"])
		}
	}
	code, entry := logged(goodToken)
	if _, said := entry["client"]; code != 401 || said {
		t.Errorf("a token of nobody: code %d, client %v", code, entry["client"])
	}
	// A token without a name says no client.
	g.SetTokens([]Credential{{Token: mac}})
	code, entry = logged(mac)
	if _, said := entry["client"]; code != 200 || said {
		t.Errorf("a token without a name: code %d, client %v", code, entry["client"])
	}
}

// req: S1
func TestATokenTakenAwayNoLongerOpensTheDoorAndTheOthersStillDo(t *testing.T) {
	mac, notebook := GenerateToken(), GenerateToken()
	g := NewGuardFor([]Credential{{"mac", mac}, {"notebook", notebook}}, hosts)
	g.SetTokens([]Credential{{"mac", mac}})
	for token, want := range map[string]int{mac: 200, notebook: 401, "": 401} {
		rec, _ := serveOne(g, post(func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }))
		if rec.Code != want {
			t.Errorf("token %.8s: code %d, want %d", token, rec.Code, want)
		}
	}
	g.SetTokens(nil)
	if rec, _ := serveOne(g, post(nil)); rec.Code != 401 {
		t.Errorf("a door without tokens let someone in: %d", rec.Code)
	}
}
