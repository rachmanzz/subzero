package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

func main() {
	scheme := flag.String("scheme", "unix", "Transport scheme (unix, tcp, http)")
	endpoint := flag.String("endpoint", "/tmp/subzero-worker.sock", "Listen socket path or address")
	secretKey := flag.String("secret", "subzero-secret-key-32b-default!", "HMAC secret key")
	idleTimeout := flag.Duration("idle-timeout", 30*time.Second, "Idle timeout duration before auto-terminating (scale-to-zero)")
	flag.Parse()

	log.Printf("[Subzero Worker] Spawning on %s://%s (idle-timeout: %v)...", *scheme, *endpoint, *idleTimeout)

	tr, err := transport.Get(*scheme)
	if err != nil {
		log.Fatalf("[Subzero Worker] Invalid transport scheme: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listener, err := tr.Listen(ctx, *endpoint)
	if err != nil {
		log.Fatalf("[Subzero Worker] Failed to listen on %s: %v", *endpoint, err)
	}
	defer listener.Close()

	w := worker.New(worker.Config{
		SecretKey:   []byte(*secretKey),
		IdleTimeout: *idleTimeout,
		OnTerminate: func() {
			log.Println("[Subzero Worker] Idle timeout reached. Auto-terminating (scale-to-zero)...")
			os.Exit(0)
		},
	}, func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		log.Printf("[Subzero Worker] Executing task %s (tenant: %s, target: %s)...", task.TaskID, task.TenantID, task.ActionTarget)
		return &protocol.ResultMessage{
			TaskID:        task.TaskID,
			IsSuccess:     true,
			ProcessedRows: 100,
		}, nil
	})
	defer w.Close()

	// Signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[Subzero Worker] Interrupt received. Shutting down...")
		_ = w.Close()
		os.Exit(0)
	}()

	log.Printf("[Subzero Worker] Serving on %s...", listener.Addr())
	if err := w.Serve(listener); err != nil {
		log.Printf("[Subzero Worker] Serve exited: %v", err)
	}
}
