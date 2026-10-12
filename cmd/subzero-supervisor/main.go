package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

func main() {
	scheme := flag.String("scheme", "unix", "Transport scheme for Supervisor (unix, tcp, http)")
	endpoint := flag.String("endpoint", "/tmp/subzero-supervisor.sock", "Listen socket path or address")
	workerBin := flag.String("worker-bin", "./bin/subzero-worker", "Path to subzero-worker executable")
	workerEndpoint := flag.String("worker-endpoint", "/tmp/subzero-worker-spawned.sock", "Socket path or address for spawned worker")
	secretKey := flag.String("secret", "subzero-secret-key-32b-default!", "HMAC secret key")
	concurrency := flag.Int("concurrency", 16, "Maximum concurrent task limit")
	idleTimeout := flag.Duration("idle-timeout", 15*time.Second, "Worker idle timeout before scale-to-zero")
	flag.Parse()

	log.Printf("[Subzero Supervisor] Starting daemon on %s://%s (concurrency: %d)...", *scheme, *endpoint, *concurrency)

	tr, err := transport.Get(*scheme)
	if err != nil {
		log.Fatalf("[Subzero Supervisor] Invalid transport scheme: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listener, err := tr.Listen(ctx, *endpoint)
	if err != nil {
		log.Fatalf("[Subzero Supervisor] Failed to listen on %s: %v", *endpoint, err)
	}
	defer listener.Close()

	// Initialize Automated Process Spawner for Cold-Start / Scale-to-Zero management
	spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: *workerBin,
		Scheme:       *scheme,
		Endpoint:     *workerEndpoint,
		SecretKey:    []byte(*secretKey),
		IdleTimeout:  *idleTimeout,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		log.Fatalf("[Subzero Supervisor] Failed to create process spawner: %v", err)
	}
	defer spawner.Stop()

	// Initialize Task Concurrency Dispatcher
	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: *concurrency,
		QueueSize:   1024,
		SubLimit:    8,
	})
	dispatcher.Start()
	defer dispatcher.Stop()

	// Handle graceful shutdown on OS signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("[Subzero Supervisor] Shutdown signal received. Exiting gracefully...")
		_ = listener.Close()
		dispatcher.Stop()
		spawner.Stop()
		os.Exit(0)
	}()

	log.Printf("[Subzero Supervisor] Ready and awaiting tasks on %s (Scale-to-Zero Worker Spawner active)", listener.Addr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			select {
			case <-sigCh:
				return
			default:
				log.Printf("[Subzero Supervisor] Accept error: %v", err)
				time.Sleep(50 * time.Millisecond)
				continue
			}
		}

		go handleClientTask(ctx, conn, tr, spawner, dispatcher, *workerEndpoint, []byte(*secretKey))
	}
}

type relayTask struct {
	clientConn net.Conn
	tr         transport.Transport
	spawner    *supervisor.ProcessSpawner
	workerAddr string
	secretKey  []byte
}

func (r *relayTask) Name() string { return "task-relay" }
func (r *relayTask) Execute(ctx context.Context, pool supervisor.SubWorkerPool) error {
	defer r.clientConn.Close()

	// 1. Read framed task message from client
	rawPayload, err := protocol.ReadFrame(r.clientConn, r.secretKey, 0)
	if err != nil {
		return fmt.Errorf("read client frame failed: %w", err)
	}

	// 2. Ensure worker is running (Cold-Start if DORMANT)
	if err := r.spawner.EnsureRunning(ctx); err != nil {
		errRes := &protocol.ResultMessage{
			IsSuccess:    false,
			ErrorMessage: fmt.Sprintf("cold-start worker failed: %v", err),
		}
		data, _ := protocol.EncodeResult(errRes)
		_ = protocol.WriteFrame(r.clientConn, data, r.secretKey)
		return err
	}

	// 3. Forward task to worker
	workerConn, err := r.tr.Dial(ctx, r.workerAddr)
	if err != nil {
		errRes := &protocol.ResultMessage{
			IsSuccess:    false,
			ErrorMessage: fmt.Sprintf("dial worker failed: %v", err),
		}
		data, _ := protocol.EncodeResult(errRes)
		_ = protocol.WriteFrame(r.clientConn, data, r.secretKey)
		return err
	}
	defer workerConn.Close()

	if err := protocol.WriteFrame(workerConn, rawPayload, r.secretKey); err != nil {
		return fmt.Errorf("relay task to worker failed: %w", err)
	}

	// 4. Read result from worker and relay back to client
	resPayload, err := protocol.ReadFrame(workerConn, r.secretKey, 0)
	if err != nil {
		return fmt.Errorf("read worker result failed: %w", err)
	}

	return protocol.WriteFrame(r.clientConn, resPayload, r.secretKey)
}

func handleClientTask(
	ctx context.Context,
	conn net.Conn,
	tr transport.Transport,
	spawner *supervisor.ProcessSpawner,
	dispatcher *supervisor.Dispatcher,
	workerAddr string,
	secretKey []byte,
) {
	task := &relayTask{
		clientConn: conn,
		tr:         tr,
		spawner:    spawner,
		workerAddr: workerAddr,
		secretKey:  secretKey,
	}

	if err := dispatcher.ExecuteSync(ctx, task); err != nil {
		log.Printf("[Subzero Supervisor] Task relay error: %v", err)
	}
}
