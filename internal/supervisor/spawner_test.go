package supervisor_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
)

func TestProcessSpawnerLifecycle(t *testing.T) {
	// Locate compiled worker binary
	workerBin := filepath.Join("..", "..", "bin", "subzero-worker")
	if _, err := os.Stat(workerBin); os.IsNotExist(err) {
		t.Skip("skipping spawner test: bin/subzero-worker binary not found, run make build first")
	}

	tmpDir, err := os.MkdirTemp("", "subzero-spawner-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "worker.sock")
	secretKey := []byte("spawner-secret-key-32b-subzero!")

	spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: workerBin,
		Scheme:       "unix",
		Endpoint:     sockPath,
		SecretKey:    secretKey,
		IdleTimeout:  300 * time.Millisecond,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProcessSpawner failed: %v", err)
	}
	defer spawner.Stop()

	// 1. Initial State should be DORMANT
	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected state DORMANT, got %s", spawner.State())
	}

	// 2. Trigger Cold-Start
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning failed: %v", err)
	}
	coldStartLatency := time.Since(start)
	t.Logf("Cold-start latency: %v", coldStartLatency)

	if spawner.State() != supervisor.StateRunning {
		t.Fatalf("expected state RUNNING, got %s", spawner.State())
	}

	// 3. Verify communication with spawned worker
	tr, err := transport.Get("unix")
	if err != nil {
		t.Fatal(err)
	}

	conn, err := tr.Dial(ctx, sockPath)
	if err != nil {
		t.Fatalf("Dial spawned worker failed: %v", err)
	}
	defer conn.Close()

	task := &protocol.TaskMessage{
		TaskID:       "task-spawn-1",
		TenantID:     "tenant-spawner",
		ActionTarget: "ping",
	}
	taskBytes, _ := protocol.EncodeTask(task, secretKey)
	if err := protocol.WriteFrame(conn, taskBytes, secretKey); err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}

	resPayload, err := protocol.ReadFrame(conn, secretKey, 0)
	if err != nil {
		t.Fatalf("ReadFrame failed: %v", err)
	}

	result, err := protocol.DecodeResult(resPayload)
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsSuccess || result.TaskID != task.TaskID {
		t.Fatalf("unexpected task result: %+v", result)
	}

	// Close client connection so worker becomes idle
	_ = conn.Close()

	// 4. Wait for idle watchdog auto-termination (Scale-to-Zero)
	deadline := time.Now().Add(2 * time.Second)
	scaledToZero := false
	for time.Now().Before(deadline) {
		if spawner.State() == supervisor.StateDormant {
			scaledToZero = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if !scaledToZero {
		t.Fatalf("worker did not auto-terminate back to DORMANT; current state: %s", spawner.State())
	}
	t.Log("Worker successfully scaled to zero!")
}

func TestProcessSpawner_WithCustomPolicy(t *testing.T) {
	workerBin := filepath.Join("..", "..", "bin", "subzero-worker")
	if _, err := os.Stat(workerBin); os.IsNotExist(err) {
		t.Skip("skipping spawner test: bin/subzero-worker binary not found, run make build first")
	}

	tmpDir, err := os.MkdirTemp("", "subzero-policy-spawner-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "worker.sock")
	secretKey := []byte("spawner-secret-key-32b-subzero!")

	// Configure spawner with a programmatic WorkerPolicy
	policy := permission.DefaultWorkerPolicy()
	policy.S3.AllowedPrefixes = []string{"s3://tenant-alpha-vault/"}

	spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: workerBin,
		Scheme:       "unix",
		Endpoint:     sockPath,
		SecretKey:    secretKey,
		IdleTimeout:  300 * time.Millisecond,
		ReadyTimeout: 5 * time.Second,
		WorkerPolicy: &policy,
	})
	if err != nil {
		t.Fatalf("NewProcessSpawner failed: %v", err)
	}
	defer spawner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning failed: %v", err)
	}

	if spawner.State() != supervisor.StateRunning {
		t.Fatalf("expected state RUNNING, got %s", spawner.State())
	}
}

