package e2e_test

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	_ "github.com/rachmanzz/subzero/pkg/transport/http"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
)

var (
	e2eSupervisorBinPath string
	e2eSupervisorBuildMu sync.Mutex
)

func getE2ESupervisorBinary(t testing.TB) string {
	e2eSupervisorBuildMu.Lock()
	defer e2eSupervisorBuildMu.Unlock()

	if e2eSupervisorBinPath != "" {
		return e2eSupervisorBinPath
	}

	relBin := filepath.Join("..", "..", "bin", "subzero-supervisor")
	if _, err := os.Stat(relBin); err == nil {
		e2eSupervisorBinPath = relBin
		return e2eSupervisorBinPath
	}

	tmpDir, err := os.MkdirTemp("", "subzero-e2e-supervisor-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	binPath := filepath.Join(tmpDir, "subzero-supervisor")
	cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/subzero-supervisor")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build subzero-supervisor: %v, output: %s", err, string(out))
	}
	e2eSupervisorBinPath = binPath
	return e2eSupervisorBinPath
}

func TestSupervisorBinary_ProcessLifecycle(t *testing.T) {
	workerBin := getE2EWorkerBinary(t)
	supervisorBin := getE2ESupervisorBinary(t)

	tmpDir := t.TempDir()
	supervisorSocket := filepath.Join(tmpDir, "supervisor.sock")
	workerSocket := filepath.Join(tmpDir, "worker.sock")
	secretKey := []byte("subzero-secret-key-32b-default!")

	// Launch actual subzero-supervisor binary process
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, supervisorBin,
		"-scheme", "unix",
		"-endpoint", supervisorSocket,
		"-worker-bin", workerBin,
		"-worker-endpoint", workerSocket,
		"-spawner", "process",
		"-secret", string(secretKey),
		"-idle-timeout", "2s",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start subzero-supervisor binary: %v", err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	// Wait for supervisor to start listening on socket
	deadline := time.Now().Add(5 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("unix", supervisorSocket); err == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("subzero-supervisor binary failed to bind socket %s within 5s", supervisorSocket)
	}

	// Connect Producer client to the live supervisor process
	client, err := producer.New(producer.Config{
		Scheme:    "unix",
		Endpoint:  supervisorSocket,
		SecretKey: secretKey,
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	// Submit ping task to trigger live worker cold-start via supervisor
	task := &protocol.TaskMessage{
		TaskID:       "task-supervisor-bin-001",
		TenantID:     "tenant-live-supervisor",
		ActionTarget: "ping",
	}

	submitCtx, submitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer submitCancel()

	result, err := client.SubmitTask(submitCtx, task)
	if err != nil {
		t.Fatalf("SubmitTask to live subzero-supervisor binary failed: %v", err)
	}
	if !result.IsSuccess {
		t.Fatalf("task reported failure from live supervisor: %+v", result)
	}
	t.Logf("✓ Live subzero-supervisor binary executed task successfully: %+v", result)

	// Subtest: Chunk fan-out task execution via live supervisor binary
	t.Run("ChunkFanOut_LiveSupervisorBinary", func(t *testing.T) {
		chunkTask := &protocol.TaskMessage{
			TaskID:       "task-supervisor-bin-chunk-001",
			TenantID:     "tenant-live-supervisor",
			ActionTarget: "ping",
			CustomConfig: map[string]string{
				"sheets": "SheetA,SheetB,SheetC",
			},
		}

		cCtx, cCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cCancel()

		chunkRes, err := client.SubmitTask(cCtx, chunkTask)
		if err != nil {
			t.Fatalf("SubmitTask chunk fan-out to live supervisor failed: %v", err)
		}
		if !chunkRes.IsSuccess {
			t.Fatalf("chunk fan-out reported failure from live supervisor: %+v", chunkRes)
		}
		t.Logf("✓ Live supervisor binary successfully executed chunk fan-out task: %+v", chunkRes)
	})
}

func TestSupervisorBinary_HTTPIngressScheme(t *testing.T) {
	workerBin := getE2EWorkerBinary(t)
	supervisorBin := getE2ESupervisorBinary(t)

	tmpDir := t.TempDir()
	workerSocket := filepath.Join(tmpDir, "worker-http.sock")
	secretKey := []byte("subzero-secret-key-32b-default!")

	// Listen on ephemeral loopback TCP port for HTTP ingress
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen failed: %v", err)
	}
	httpEndpoint := l.Addr().String()
	_ = l.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, supervisorBin,
		"-scheme", "http",
		"-endpoint", httpEndpoint,
		"-worker-bin", workerBin,
		"-worker-endpoint", workerSocket,
		"-spawner", "process",
		"-secret", string(secretKey),
		"-idle-timeout", "2s",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start subzero-supervisor HTTP binary: %v", err)
	}
	defer func() {
		cancel()
		_ = cmd.Wait()
	}()

	// Wait for supervisor to start listening on HTTP address
	deadline := time.Now().Add(5 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		if conn, err := net.Dial("tcp", httpEndpoint); err == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("subzero-supervisor failed to bind http endpoint %s within 5s", httpEndpoint)
	}

	// Connect Producer client over HTTP streaming transport
	client, err := producer.New(producer.Config{
		Scheme:    "http",
		Endpoint:  httpEndpoint,
		SecretKey: secretKey,
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatalf("producer.New (http) failed: %v", err)
	}

	task := &protocol.TaskMessage{
		TaskID:       "task-http-ingress-bin-001",
		TenantID:     "tenant-http-live",
		ActionTarget: "ping",
	}

	submitCtx, submitCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer submitCancel()

	result, err := client.SubmitTask(submitCtx, task)
	if err != nil {
		t.Fatalf("SubmitTask via HTTP ingress to live supervisor binary failed: %v", err)
	}
	if !result.IsSuccess {
		t.Fatalf("task reported failure over HTTP ingress: %+v", result)
	}
	t.Logf("✓ Live subzero-supervisor binary executed task over HTTP ingress successfully: %+v", result)
}
