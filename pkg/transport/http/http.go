package http

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

	"github.com/rachmanzz/subzero/pkg/transport"
)

const Scheme = "http"

func init() {
	transport.Register(New())
}

// Transport implements transport.Transport over HTTP using standard HTTP Upgrade/Switching Protocols.
// This allows full-duplex streaming through standard HTTP proxies and Ingress controllers.
type Transport struct{}

// New creates a new HTTP streaming transport adapter.
func New() *Transport {
	return &Transport{}
}

func (t *Transport) Scheme() string {
	return Scheme
}

// Listen starts an HTTP server on the given address (e.g. "127.0.0.1:9090" or ":9090")
// and returns a net.Listener whose Accept() yields hijacked full-duplex connections.
func (t *Transport) Listen(ctx context.Context, endpoint string) (net.Listener, error) {
	var lc net.ListenConfig
	rawListener, err := lc.Listen(ctx, "tcp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("http: listen tcp failed: %w", err)
	}

	hl := &httpListener{
		raw:      rawListener,
		connChan: make(chan net.Conn, 64),
		done:     make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/subzero/stream", hl.handleUpgrade)

	server := &http.Server{
		Handler: mux,
	}
	hl.server = server

	go func() {
		_ = server.Serve(rawListener)
	}()

	return hl, nil
}

// Dial connects to an HTTP streaming server endpoint via HTTP Upgrade protocol.
func (t *Transport) Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	host := endpoint
	host = strings.TrimPrefix(host, "http://")
	host = strings.TrimPrefix(host, "https://")
	path := "/subzero/stream"
	if idx := strings.Index(host, "/"); idx != -1 {
		path = host[idx:]
		host = host[:idx]
	}

	var d net.Dialer
	rawConn, err := d.DialContext(ctx, "tcp", host)
	if err != nil {
		return nil, fmt.Errorf("http: dial %q failed: %w", host, err)
	}

	// Send HTTP Upgrade Request
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: subzero\r\nConnection: Upgrade\r\n\r\n", path, host)
	if _, err := rawConn.Write([]byte(req)); err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("http: failed to send upgrade request: %w", err)
	}

	// Read response status
	br := bufio.NewReader(rawConn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	if err != nil {
		_ = rawConn.Close()
		return nil, fmt.Errorf("http: failed to read upgrade response: %w", err)
	}

	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = rawConn.Close()
		return nil, fmt.Errorf("http: upgrade failed with status %d", resp.StatusCode)
	}

	// If the buffered reader has any unread bytes, wrap it
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: rawConn, r: io.MultiReader(br, rawConn)}, nil
	}

	return rawConn, nil
}

type httpListener struct {
	raw      net.Listener
	server   *http.Server
	connChan chan net.Conn
	done     chan struct{}
	once     sync.Once
}

func (l *httpListener) handleUpgrade(w http.ResponseWriter, r *http.Request) {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "subzero") {
		http.Error(w, "upgrade to subzero required", http.StatusBadRequest)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}

	conn, brw, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "hijack failed", http.StatusInternalServerError)
		return
	}

	// Send HTTP 101 Switching Protocols
	_, _ = brw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: subzero\r\nConnection: Upgrade\r\n\r\n")
	_ = brw.Flush()

	var wrapped net.Conn = conn
	if brw.Reader.Buffered() > 0 {
		wrapped = &bufferedConn{Conn: conn, r: io.MultiReader(brw.Reader, conn)}
	}

	select {
	case <-l.done:
		_ = wrapped.Close()
	case l.connChan <- wrapped:
	}
}

func (l *httpListener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, errors.New("http: listener closed")
	case conn, ok := <-l.connChan:
		if !ok {
			return nil, errors.New("http: listener closed")
		}
		return conn, nil
	}
}

func (l *httpListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.server.Close()
	})
	return err
}

func (l *httpListener) Addr() net.Addr {
	return l.raw.Addr()
}

type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (bc *bufferedConn) Read(p []byte) (int, error) {
	return bc.r.Read(p)
}
