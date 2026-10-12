package tcp

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"

	"github.com/rachmanzz/subzero/pkg/transport"
)

const Scheme = "tcp"

func init() {
	transport.Register(New(nil))
}

// Transport implements transport.Transport for cross-node host-to-host TCP with optional TLS/mTLS.
type Transport struct {
	tlsConfig *TLSConfig
}

// New creates a new TCP transport with the given TLS options.
func New(tlsConfig *TLSConfig) *Transport {
	return &Transport{tlsConfig: tlsConfig}
}

func (t *Transport) Scheme() string {
	return Scheme
}

// Listen creates a TCP listener (or TLS listener if TLS config is set).
func (t *Transport) Listen(ctx context.Context, endpoint string) (net.Listener, error) {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "tcp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("tcp: listen %q failed: %w", endpoint, err)
	}

	serverTLS, err := t.tlsConfig.BuildServerTLS()
	if err != nil {
		_ = l.Close()
		return nil, err
	}

	if serverTLS != nil {
		return tls.NewListener(l, serverTLS), nil
	}

	return l, nil
}

// Dial establishes a TCP connection to endpoint (using TLS if configured).
func (t *Transport) Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	clientTLS, err := t.tlsConfig.BuildClientTLS()
	if err != nil {
		return nil, err
	}

	if clientTLS != nil {
		var d tls.Dialer
		d.Config = clientTLS
		conn, err := d.DialContext(ctx, "tcp", endpoint)
		if err != nil {
			return nil, fmt.Errorf("tcp: tls dial %q failed: %w", endpoint, err)
		}
		return conn, nil
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", endpoint)
	if err != nil {
		return nil, fmt.Errorf("tcp: dial %q failed: %w", endpoint, err)
	}
	return conn, nil
}
