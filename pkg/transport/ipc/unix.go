package ipc

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"

	"github.com/rachmanzz/subzero/pkg/transport"
)

const (
	SchemeUnix = "unix"
	SchemeIPC  = "ipc"
)

func init() {
	t := New()
	transport.Register(t)
	transport.Register(&aliasTransport{Transport: t, scheme: SchemeIPC})
}

type aliasTransport struct {
	*Transport
	scheme string
}

func (a *aliasTransport) Scheme() string {
	return a.scheme
}

// Transport implements transport.Transport using Unix Domain Sockets.
type Transport struct{}

// New creates a new Unix Domain Socket transport.
func New() *Transport {
	return &Transport{}
}

func (t *Transport) Scheme() string {
	return SchemeUnix
}

// Listen binds a Unix Domain Socket at the specified socket path.
// It creates parent directories if needed and removes stale socket files before binding.
func (t *Transport) Listen(ctx context.Context, endpoint string) (net.Listener, error) {
	// Clean and ensure parent directory exists
	dir := filepath.Dir(endpoint)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, fmt.Errorf("ipc: failed to create socket dir %q: %w", dir, err)
	}

	// Remove stale socket if present
	_ = os.Remove(endpoint)

	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("ipc: listen unix %q failed: %w", endpoint, err)
	}

	// Restrict permissions to owner and group (0660)
	if err := os.Chmod(endpoint, 0660); err != nil {
		_ = l.Close()
		return nil, fmt.Errorf("ipc: chmod socket %q failed: %w", endpoint, err)
	}

	return &cleanupListener{Listener: l, path: endpoint}, nil
}

// Dial connects to a Unix Domain Socket at the specified path.
func (t *Transport) Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", endpoint)
	if err != nil {
		return nil, fmt.Errorf("ipc: dial unix %q failed: %w", endpoint, err)
	}
	return conn, nil
}

// cleanupListener wraps net.Listener to ensure the socket file is unlinked on Close.
type cleanupListener struct {
	net.Listener
	path string
	once sync.Once
}

func (cl *cleanupListener) Close() error {
	var closeErr error
	cl.once.Do(func() {
		closeErr = cl.Listener.Close()
		_ = os.Remove(cl.path)
	})
	return closeErr
}
