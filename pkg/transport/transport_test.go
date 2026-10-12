package transport_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

func runTransportTest(t *testing.T, scheme, endpoint string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tr, err := transport.Get(scheme)
	if err != nil {
		t.Fatalf("[%s] transport.Get failed: %v", scheme, err)
	}

	listener, err := tr.Listen(ctx, endpoint)
	if err != nil {
		t.Fatalf("[%s] Listen failed on %s: %v", scheme, endpoint, err)
	}
	defer listener.Close()

	actualEndpoint := endpoint
	if scheme == "tcp" || scheme == "http" {
		actualEndpoint = listener.Addr().String()
	}

	secretKey := []byte("test-secret-key-32-bytes-subzero!")
	testPayload := []byte(fmt.Sprintf("hello from %s transport!", scheme))

	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrCh <- fmt.Errorf("accept failed: %w", err)
			return
		}
		defer conn.Close()

		received, err := protocol.ReadFrame(conn, secretKey, 0)
		if err != nil {
			serverErrCh <- fmt.Errorf("server read frame failed: %w", err)
			return
		}

		if !bytes.Equal(received, testPayload) {
			serverErrCh <- fmt.Errorf("server received mismatch: %q vs %q", received, testPayload)
			return
		}

		// Echo back
		if err := protocol.WriteFrame(conn, append([]byte("echo: "), received...), secretKey); err != nil {
			serverErrCh <- fmt.Errorf("server write frame failed: %w", err)
			return
		}
		serverErrCh <- nil
	}()

	clientConn, err := tr.Dial(ctx, actualEndpoint)
	if err != nil {
		t.Fatalf("[%s] Dial failed to %s: %v", scheme, actualEndpoint, err)
	}
	defer clientConn.Close()

	if err := protocol.WriteFrame(clientConn, testPayload, secretKey); err != nil {
		t.Fatalf("[%s] client WriteFrame failed: %v", scheme, err)
	}

	reply, err := protocol.ReadFrame(clientConn, secretKey, 0)
	if err != nil && err != io.EOF {
		t.Fatalf("[%s] client ReadFrame failed: %v", scheme, err)
	}

	expectedReply := append([]byte("echo: "), testPayload...)
	if !bytes.Equal(reply, expectedReply) {
		t.Fatalf("[%s] client reply mismatch: expected %q, got %q", scheme, expectedReply, reply)
	}

	if err := <-serverErrCh; err != nil {
		t.Fatalf("[%s] server error: %v", scheme, err)
	}
}

func TestInprocTransport(t *testing.T) {
	runTransportTest(t, "inproc", "test-pipe-1")
}

func TestIPCTransport(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subzero-ipc-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "worker.sock")
	runTransportTest(t, "ipc", sockPath)
}

func TestTCPTransport(t *testing.T) {
	runTransportTest(t, "tcp", "127.0.0.1:0")
}

func TestHTTPTransport(t *testing.T) {
	// Pick an ephemeral port for testing HTTP
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	runTransportTest(t, "http", addr)
}
