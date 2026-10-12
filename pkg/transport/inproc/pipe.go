package inproc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/rachmanzz/subzero/pkg/transport"
)

const Scheme = "inproc"

func init() {
	transport.Register(New())
}

var (
	ErrListenerClosed = errors.New("inproc: listener closed")
	ErrNotFound       = errors.New("inproc: endpoint listener not found")
)

// Transport implements transport.Transport using in-memory net.Pipe.
type Transport struct {
	mu        sync.RWMutex
	listeners map[string]*Listener
}

// New creates a new in-memory pipe transport.
func New() *Transport {
	return &Transport{
		listeners: make(map[string]*Listener),
	}
}

func (t *Transport) Scheme() string {
	return Scheme
}

// Listen creates an in-memory listener on the named endpoint.
func (t *Transport) Listen(ctx context.Context, endpoint string) (net.Listener, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if _, exists := t.listeners[endpoint]; exists {
		return nil, fmt.Errorf("inproc: endpoint %q already bound", endpoint)
	}

	l := &Listener{
		endpoint: endpoint,
		ch:       make(chan net.Conn, 128),
		done:     make(chan struct{}),
		onClose: func() {
			t.mu.Lock()
			delete(t.listeners, endpoint)
			t.mu.Unlock()
		},
	}

	t.listeners[endpoint] = l
	return l, nil
}

// Dial connects to an in-memory listener on the named endpoint.
func (t *Transport) Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	t.mu.RLock()
	l, ok := t.listeners[endpoint]
	t.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, endpoint)
	}

	clientConn, serverConn := net.Pipe()

	select {
	case <-l.done:
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, ErrListenerClosed
	case <-ctx.Done():
		_ = clientConn.Close()
		_ = serverConn.Close()
		return nil, ctx.Err()
	case l.ch <- serverConn:
		return clientConn, nil
	}
}

// Listener represents an in-memory net.Listener.
type Listener struct {
	endpoint string
	ch       chan net.Conn
	done     chan struct{}
	once     sync.Once
	onClose  func()
}

func (l *Listener) Accept() (net.Conn, error) {
	select {
	case <-l.done:
		return nil, ErrListenerClosed
	case conn, ok := <-l.ch:
		if !ok {
			return nil, ErrListenerClosed
		}
		return conn, nil
	}
}

func (l *Listener) Close() error {
	l.once.Do(func() {
		close(l.done)
		if l.onClose != nil {
			l.onClose()
		}
	})
	return nil
}

func (l *Listener) Addr() net.Addr {
	return inprocAddr(l.endpoint)
}

type inprocAddr string

func (a inprocAddr) Network() string { return Scheme }
func (a inprocAddr) String() string  { return string(a) }
