package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/inproc"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

func main() {
	scheme := flag.String("scheme", "unix", "Transport scheme (unix, tcp, http)")
	endpoint := flag.String("endpoint", "/tmp/subzero-supervisor.sock", "Listen socket path or address")
	concurrency := flag.Int("concurrency", 16, "Maximum concurrent task limit")
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
		os.Exit(0)
	}()

	log.Printf("[Subzero Supervisor] Ready and awaiting tasks on %s", listener.Addr())
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

		// Handle connection
		_ = conn.Close()
	}
}
