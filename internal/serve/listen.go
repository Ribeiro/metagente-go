package serve

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/Ribeiro/metagente-go/internal/diag"
)

// RunOptions are the limits of the listener (requirement S6).
type RunOptions struct {
	// WriteTimeout is how long an answer may take to be written. It has to be longer
	// than the longest request, or an answer would be cut off at the end of its work.
	WriteTimeout time.Duration
	// MaxConnections is the most connections open at once. Zero means no limit.
	MaxConnections int
	// MaxConnectionsPerAddress is the most connections open at once from one place
	// (see addressGroup). Zero means no limit: it is only for a server that faces the
	// network by itself, since on this computer, or behind a proxy, every client comes
	// from the same address.
	MaxConnectionsPerAddress int
	// ReadHeaderTimeout, ReadTimeout and IdleTimeout are the usual ones of a server:
	// 5 s, 30 s and 60 s when zero.
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	IdleTimeout       time.Duration
	// MaxHeaderBytes is the most the headers of a request may have. 16 KiB when zero.
	MaxHeaderBytes int
	// Grace is how long a request that is running may take to finish when the server
	// is stopped. 10 seconds when zero.
	Grace time.Duration
}

// TLSConfig reads the certificate of the plan. It is nil for a plan without TLS.
// Nothing older than TLS 1.2 is spoken.
func (p *Plan) TLSConfig() (*tls.Config, error) {
	if !p.TLS {
		return nil, nil
	}
	cert, err := tls.LoadX509KeyPair(p.CertFile, p.KeyFile)
	if err != nil {
		return nil, diag.New("I could not read the certificate or its key").
			Fix("check --tls-cert and --tls-key: both are PEM files, and the key has to be the one of the certificate")
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, nil
}

// Serve answers on the listener until the context ends, and then stops: new
// connections are no longer taken, and a request that is running gets the grace
// time to finish before it is cut.
//
// The server speaks HTTP/1.1 only. HTTP/2 would make the door of the server the
// door of a protocol with more ways to be abused, for a server whose clients are
// programs that make one request at a time.
func Serve(ctx context.Context, ln net.Listener, handler http.Handler, tlsConfig *tls.Config, ro RunOptions) error {
	if ro.MaxConnectionsPerAddress > 0 {
		ln = LimitPerAddress(ln, ro.MaxConnectionsPerAddress)
	}
	if ro.MaxConnections > 0 {
		ln = LimitListener(ln, ro.MaxConnections)
	}
	if ro.Grace <= 0 {
		ro.Grace = 10 * time.Second
	}
	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: orDuration(ro.ReadHeaderTimeout, 5*time.Second),
		ReadTimeout:       orDuration(ro.ReadTimeout, 30*time.Second),
		WriteTimeout:      ro.WriteTimeout,
		IdleTimeout:       orDuration(ro.IdleTimeout, 60*time.Second),
		MaxHeaderBytes:    orInt(ro.MaxHeaderBytes, 16<<10),
		TLSConfig:         tlsConfig,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
		ErrorLog:          log.New(io.Discard, "", 0),
	}
	done := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			done <- srv.ServeTLS(ln, "", "")
			return
		}
		done <- srv.Serve(ln)
	}()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), ro.Grace)
	defer cancel()
	if err := srv.Shutdown(stopCtx); err != nil {
		_ = srv.Close() // what is still running when the grace is over is cut
	}
	if err := <-done; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func orDuration(d, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}

func orInt(n, fallback int) int {
	if n > 0 {
		return n
	}
	return fallback
}

// LimitListener makes a listener take at most n connections at once. When it is
// full it stops accepting, and the system keeps the ones that wait; the timeouts of
// the server decide how long a connection may hold its place without doing
// anything.
func LimitListener(l net.Listener, n int) net.Listener {
	return &limitListener{Listener: l, places: make(chan struct{}, n), closed: make(chan struct{})}
}

type limitListener struct {
	net.Listener
	places chan struct{}
	closed chan struct{}
	once   sync.Once
}

func (l *limitListener) Accept() (net.Conn, error) {
	select {
	case l.places <- struct{}{}:
	case <-l.closed:
		return nil, net.ErrClosed
	}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.places
		return nil, err
	}
	return &limitConn{Conn: conn, release: func() { <-l.places }}, nil
}

func (l *limitListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return l.Listener.Close()
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}

// LimitPerAddress makes a listener keep at most n connections at once from one place. A
// connection over that is closed as soon as it is accepted, and never takes one of the
// places of the server: one place cannot hold all of them by opening connections and
// sending nothing, or sending it slowly.
func LimitPerAddress(l net.Listener, n int) net.Listener {
	return &perAddressListener{Listener: l, limit: n, open: map[string]int{}}
}

type perAddressListener struct {
	net.Listener
	limit int

	mu   sync.Mutex
	open map[string]int
}

func (l *perAddressListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		key := addressGroup(conn.RemoteAddr().String())
		if !l.take(key) {
			_ = conn.Close()
			continue
		}
		return &limitConn{Conn: conn, release: func() { l.give(key) }}, nil
	}
}

func (l *perAddressListener) take(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[key] >= l.limit {
		return false
	}
	l.open[key]++
	return true
}

func (l *perAddressListener) give(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open[key]--; l.open[key] <= 0 {
		delete(l.open, key)
	}
}
