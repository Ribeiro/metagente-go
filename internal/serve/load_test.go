package serve

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ribeiro/metagente-go/internal/lang"
)

// The tests of load and of limits that the specification asks for in S6. They run
// against a server that listens on a real port, with its own timeouts, because what
// they prove is the behavior of the listener and not of a function.

var realClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}, Timeout: 20 * time.Second}

// realServer serves on a real port. The Host it answers to is the address it has.
func realServer(t *testing.T, tweak func(*Config), ro RunOptions, agents ...Agent) (string, *Server) {
	t.Helper()
	ln := tcpListener(t)
	cfg := Config{Token: goodToken, Hosts: []string{ln.Addr().String()}, Version: "1"}
	if tweak != nil {
		tweak(&cfg)
	}
	if len(agents) == 0 {
		agents = []Agent{newFake("Bob", defaultSkills...)}
	}
	s, err := New(cfg, agents)
	if err != nil {
		t.Fatal(err)
	}
	if ro.WriteTimeout == 0 {
		ro.WriteTimeout = s.WriteTimeout()
	}
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, s, nil, ro) }()
	t.Cleanup(func() {
		stop()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("the server did not stop")
		}
		s.Close()
	})
	return ln.Addr().String(), s
}

// realPost sends a message to the agent and returns the status, the headers and the body.
// It does not stop the test, so it can be used from the goroutines of a test.
func realPost(address, path, body string) (int, http.Header, string, error) {
	req, err := http.NewRequest(http.MethodPost, "http://"+address+path, strings.NewReader(body))
	if err != nil {
		return 0, nil, "", err
	}
	req.Header.Set("Authorization", "Bearer "+goodToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := realClient.Do(req)
	if err != nil {
		return 0, nil, "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, string(raw), nil
}

// mustPost is realPost for the goroutine of the test: a failure to talk stops it.
func mustPost(t *testing.T, address, path, body string) (int, http.Header, string) {
	t.Helper()
	status, header, text, err := realPost(address, path, body)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return status, header, text
}

func sendTo(skill, args string) string {
	return rpcBody("SendMessage", message(dataPart(skill, args), ""))
}

// req: S6
func TestABodyOfTwoMiBIsRefusedWith413BeforeItIsRead(t *testing.T) {
	address, _ := realServer(t, nil, RunOptions{})
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Only the header is sent; the body that it promises is never written. The answer has to
	// come all the same, because the server does not wait for what it will not read.
	header := fmt.Sprintf("POST /agents/Bob HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/json\r\nContent-Length: %d\r\n\r\n",
		address, goodToken, 2<<20)
	if _, err := io.WriteString(conn, header); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("no answer to a body that is too large: %v", err)
	}
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

// req: S6
func TestSlowConnectionsDoNotStopANormalRequestAndAreCutByTheTimeout(t *testing.T) {
	address, _ := realServer(t, nil, RunOptions{ReadHeaderTimeout: 300 * time.Millisecond, MaxConnections: 256})
	slow := make([]net.Conn, 100)
	for i := range slow {
		conn, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		// The start of a request that is never finished.
		if _, err := io.WriteString(conn, "POST /agents/Bob HTTP/1.1\r\nHost: "+address+"\r\nX-Slow: "); err != nil {
			t.Fatal(err)
		}
		slow[i] = conn
	}

	status, _, body := mustPost(t, address, "/agents/Bob", sendTo("echo", `{"text":"in spite of them"}`))
	if status != http.StatusOK || !strings.Contains(body, "echo: in spite of them") {
		t.Errorf("a normal request among 100 slow ones: %d %s", status, body)
	}

	for i, conn := range slow {
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, err := io.Copy(io.Discard, conn)
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			t.Fatalf("connection %d was still open after the time to read the header", i)
		}
	}
}

// req: S6
func TestThe65thTaskAtTheSameTimeGets503AndTheOthersFinish(t *testing.T) {
	holder := newFake("Bob", defaultSkills...)
	holder.hold = make(chan struct{})
	address, _ := realServer(t, func(c *Config) { c.MaxInFlight = 64 }, RunOptions{MaxConnections: 256}, holder)

	replies := holdTasks(address, 64)
	waitForTasks(t, holder, 64)

	status, header, body := mustPost(t, address, "/agents/Bob", sendTo("echo", `{"text":"x"}`))
	if status != http.StatusServiceUnavailable || header.Get("Retry-After") != "1" || !strings.Contains(body, "busy") {
		t.Errorf("the 65th task: %d, Retry-After %q, %q", status, header.Get("Retry-After"), body)
	}

	close(holder.hold)
	finishHeldTasks(t, replies, 64)
	if status, _, _ := mustPost(t, address, "/agents/Bob", sendTo("echo", `{"text":"again"}`)); status != http.StatusOK {
		t.Errorf("the server did not recover: %d", status)
	}
}

// heldReply is the answer to a request that was held.
type heldReply struct {
	status int
	body   string
}

// holdTasks sends n requests to the skill that holds, each one from a goroutine of its own, and gives
// their answers as they come.
func holdTasks(address string, n int) <-chan heldReply {
	replies := make(chan heldReply, n)
	for i := 0; i < n; i++ {
		go func() {
			status, _, body, err := realPost(address, "/agents/Bob", sendTo("hold", `{}`))
			if err != nil {
				body = err.Error()
			}
			replies <- heldReply{status, body}
		}()
	}
	return replies
}

// waitForTasks waits, for up to 15 seconds, until n tasks are running at the same time.
func waitForTasks(t *testing.T, holder *fakeAgent, n int) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for int(holder.entered.Load()) < n && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := int(holder.entered.Load()); got != n {
		t.Fatalf("%d tasks are running, want %d", got, n)
	}
}

