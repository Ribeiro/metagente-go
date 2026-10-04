package serve

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func tcpListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// req: S6
func TestTheListenerHoldsTheConnectionsOverTheLimitUntilAPlaceIsFree(t *testing.T) {
	ln := LimitListener(tcpListener(t), 2)
	defer ln.Close()
	accepted := make(chan net.Conn, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			accepted <- conn
		}
	}()
	var clients []net.Conn
	for i := 0; i < 3; i++ {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		clients = append(clients, c)
	}
	first, second := <-accepted, <-accepted
	select {
	case <-accepted:
		t.Fatal("a third connection was taken although the limit is two")
	case <-time.After(150 * time.Millisecond):
	}
	_ = first.Close()
	select {
	case third := <-accepted:
		_ = third.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("the connection that waited was not taken when a place was freed")
	}
	_ = second.Close()
	_ = clients
}

func TestClosingTheListenerFreesWhoWaitsForAPlace(t *testing.T) {
	ln := LimitListener(tcpListener(t), 1)
	go func() { _, _ = net.Dial("tcp", ln.Addr().String()) }()
	held, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	result := make(chan error, 1)
	go func() { _, err := ln.Accept(); result <- err }()
	time.Sleep(50 * time.Millisecond)
	_ = ln.Close()
	select {
	case err := <-result:
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Accept stayed waiting after the listener was closed")
	}
	// A place is given back once, however many times a connection is closed.
	_ = held.Close()
	_ = held.Close()
}

func get(t *testing.T, client *http.Client, url string) string {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestTheServerAnswersAndStopsWhenTheContextEnds(t *testing.T) {
	ln := tcpListener(t)
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "ok") }),
			nil, RunOptions{WriteTimeout: time.Minute, MaxConnections: 10, Grace: time.Second})
	}()
	if got := get(t, http.DefaultClient, "http://"+ln.Addr().String()); got != "ok" {
		t.Errorf("got %q", got)
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("a server that was stopped returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop")
	}
	if c, err := net.DialTimeout("tcp", ln.Addr().String(), 200*time.Millisecond); err == nil {
		c.Close()
		t.Error("the port is still open")
	}
}

func TestARequestStillRunningWhenTheGraceIsOverIsCut(t *testing.T) {
	ln := tcpListener(t)
	started := make(chan struct{})
	var cut atomic.Bool
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
			cut.Store(true)
		case <-time.After(10 * time.Second):
		}
	})
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, ln, handler, nil, RunOptions{WriteTimeout: time.Minute, Grace: 100 * time.Millisecond})
	}()
	go func() { _, _ = http.Get("http://" + ln.Addr().String()) }()
	<-started
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the server did not stop while a request was running")
	}
	deadline := time.Now().Add(2 * time.Second)
	for !cut.Load() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !cut.Load() {
		t.Error("the request that was running was not told to stop")
	}
}

func TestAServerThatCannotServeSaysSo(t *testing.T) {
	ln := tcpListener(t)
	_ = ln.Close()
	err := Serve(context.Background(), ln, http.NotFoundHandler(), nil, RunOptions{})
	if err == nil {
		t.Error("serving on a closed listener was said to work")
	}
}

// ---------- TLS ----------

func selfSigned(t *testing.T) (certFile, keyFile string, pool *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	pool = x509.NewCertPool()
	pool.AppendCertsFromPEM(certPEM)
	return certFile, keyFile, pool
}

// req: S5
func TestTLSSpeaksNothingOlderThanTLS12AndOnlyHTTP11(t *testing.T) {
	certFile, keyFile, pool := selfSigned(t)
	plan := &Plan{TLS: true, CertFile: certFile, KeyFile: keyFile}
	config, err := plan.TLSConfig()
	if err != nil || config == nil {
		t.Fatalf("config %v, err %v", config, err)
	}
	if config.MinVersion != tls.VersionTLS12 {
		t.Errorf("MinVersion = %x", config.MinVersion)
	}
	ln := tcpListener(t)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	go func() {
		_ = Serve(ctx, ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "secure") }),
			config, RunOptions{WriteTimeout: time.Minute})
	}()
	address := "https://" + ln.Addr().String()

	modern := &http.Client{Transport: &http.Transport{
		TLSClientConfig:   &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2: true, // asks for HTTP/2; the server must not accept
	}}
	resp, err := modern.Get(address)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "secure" || resp.ProtoMajor != 1 || resp.TLS.NegotiatedProtocol == "h2" {
		t.Errorf("body %q, proto %s, negotiated %q", body, resp.Proto, resp.TLS.NegotiatedProtocol)
	}

	old := &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS10, MaxVersion: tls.VersionTLS11},
	}}
	if resp, err := old.Get(address); err == nil {
		resp.Body.Close()
		t.Error("a client that speaks only TLS 1.1 was served")
	}
}

func TestACertificateThatCannotBeReadIsExplainedWithoutItsPath(t *testing.T) {
	certFile, keyFile, _ := selfSigned(t)
	otherCert, _, _ := selfSigned(t)
	for name, plan := range map[string]*Plan{
		"missing":  {TLS: true, CertFile: "/no/such/cert.pem", KeyFile: keyFile},
		"mixed up": {TLS: true, CertFile: otherCert, KeyFile: keyFile},
		"not pem":  {TLS: true, CertFile: keyFile, KeyFile: certFile},
	} {
		_, err := plan.TLSConfig()
		text := problem(t, err)
		if !strings.Contains(text, "could not read the certificate or its key") {
			t.Errorf("%s: %s", name, text)
		}
		if strings.Contains(text, "/no/such") {
			t.Errorf("%s: the path was repeated", name)
		}
	}
	if config, err := (&Plan{}).TLSConfig(); config != nil || err != nil {
		t.Errorf("a plan without TLS: %v %v", config, err)
	}
}
