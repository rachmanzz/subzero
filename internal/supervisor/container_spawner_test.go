package supervisor_test

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/transport"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

var (
	_ supervisor.WorkerSpawner = (*supervisor.ProcessSpawner)(nil)
	_ supervisor.WorkerSpawner = (*supervisor.ContainerSpawner)(nil)
)

// mockCommandProcess simulates a running container command process.
type mockCommandProcess struct {
	waitCh  chan struct{}
	killed  int32
	waitErr error
}

func (m *mockCommandProcess) Wait() error {
	<-m.waitCh
	return m.waitErr
}

func (m *mockCommandProcess) Kill() error {
	if atomic.CompareAndSwapInt32(&m.killed, 0, 1) {
		close(m.waitCh)
	}
	return nil
}

// mockCommandRunner records CLI commands and returns mock processes.
type mockCommandRunner struct {
	mu          sync.Mutex
	startedCmds [][]string
	runCmds     [][]string
	onStart     func(name string, args ...string) (supervisor.CommandProcess, error)
	customProc  *mockCommandProcess
}

func (m *mockCommandRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runCmds = append(m.runCmds, append([]string{name}, args...))
	return []byte("ok"), nil
}

func (m *mockCommandRunner) Start(ctx context.Context, name string, args ...string) (supervisor.CommandProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.startedCmds = append(m.startedCmds, append([]string{name}, args...))
	if m.onStart != nil {
		return m.onStart(name, args...)
	}
	if m.customProc != nil {
		return m.customProc, nil
	}
	return &mockCommandProcess{waitCh: make(chan struct{})}, nil
}

func TestContainerSpawner_BuildRunArgs(t *testing.T) {
	policy := &permission.WorkerPolicy{
		Filesystem: permission.PolicyFilesystem{
			AllowedReadOnlyRoots: []string{"/tmp/tenant-c1"},
		},
	}

	spawner, err := supervisor.NewContainerSpawner(supervisor.ContainerSpawnerConfig{
		Engine:        "docker",
		Image:         "custom-worker:v1",
		ContainerName: "test-subzero-worker",
		Scheme:        "unix",
		Endpoint:      "/tmp/subzero-test/worker.sock",
		SecretKey:     []byte("test-secret-32b-key-1234567890"),
		IdleTimeout:   10 * time.Second,
		WorkerPolicy:  policy,
		ExtraArgs:     []string{"--cpus=2", "--memory=512m"},
	})
	if err != nil {
		t.Fatalf("NewContainerSpawner failed: %v", err)
	}

	args := spawner.BuildRunArgs()
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--name test-subzero-worker") {
		t.Errorf("expected container name in args, got: %s", joined)
	}
	if !strings.Contains(joined, "--rm") {
		t.Errorf("expected --rm in args, got: %s", joined)
	}
	if !strings.Contains(joined, "-v /tmp/subzero-test:/tmp/subzero-test") {
		t.Errorf("expected socket volume mount in args, got: %s", joined)
	}
	if !strings.Contains(joined, "--cpus=2") || !strings.Contains(joined, "--memory=512m") {
		t.Errorf("expected extra args in args, got: %s", joined)
	}
	if !strings.Contains(joined, "SUBZERO_WORKER_POLICY") {
		t.Errorf("expected SUBZERO_WORKER_POLICY in args, got: %s", joined)
	}
	if !strings.Contains(joined, "custom-worker:v1") {
		t.Errorf("expected image in args, got: %s", joined)
	}
	if !strings.Contains(joined, "-scheme unix -endpoint /tmp/subzero-test/worker.sock") {
		t.Errorf("expected CLI entrypoint flags in args, got: %s", joined)
	}
}

func TestContainerSpawner_BuildRunArgs_TCP(t *testing.T) {
	spawner, err := supervisor.NewContainerSpawner(supervisor.ContainerSpawnerConfig{
		Engine:    "podman",
		Scheme:    "tcp",
		Endpoint:  "127.0.0.1:9099",
		SecretKey: []byte("secret"),
	})
	if err != nil {
		t.Fatalf("NewContainerSpawner failed: %v", err)
	}

	args := spawner.BuildRunArgs()
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "-p 9099:9099") {
		t.Errorf("expected port mapping in args for TCP, got: %s", joined)
	}
}

