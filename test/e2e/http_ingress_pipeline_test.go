package e2e_test

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
)

// TestHTTPIngressReverseProxyPipeline verifies PRD Section 5.2:
// HTTP/TCP Transport Driver: Full-duplex streaming communication passing through an Ingress Controller / Reverse Proxy.
func TestHTTPIngressReverseProxyPipeline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	tmpDir := t.TempDir()
	workerBin := getE2EWorkerBinary(t)

	secretKey := []byte("http-ingress-secret-key-32b-subzero!")
	workerSock := filepath.Join(tmpDir, "worker-http.sock")

	// 1. Initialize Supervisor Spawner with short idle timeout
	idleTimeout := 300 * time.Millisecond
	spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: workerBin,
		Scheme:       "unix",
		Endpoint:     workerSock,
		SecretKey:    secretKey,
		IdleTimeout:  idleTimeout,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProcessSpawner failed: %v", err)
	}
	defer spawner.Stop()

	// 2. Initialize Supervisor Dispatcher
	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		QueueSize:   32,
		SubLimit:    2,
	})
	dispatcher.Start()
	defer dispatcher.Stop()

	// 3. Start Supervisor Backend listening on HTTP transport scheme
	httpTr, err := transport.Get("http")
	if err != nil {
		t.Fatalf("transport.Get http failed: %v", err)
	}

	ipcTr, err := transport.Get("unix")
	if err != nil {
		t.Fatalf("transport.Get unix failed: %v", err)
	}

	// Listen on ephemeral localhost port
	supervisorListener, err := httpTr.Listen(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatalf("httpTr.Listen failed: %v", err)
	}
	defer supervisorListener.Close()

	supervisorAddr := supervisorListener.Addr().String()
	t.Logf("[Supervisor Backend] Listening on http://%s", supervisorAddr)

	// Supervisor accept loop relaying incoming HTTP stream connections
	go func() {
		for {
			conn, err := supervisorListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				rawPayload, err := protocol.ReadFrame(c, secretKey, 0)
				if err != nil {
					return
				}

				// Ensure worker is running
				if err := spawner.EnsureRunning(ctx); err != nil {
					errRes := &protocol.ResultMessage{
						IsSuccess:    false,
						ErrorMessage: fmt.Sprintf("cold-start worker failed: %v", err),
					}
					data, _ := protocol.EncodeResult(errRes)
					_ = protocol.WriteFrame(c, data, secretKey)
					return
				}

				// Dial worker process over local fast IPC
				wConn, err := ipcTr.Dial(ctx, workerSock)
				if err != nil {
					errRes := &protocol.ResultMessage{
						IsSuccess:    false,
						ErrorMessage: fmt.Sprintf("dial worker failed: %v", err),
					}
					data, _ := protocol.EncodeResult(errRes)
					_ = protocol.WriteFrame(c, data, secretKey)
					return
				}
				defer wConn.Close()

				if err := protocol.WriteFrame(wConn, rawPayload, secretKey); err != nil {
					return
				}

				resPayload, err := protocol.ReadFrame(wConn, secretKey, 0)
				if err != nil {
					return
				}

				_ = protocol.WriteFrame(c, resPayload, secretKey)
			}(conn)
		}
	}()

	// 4. Setup Simulated Ingress Controller / API Gateway Reverse Proxy
	backendURL, err := url.Parse("http://" + supervisorAddr)
	if err != nil {
		t.Fatalf("url.Parse backend failed: %v", err)
	}

	reverseProxy := httputil.NewSingleHostReverseProxy(backendURL)
	reverseProxy.FlushInterval = -1 // Immediate flush for streaming

	ingressServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Log proxy forwarding
		reverseProxy.ServeHTTP(w, r)
	}))
	defer ingressServer.Close()

	ingressAddr := ingressServer.Listener.Addr().String()
	t.Logf("[Ingress Controller Proxy] Listening on http://%s (forwarding to http://%s)", ingressAddr, supervisorAddr)

	// 5. Connect Producer Client through Ingress Controller endpoint
	producerClient, err := producer.New(producer.Config{
		Scheme:    "http",
		Endpoint:  ingressAddr,
		SecretKey: secretKey,
		Timeout:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	// Initial State: DORMANT
	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected initial state DORMANT, got %v", spawner.State())
	}

	// 6. Submit task via HTTP Ingress
	task := &protocol.TaskMessage{
		TaskID:       "task-ingress-http-01",
		TenantID:     "tenant-ingress",
		ActionTarget: "native-parser",
		SourceURI:    "s3://bucket/data.csv",
	}

	t.Logf("[Producer] Submitting task %s via Ingress Proxy (http://%s)...", task.TaskID, ingressAddr)
	start := time.Now()
	result, err := producerClient.SubmitTask(ctx, task)
	if err != nil {
		t.Fatalf("SubmitTask via Ingress failed: %v", err)
	}
	elapsed := time.Since(start)

	if !result.IsSuccess {
		t.Fatalf("task reported failure: %s", result.ErrorMessage)
	}

	t.Logf("[Success] Task processed via HTTP Ingress in %v (Worker cold-start & response succeeded, ProcessedRows: %d)",
		elapsed, result.ProcessedRows)

	// 7. Verify scale-to-zero after idle timeout
	t.Logf("[Monitor] Waiting for worker scale-to-zero auto-termination...")
	deadline := time.Now().Add(idleTimeout + 600*time.Millisecond)
	scaledToZero := false

	for time.Now().Before(deadline) {
		if spawner.State() == supervisor.StateDormant {
			scaledToZero = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}

	if !scaledToZero {
		t.Errorf("Scale-to-zero failed: worker remained in state %v after idle window", spawner.State())
	} else {
		t.Logf("[Scale-to-Zero Verified] Worker successfully reclaimed resources and returned to DORMANT")
	}
}
