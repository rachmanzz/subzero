// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strings"
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
	workerScheme := flag.String("worker-scheme", "", "Transport scheme for worker communication (defaults to unix if .sock, else supervisor scheme)")
	workerEndpoint := flag.String("worker-endpoint", "/tmp/subzero-worker-spawned.sock", "Socket path or address for spawned worker")
	spawnerType := flag.String("spawner", "process", "Worker spawner type: process or container (PRD 5.2)")
	containerEngine := flag.String("container-engine", "docker", "Container engine (docker or podman)")
	containerImage := flag.String("container-image", "subzero-worker:latest", "Container image for worker sandbox")
	secretKey := flag.String("secret", "subzero-secret-key-32b-default!", "HMAC secret key")
	concurrency := flag.Int("concurrency", 16, "Maximum concurrent task limit")
	idleTimeout := flag.Duration("idle-timeout", 15*time.Second, "Worker idle timeout before scale-to-zero")
	flag.Parse()

	actualWorkerScheme := *workerScheme
	if actualWorkerScheme == "" {
		if strings.HasSuffix(*workerEndpoint, ".sock") || strings.HasPrefix(*workerEndpoint, "/") {
			actualWorkerScheme = "unix"
		} else {
			actualWorkerScheme = *scheme
		}
	}

	log.Printf("[Subzero Supervisor] Starting daemon on %s://%s (concurrency: %d, spawner: %s, workerScheme: %s)...", *scheme, *endpoint, *concurrency, *spawnerType, actualWorkerScheme)

	tr, err := transport.Get(*scheme)
	if err != nil {
		log.Fatalf("[Subzero Supervisor] Invalid transport scheme: %v", err)
	}

	workerTr, err := transport.Get(actualWorkerScheme)
	if err != nil {
		log.Fatalf("[Subzero Supervisor] Invalid worker transport scheme: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	listener, err := tr.Listen(ctx, *endpoint)
	if err != nil {
		log.Fatalf("[Subzero Supervisor] Failed to listen on %s: %v", *endpoint, err)
	}
	defer listener.Close()

	// Initialize Automated Worker Spawner for Cold-Start / Scale-to-Zero management
	var spawner supervisor.WorkerSpawner
	if *spawnerType == "container" {
		spawner, err = supervisor.NewContainerSpawner(supervisor.ContainerSpawnerConfig{
			Engine:       *containerEngine,
			Image:        *containerImage,
			Scheme:       actualWorkerScheme,
			Endpoint:     *workerEndpoint,
			SecretKey:    []byte(*secretKey),
			IdleTimeout:  *idleTimeout,
			ReadyTimeout: 5 * time.Second,
		})
	} else {
		binPath := *workerBin
		if _, err := os.Stat(binPath); err != nil {
			if _, err2 := os.Stat("/usr/local/bin/subzero-worker"); err2 == nil {
				binPath = "/usr/local/bin/subzero-worker"
			} else if lp, err3 := exec.LookPath("subzero-worker"); err3 == nil {
				binPath = lp
			}
		}

		spawner, err = supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
			WorkerBinary: binPath,
			Scheme:       actualWorkerScheme,
			Endpoint:     *workerEndpoint,
			SecretKey:    []byte(*secretKey),
			IdleTimeout:  *idleTimeout,
			ReadyTimeout: 5 * time.Second,
		})
	}
	if err != nil {
		log.Fatalf("[Subzero Supervisor] Failed to create worker spawner: %v", err)
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

		go handleClientTask(ctx, conn, workerTr, spawner, dispatcher, *workerEndpoint, []byte(*secretKey))
	}
}

type relayTask struct {
	clientConn net.Conn
	tr         transport.Transport
	spawner    supervisor.WorkerSpawner
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

	// Decode task to check if chunk fan-out is needed
	taskMsg, err := protocol.DecodeTask(rawPayload)
	if err == nil {
		if plan, isChunked := supervisor.BuildChunkFanOutPlan(taskMsg); isChunked {
			runner := supervisor.TaskRunnerFunc(func(subCtx context.Context, subTask *protocol.TaskMessage) (*protocol.ResultMessage, error) {
				workerConn, err := r.tr.Dial(subCtx, r.workerAddr)
				if err != nil {
					return nil, err
				}
				defer workerConn.Close()

				subBytes, err := protocol.EncodeTask(subTask, r.secretKey)
				if err != nil {
					return nil, err
				}
				if err := protocol.WriteFrame(workerConn, subBytes, r.secretKey); err != nil {
					return nil, err
				}
				resBytes, err := protocol.ReadFrame(workerConn, r.secretKey, 0)
				if err != nil {
					return nil, err
				}
				return protocol.DecodeResult(resBytes)
			})

			chunkedTask := supervisor.NewChunkedTask(plan, runner)
			_ = chunkedTask.Execute(ctx, pool)
			resPayload, _ := protocol.EncodeResult(chunkedTask.Result())
			return protocol.WriteFrame(r.clientConn, resPayload, r.secretKey)
		}
	}

	// 3. Single-task direct forwarding to worker
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
	spawner supervisor.WorkerSpawner,
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
