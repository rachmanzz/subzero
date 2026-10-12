package transport

import (
	"context"
	"fmt"
	"net"
	"sync"
)

// Transport defines the common contract for all communication adapters in Subzero.
type Transport interface {
	// Scheme returns the unique identifier for the transport (e.g., "unix", "tcp", "http", "inproc").
	Scheme() string

	// Listen creates a network listener for receiving framed connections on the specified endpoint.
	Listen(ctx context.Context, endpoint string) (net.Listener, error)

	// Dial establishes a network connection to the target endpoint.
	Dial(ctx context.Context, endpoint string) (net.Conn, error)
}

var (
	registryMu sync.RWMutex
	transports = make(map[string]Transport)
)

// Register registers a transport implementation for its scheme.
func Register(t Transport) {
	registryMu.Lock()
	defer registryMu.Unlock()
	transports[t.Scheme()] = t
}

// Get returns the registered transport for a given scheme.
func Get(scheme string) (Transport, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	t, ok := transports[scheme]
	if !ok {
		return nil, fmt.Errorf("transport: unsupported scheme %q", scheme)
	}
	return t, nil
}