// finishHeldTasks wants the n tasks that were held to answer, and to say they were released.
func finishHeldTasks(t *testing.T, replies <-chan heldReply, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case r := <-replies:
			if r.status != http.StatusOK || !strings.Contains(r.body, "released") {
				t.Errorf("a task that was running: %d %s", r.status, r.body)
			}
		case <-time.After(15 * time.Second):
			t.Fatalf("only %d of the %d tasks finished", i, n)
		}
	}
}

// Fifty conversations at the same time, each with its own memory, against agents of the
// interpreter. This is the test that puts the runtime, the memory of the conversations and
// the door to work together; run with -race it is the best test of concurrency of the project.
//
// req: S6, S7
func TestFiftyRequestsAtTheSameTimeEachKeepsItsOwnMemory(t *testing.T) {
	rt := realRuntime(t)
	defs, err := lang.ParseFile("notes.ag", "", notesSource)
	if err != nil {
		t.Fatal(err)
	}
	address, server := realServer(t, func(c *Config) { c.MaxInFlight = 64 }, RunOptions{MaxConnections: 256}, NewRuntimeAgent(rt, defs[0]))

	var wg sync.WaitGroup
	problems := make(chan string, 200)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			mine := fmt.Sprintf("value-%d", i)
			status, _, body, err := realPost(address, "/agents/Notes", sendTo("remember", fmt.Sprintf(`{"what":%q}`, mine)))
			if err != nil || status != http.StatusOK {
				problems <- fmt.Sprintf("%d remember: %d %s %v", i, status, body, err)
				return
			}
			id := between(body, `"contextId":"`, `"`)
			status, _, body, err = realPost(address, "/agents/Notes", rpcBody("SendMessage", message(dataPart("recall", `{}`), `"contextId":"`+id+`"`)))
			if err != nil || status != http.StatusOK || !strings.Contains(body, `"text":"`+mine+`"`) {
				problems <- fmt.Sprintf("%d recall: %d %s %v, wanted %s", i, status, body, err, mine)
			}
		}(i)
	}
	wg.Wait()
	close(problems)
	for p := range problems {
		t.Error(p)
	}
	if got := server.contexts.Len(); got != 50 {
		t.Errorf("%d conversations, want 50", got)
	}
}

func between(text, from, to string) string {
	i := strings.Index(text, from)
	if i < 0 {
		return ""
	}
	rest := text[i+len(from):]
	if j := strings.Index(rest, to); j >= 0 {
		return rest[:j]
	}
	return ""
}
