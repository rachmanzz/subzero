package transport_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
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
	"github.com/rachmanzz/subzero/pkg/transport/tcp"
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

func TestTCPTransport_mTLS(t *testing.T) {
	// Generate self-signed CA and client/server certificates for testing
	caCertPEM, caKeyPEM, err := generateTestCA()
	if err != nil {
		t.Fatalf("failed to generate CA: %v", err)
	}

	serverCertPEM, serverKeyPEM, err := generateTestCert(caCertPEM, caKeyPEM, "127.0.0.1", true)
	if err != nil {
		t.Fatalf("failed to generate server cert: %v", err)
	}

	clientCertPEM, clientKeyPEM, err := generateTestCert(caCertPEM, caKeyPEM, "client-node", false)
	if err != nil {
		t.Fatalf("failed to generate client cert: %v", err)
	}

	serverTLS := &tcp.TLSConfig{
		CertPEM: serverCertPEM,
		KeyPEM:  serverKeyPEM,
		CAPEM:   caCertPEM,
	}

	clientTLS := &tcp.TLSConfig{
		CertPEM:    clientCertPEM,
		KeyPEM:     clientKeyPEM,
		CAPEM:      caCertPEM,
		ServerName: "127.0.0.1",
	}

	serverTransport := tcp.New(serverTLS)
	clientTransport := tcp.New(clientTLS)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	listener, err := serverTransport.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mTLS listen failed: %v", err)
	}
	defer listener.Close()

	endpoint := listener.Addr().String()
	secretKey := []byte("mtls-secret-key-32-bytes-subzero")
	testPayload := []byte("secure mTLS message payload")

	serverErrCh := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverErrCh <- err
			return
		}
		defer conn.Close()

		received, err := protocol.ReadFrame(conn, secretKey, 0)
		if err != nil {
			serverErrCh <- err
			return
		}

		_ = protocol.WriteFrame(conn, append([]byte("mtls-ack: "), received...), secretKey)
		serverErrCh <- nil
	}()

	clientConn, err := clientTransport.Dial(ctx, endpoint)
	if err != nil {
		t.Fatalf("mTLS dial failed: %v", err)
	}
	defer clientConn.Close()

	if err := protocol.WriteFrame(clientConn, testPayload, secretKey); err != nil {
		t.Fatalf("mTLS write frame failed: %v", err)
	}

	reply, err := protocol.ReadFrame(clientConn, secretKey, 0)
	if err != nil {
		t.Fatalf("mTLS read frame failed: %v", err)
	}

	expectedReply := append([]byte("mtls-ack: "), testPayload...)
	if !bytes.Equal(reply, expectedReply) {
		t.Fatalf("unexpected mTLS reply: %q", reply)
	}

	if err := <-serverErrCh; err != nil {
		t.Fatalf("server mTLS error: %v", err)
	}

	// Verify that unauthenticated client (no client cert) is rejected by mTLS server
	unauthTransport := tcp.New(&tcp.TLSConfig{
		CAPEM:      caCertPEM,
		ServerName: "127.0.0.1",
	})
	unauthConn, err := unauthTransport.Dial(ctx, endpoint)
	if err != nil {
		// Connection was rejected during Dial / TLS handshake as expected
		t.Logf("✓ Unauthenticated client rejected at Dial time: %v", err)
	} else {
		// If dial succeeded (e.g. deferred TLS handshake), attempting frame write/read must fail
		_ = protocol.WriteFrame(unauthConn, testPayload, secretKey)
		_, readErr := protocol.ReadFrame(unauthConn, secretKey, 0)
		_ = unauthConn.Close()
		if readErr == nil {
			t.Fatal("expected unauthenticated client connection to fail on mTLS listener, but frame read succeeded")
		}
		t.Logf("✓ Unauthenticated client rejected at handshake/read time: %v", readErr)
	}
}

func generateTestCA() ([]byte, []byte, error) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "Subzero Test CA",
		},
		NotBefore:             time.Now().Add(-1 * time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certPEM, keyPEM, nil
}

func generateTestCert(caCertPEM, caKeyPEM []byte, commonName string, isServer bool) ([]byte, []byte, error) {
	caCertBlock, _ := pem.Decode(caCertPEM)
	caKeyBlock, _ := pem.Decode(caKeyPEM)

	caCert, err := x509.ParseCertificate(caCertBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	caKey, err := x509.ParsePKCS1PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject: pkix.Name{
			CommonName: commonName,
		},
		NotBefore: time.Now().Add(-1 * time.Hour),
		NotAfter:  time.Now().Add(24 * time.Hour),
	}

	if isServer {
		tmpl.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &priv.PublicKey, caKey)
	if err != nil {
		return nil, nil, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(priv)})
	return certPEM, keyPEM, nil
}