func TestContainerSpawner_LifecycleWithMock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subzero-spawner-test-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	sockPath := filepath.Join(tmpDir, "worker.sock")
	proc := &mockCommandProcess{waitCh: make(chan struct{})}
	mockRunner := &mockCommandRunner{customProc: proc}

	spawner, err := supervisor.NewContainerSpawner(supervisor.ContainerSpawnerConfig{
		Engine:        "docker",
		Image:         "subzero-worker:latest",
		ContainerName: "mock-worker-lifecycle",
		Scheme:        "unix",
		Endpoint:      sockPath,
		SecretKey:     []byte("test-secret"),
		ReadyTimeout:  2 * time.Second,
		Runner:        mockRunner,
	})
	if err != nil {
		t.Fatalf("NewContainerSpawner failed: %v", err)
	}

	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected initial state DORMANT, got %v", spawner.State())
	}

	// In background, start a mock unix listener to simulate worker becoming ready
	var ln net.Listener
	var lnMu sync.Mutex
	tr, _ := transport.Get("unix")

	go func() {
		// Small delay before worker socket opens to test cold-start wait
		time.Sleep(50 * time.Millisecond)
		lnMu.Lock()
		ln, _ = tr.Listen(context.Background(), sockPath)
		lnMu.Unlock()
	}()

	defer func() {
		lnMu.Lock()
		if ln != nil {
			_ = ln.Close()
		}
		lnMu.Unlock()
	}()

	// EnsureRunning should transition from DORMANT -> PROVISIONING -> RUNNING
	ctx := context.Background()
	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning failed: %v", err)
	}

	if spawner.State() != supervisor.StateRunning {
		t.Fatalf("expected state RUNNING, got %v", spawner.State())
	}

	// Calling EnsureRunning again when RUNNING should be a no-op
	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("second EnsureRunning failed: %v", err)
	}

	// Simulate worker container exiting (auto-termination watchdog scale-to-zero)
	_ = proc.Kill()
	// Allow background goroutine to update state
	time.Sleep(50 * time.Millisecond)

	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected state DORMANT after container exit, got %v", spawner.State())
	}

	// Test Stop cleanup
	spawner.Stop()
	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected state DORMANT after Stop, got %v", spawner.State())
	}

	// Verify docker rm -f was called during cleanup
	mockRunner.mu.Lock()
	var rmCalled bool
	for _, cmd := range mockRunner.runCmds {
		if len(cmd) >= 4 && cmd[0] == "docker" && cmd[1] == "rm" && cmd[2] == "-f" {
			rmCalled = true
			break
		}
	}
	mockRunner.mu.Unlock()

	if !rmCalled {
		t.Errorf("expected docker rm -f to be invoked during Stop/cleanup")
	}
}

func TestContainerSpawner_ColdStartTimeout(t *testing.T) {
	mockRunner := &mockCommandRunner{}

	spawner, err := supervisor.NewContainerSpawner(supervisor.ContainerSpawnerConfig{
		Engine:        "docker",
		Image:         "subzero-worker:latest",
		ContainerName: "mock-worker-timeout",
		Scheme:        "unix",
		Endpoint:      "/tmp/nonexistent-worker-timeout.sock",
		SecretKey:     []byte("test-secret"),
		ReadyTimeout:  150 * time.Millisecond,
		Runner:        mockRunner,
	})
	if err != nil {
		t.Fatalf("NewContainerSpawner failed: %v", err)
	}

	err = spawner.EnsureRunning(context.Background())
	if !errors.Is(err, supervisor.ErrColdStartTimeout) {
		t.Fatalf("expected ErrColdStartTimeout, got: %v", err)
	}

	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected state DORMANT after timeout, got %v", spawner.State())
	}
}
