package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/permission"
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
	policyFlag := flag.String("policy", "", "Path to worker permission policy file (JSON)")
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

	wasmEngine, err := wasm.NewEngine(ctx, wasm.DefaultConfig())
	if err != nil {
		log.Printf("[Subzero Worker] Warning: Wazero engine failed to initialize: %v (Wasm execution disabled)", err)
	} else {
		defer wasmEngine.Close(ctx)
	}

	resolver := func(target string) ([]byte, error) {
		// 1. Direct file path
		if strings.HasSuffix(target, ".wasm") {
			return os.ReadFile(target)
		}
		// 2. Check in data/wasm/<target>/module.wasm
		dataPath := filepath.Join(".", "data", "wasm", target, "module.wasm")
		if data, err := os.ReadFile(dataPath); err == nil {
			return data, nil
		}
		return nil, os.ErrNotExist
	}

	workerPolicy := resolveWorkerPolicy(*policyFlag)
	log.Printf("[Subzero Worker] Loaded worker capability policy (AllowedHostCalls: %d, S3Prefixes: %v)",
		len(workerPolicy.AllowedHostCalls), workerPolicy.S3.AllowedPrefixes)

	var wasmHandler worker.HandlerFunc
	if wasmEngine != nil {
		wasmHandler = worker.NewWasmHandlerWithConfig(worker.WasmHandlerConfig{
			Engine:       wasmEngine,
			Resolver:     resolver,
			WorkerPolicy: &workerPolicy,
		})
	}

	w := worker.New(worker.Config{
		SecretKey:   []byte(*secretKey),
		IdleTimeout: *idleTimeout,
		OnTerminate: func() {
			log.Println("[Subzero Worker] Idle timeout reached. Auto-terminating (scale-to-zero)...")
			os.Exit(0)
		},
	}, func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		log.Printf("[Subzero Worker] Executing task %s (tenant: %s, target: %s)...", task.TaskID, task.TenantID, task.ActionTarget)

		// If target can be resolved as Wasm, execute via Wasm sandbox
		if wasmHandler != nil {
			if _, err := resolver(task.ActionTarget); err == nil {
				return wasmHandler(ctx, task)
			}
		}

		// Native fallback
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

func resolveWorkerPolicy(policyPath string) permission.WorkerPolicy {
	// 1. Check -policy flag
	if policyPath != "" {
		data, err := os.ReadFile(policyPath)
		if err == nil {
			var p permission.WorkerPolicy
			if err := json.Unmarshal(data, &p); err == nil {
				return p
			}
			log.Printf("[Subzero Worker] Warning: failed to parse policy file %s: %v", policyPath, err)
		} else {
			log.Printf("[Subzero Worker] Warning: failed to read policy file %s: %v", policyPath, err)
		}
	}

	// 2. Check SUBZERO_WORKER_POLICY env
	if envVal := os.Getenv("SUBZERO_WORKER_POLICY"); envVal != "" {
		var p permission.WorkerPolicy
		if err := json.Unmarshal([]byte(envVal), &p); err == nil {
			return p
		} else {
			log.Printf("[Subzero Worker] Warning: failed to parse SUBZERO_WORKER_POLICY env: %v", err)
		}
	}

	// 3. Fallback to safe defaults
	return permission.DefaultWorkerPolicy()
}

