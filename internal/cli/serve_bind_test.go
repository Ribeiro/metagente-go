package cli

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The port of `integration/serve_bind.rs` of the original project.

// outsideIP is the address of this computer on the network, when it has one besides loopback. No
// packet is sent: connecting a socket of UDP only chooses a route.
func outsideIP() net.IP {
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return nil
	}
	defer conn.Close()
	ip := conn.LocalAddr().(*net.UDPAddr).IP
	if ip.IsLoopback() || ip.IsUnspecified() {
		return nil
	}
	return ip
}

// req: S5
func TestByDefaultOnlyThisComputerCanConnect(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	live := startServe(t, []string{"hello.ag", "--port", "0"}, map[string]string{"METAGENTE_TOKEN": testToken}, false)
	defer live.stop(t)
	_, port, err := net.SplitHostPort(live.address)
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), 2*time.Second); err != nil {
		t.Fatalf("this computer could not connect: %v", err)
	} else {
		_ = conn.Close()
	}
	ip := outsideIP()
	if ip == nil {
		t.Skip("this computer has no network address besides loopback, so the outside cannot be tried")
	}
	if conn, err := net.DialTimeout("tcp", net.JoinHostPort(ip.String(), port), 500*time.Millisecond); err == nil {
		_ = conn.Close()
		t.Errorf("%s could connect without --public", ip)
	}
}

func TestAPortAlreadyInUseIsExplained(t *testing.T) {
	dir := project(t)
	writeFile(t, dir, "hello.ag", helloAgent)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := strconv.Itoa(held.Addr().(*net.TCPAddr).Port)
	var errOut bytes.Buffer
	code := serveCommand(context.Background(), []string{"hello.ag", "--port", port}, io.Discard, &errOut,
		serveEnv{getenv: func(name string) string { return map[string]string{"METAGENTE_TOKEN": testToken}[name] }})
	if code != 1 {
		t.Errorf("exit code = %d", code)
	}
	for _, want := range []string{"I could not listen on 127.0.0.1:" + port, "another program may be using that port", "--port"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("missing %q in:\n%s", want, errOut.String())
		}
	}
}
