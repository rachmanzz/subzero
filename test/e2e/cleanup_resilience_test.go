package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
)

// TestSocketAndResourceCleanupResilience verifies PRD Section 9:
// Non-Functional Requirements: Resilience & Resource Cleanup
// Temporary files and socket descriptors must be cleaned up automatically upon process termination.
// Worker crash/exit must not leave lingering sockets or block subsequent cold-start spawn cycles.
func TestSocketAndResourceCleanupResilience(t *testing.T) {
	workerBin := getE2EWorkerBinary(t)
	tmpDir := t.TempDir()

	sockPath := filepath.Join(tmpDir, "resilience-worker.sock")
	secretKey := []byte("resilience-secret-key-32b-subzero!")

	spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: workerBin,
		Scheme:       "unix",
		Endpoint:     sockPath,
		SecretKey:    secretKey,
		IdleTimeout:  200 * time.Millisecond,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProcessSpawner failed: %v", err)
	}

	ctx := context.Background()

	// 1. Initial Cold-Start Spawn Cycle
	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("first EnsureRunning failed: %v", err)
	}
	if spawner.State() != supervisor.StateRunning {
		t.Fatalf("expected state RUNNING, got %v", spawner.State())
	}

	// Verify socket file exists on disk and is dialable
	if _, err := os.Stat(sockPath); os.IsNotExist(err) {
		t.Fatalf("expected socket file to exist on filesystem: %s", sockPath)
	}

	tr, _ := transport.Get("unix")
	conn, err := tr.Dial(ctx, sockPath)
	if err != nil {
		t.Fatalf("expected worker socket to be dialable: %v", err)
	}
	_ = conn.Close()

	// 2. Simulate supervisor stop / worker termination
	t.Log("[Resilience Test] Stopping worker process...")
	spawner.Stop()

	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected state DORMANT after Stop, got %v", spawner.State())
	}

	// 3. Second Cold-Start Spawn Cycle on the same socket path (Verify no "bind: address already in use" error)
	t.Log("[Resilience Test] Spawning worker on same socket path to verify no descriptor leak...")
	spawner2, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: workerBin,
		Scheme:       "unix",
		Endpoint:     sockPath,
		SecretKey:    secretKey,
		IdleTimeout:  200 * time.Millisecond,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProcessSpawner 2 failed: %v", err)
	}
	defer spawner2.Stop()

	if err := spawner2.EnsureRunning(ctx); err != nil {
		t.Fatalf("second EnsureRunning failed (socket re-bind error?): %v", err)
	}

	if spawner2.State() != supervisor.StateRunning {
		t.Fatalf("expected state RUNNING on second spawn, got %v", spawner2.State())
	}

	// Verify dialable again
	conn2, err := tr.Dial(ctx, sockPath)
	if err != nil {
		t.Fatalf("second dial failed: %v", err)
	}
	_ = conn2.Close()

	// 4. Verify scale-to-zero auto-termination
	t.Log("[Resilience Test] Awaiting idle timeout scale-to-zero...")
	deadline := time.Now().Add(600 * time.Millisecond)
	scaledToZero := false
	for time.Now().Before(deadline) {
		if spawner2.State() == supervisor.StateDormant {
			scaledToZero = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !scaledToZero {
		t.Errorf("worker did not auto-terminate to DORMANT within idle window")
	} else {
		t.Log("[Resilience Test SUCCESS] Worker safely cleaned up and returned to DORMANT without resource leaks")
	}

	spawner.Stop()
}
