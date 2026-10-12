package e2e_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/producer"
	"github.com/rachmanzz/subzero/pkg/protocol"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
	_ "github.com/rachmanzz/subzero/pkg/transport/tcp"
)

// mockContainerProcess wraps the running worker process to simulate container execution.
type mockContainerProcess struct {
	cmd *exec.Cmd
}

func (p *mockContainerProcess) Wait() error {
	if p.cmd == nil {
		return nil
	}
	return p.cmd.Wait()
}

func (p *mockContainerProcess) Kill() error {
	if p.cmd != nil && p.cmd.Process != nil {
		return p.cmd.Process.Kill()
	}
	return nil
}

// mockContainerRunner captures docker/podman CLI invocations and runs the native worker binary.
type mockContainerRunner struct {
	t            testing.TB
	workerBin    string
	mu           sync.Mutex
	capturedRuns [][]string
}

func newMockContainerRunner(t testing.TB, workerBin string) *mockContainerRunner {
	return &mockContainerRunner{
		t:         t,
		workerBin: workerBin,
	}
}

func (r *mockContainerRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	r.mu.Lock()
	r.capturedRuns = append(r.capturedRuns, append([]string{name}, args...))
	r.mu.Unlock()
	return []byte("mock container run output"), nil
}

func (r *mockContainerRunner) Start(ctx context.Context, name string, args ...string) (supervisor.CommandProcess, error) {
	r.mu.Lock()
	r.capturedRuns = append(r.capturedRuns, append([]string{name}, args...))
	r.mu.Unlock()

	// Locate image in args and extract worker entrypoint flags following it
	var entrypointArgs []string
	var envVars []string

	for i := 0; i < len(args); i++ {
		if args[i] == "-e" && i+1 < len(args) {
			envVars = append(envVars, args[i+1])
			i++
		}
		if args[i] == "subzero-worker:latest" {
			// All remaining arguments are the container entrypoint arguments
			entrypointArgs = args[i+1:]
			break
		}
	}

	cmd := exec.CommandContext(ctx, r.workerBin, entrypointArgs...)
	cmd.Env = append(os.Environ(), envVars...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("mock container start failed: %w", err)
	}

	return &mockContainerProcess{cmd: cmd}, nil
}

func (r *mockContainerRunner) getLastRun() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.capturedRuns) == 0 {
		return nil
	}
	return r.capturedRuns[len(r.capturedRuns)-1]
}

// TestContainerSpawner_EndToEndLifecycle verifies on-demand worker container lifecycle (PRD 5.2 & 8):
// 1. Cold-start provisioning (DORMANT -> PROVISIONING -> RUNNING)
// 2. Volume mount generation & policy injection in container arguments
// 3. Task submission and result completion over Unix Domain Socket
// 4. Scale-to-zero auto-termination upon inactivity
// 5. Subsequent cold-start re-spawning
// 6. Explicit Stop cleanup
func TestContainerSpawner_EndToEndLifecycle(t *testing.T) {
	workerBin := getE2EWorkerBinary(t)

	tmpDir, err := os.MkdirTemp("", "subzero-container-e2e-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	socketPath := filepath.Join(tmpDir, "worker.sock")
	secretKey := []byte("subzero-container-secret-key-32b!")
	idleTimeout := 600 * time.Millisecond
	readyTimeout := 3 * time.Second

	policy := &permission.WorkerPolicy{
		AllowedHostCalls: []string{"storage:get_object", "logger:debug"},
		Filesystem: permission.PolicyFilesystem{
			AllowedReadOnlyRoots:  []string{tmpDir},
			AllowedReadWriteRoots: []string{tmpDir},
		},
	}

	runner := newMockContainerRunner(t, workerBin)

	spawner, err := supervisor.NewContainerSpawner(supervisor.ContainerSpawnerConfig{
		Engine:        "docker",
		Image:         "subzero-worker:latest",
		ContainerName: "test-subzero-worker-01",
		Scheme:        "unix",
		Endpoint:      socketPath,
		SecretKey:     secretKey,
		IdleTimeout:   idleTimeout,
		ReadyTimeout:  readyTimeout,
		WorkerPolicy:  policy,
		ExtraArgs:     []string{"--cpus=2.0", "--memory=512m"},
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("NewContainerSpawner failed: %v", err)
	}

	// 1. Initial State: DORMANT
	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected initial state Dormant, got %v", spawner.State())
	}

	// 2. EnsureRunning triggers cold-start provision
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning cold-start failed: %v", err)
	}
	coldStartDuration := time.Since(start)
	t.Logf("✓ Worker container cold-start ready in %v", coldStartDuration)

	if spawner.State() != supervisor.StateRunning {
		t.Fatalf("expected state Running, got %v", spawner.State())
	}

	// 3. Verify Generated Container Arguments & Volume Mounts
	capturedArgs := runner.getLastRun()
	if len(capturedArgs) == 0 {
		t.Fatal("expected captured container run arguments, got none")
	}

	argsJoined := strings.Join(capturedArgs, " ")
	expectedFragments := []string{
		"docker run --rm --name test-subzero-worker-01",
		"SUBZERO_SCHEME=unix",
		fmt.Sprintf("SUBZERO_ENDPOINT=%s", socketPath),
		"SUBZERO_IDLE_TIMEOUT=600ms",
		"--cpus=2.0 --memory=512m",
		fmt.Sprintf("-v %s:%s", filepath.Dir(socketPath), filepath.Dir(socketPath)),
		"subzero-worker:latest",
		"-scheme unix",
		fmt.Sprintf("-endpoint %s", socketPath),
	}
	for _, frag := range expectedFragments {
		if !strings.Contains(argsJoined, frag) {
			t.Errorf("expected argument fragment %q in container CLI command: %s", frag, argsJoined)
		}
	}

	// Verify WorkerPolicy was serialized and injected
	policyBytes, _ := json.Marshal(policy)
	if !strings.Contains(argsJoined, string(policyBytes)) {
		t.Errorf("expected serialized worker policy %q in container env arguments", string(policyBytes))
	}

	// 4. Submit Task to Containerized Worker
	client, err := producer.New(producer.Config{
		Scheme:    "unix",
		Endpoint:  socketPath,
		SecretKey: secretKey,
		Timeout:   3 * time.Second,
	})
	if err != nil {
		t.Fatalf("producer.New failed: %v", err)
	}

	task := &protocol.TaskMessage{
		TaskID:       "task-container-001",
		TenantID:     "tenant-alpha",
		ActionTarget: "ping-test",
		SourceURI:    "file:///tmp/sample.csv",
	}

	res, err := client.SubmitTask(context.Background(), task)
	if err != nil {
		t.Fatalf("SubmitTask to container worker failed: %v", err)
	}
	if !res.IsSuccess || res.TaskID != "task-container-001" {
		t.Fatalf("unexpected task result: %+v", res)
	}
	t.Logf("✓ Task executed successfully by container worker in %s", res.ExecutionTime)

	// 5. Test Scale-to-Zero Auto-Termination
	t.Logf("Waiting for idle timeout (%v) to trigger scale-to-zero...", idleTimeout)
	deadline := time.Now().Add(idleTimeout + 2*time.Second)
	var scaledToZero bool
	for time.Now().Before(deadline) {
		if spawner.State() == supervisor.StateDormant {
			scaledToZero = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !scaledToZero {
		t.Fatalf("expected container worker to scale-to-zero to Dormant state within %v, current state: %v", idleTimeout+2*time.Second, spawner.State())
	}
	t.Log("✓ Container worker scaled-to-zero (Dormant)")

	// 6. Test Subsequent Cold-Start Re-spawning
	reSpawnCtx, reSpawnCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer reSpawnCancel()

	if err := spawner.EnsureRunning(reSpawnCtx); err != nil {
		t.Fatalf("second EnsureRunning failed: %v", err)
	}
	if spawner.State() != supervisor.StateRunning {
		t.Fatalf("expected state Running after re-spawn, got %v", spawner.State())
	}
	t.Log("✓ Container worker re-spawned successfully")

	// Submit second task to re-spawned worker
	task2 := &protocol.TaskMessage{
		TaskID:       "task-container-002",
		TenantID:     "tenant-alpha",
		ActionTarget: "ping-test",
	}
	res2, err := client.SubmitTask(context.Background(), task2)
	if err != nil {
		t.Fatalf("SubmitTask to re-spawned worker failed: %v", err)
	}
	if !res2.IsSuccess {
		t.Fatalf("expected success on second task, got %+v", res2)
	}

	// 7. Explicit Stop Cleanup
	spawner.Stop()
	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected state Dormant after Stop, got %v", spawner.State())
	}

	// EnsureRunning should now return ErrSpawnerStopped
	if err := spawner.EnsureRunning(context.Background()); err != supervisor.ErrSpawnerStopped {
		t.Fatalf("expected ErrSpawnerStopped after Stop, got: %v", err)
	}
	t.Log("✓ Container spawner cleanly stopped and sealed")
}

// TestContainerSpawner_TCP_PortMapping verifies TCP container arguments and port publishing.
func TestContainerSpawner_TCP_PortMapping(t *testing.T) {
	workerBin := getE2EWorkerBinary(t)

	// Pick a free TCP port
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpAddr := l.Addr().String()
	_ = l.Close()

	_, port, _ := net.SplitHostPort(tcpAddr)
	runner := newMockContainerRunner(t, workerBin)
	secretKey := []byte("subzero-tcp-secret-32b-key-default!")

	spawner, err := supervisor.NewContainerSpawner(supervisor.ContainerSpawnerConfig{
		Engine:        "docker",
		Image:         "subzero-worker:latest",
		ContainerName: "tcp-subzero-worker",
		Scheme:        "tcp",
		Endpoint:      tcpAddr,
		SecretKey:     secretKey,
		IdleTimeout:   10 * time.Second,
		ReadyTimeout:  3 * time.Second,
		Runner:        runner,
	})
	if err != nil {
		t.Fatalf("NewContainerSpawner TCP failed: %v", err)
	}
	defer spawner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning TCP failed: %v", err)
	}

	// Verify port argument (-p port:port)
	runArgs := runner.getLastRun()
	argsJoined := strings.Join(runArgs, " ")
	expectedPortFlag := fmt.Sprintf("-p %s:%s", port, port)
	if !strings.Contains(argsJoined, expectedPortFlag) {
		t.Fatalf("expected TCP port mapping flag %q in container CLI args: %s", expectedPortFlag, argsJoined)
	}

	// Submit task over TCP
	client, err := producer.New(producer.Config{
		Scheme:    "tcp",
		Endpoint:  tcpAddr,
		SecretKey: secretKey,
		Timeout:   3 * time.Second,
	})
	if err != nil {
		t.Fatalf("producer.New TCP failed: %v", err)
	}

	task := &protocol.TaskMessage{
		TaskID:       "tcp-task-001",
		TenantID:     "tenant-tcp",
		ActionTarget: "ping-test",
	}

	res, err := client.SubmitTask(context.Background(), task)
	if err != nil {
		t.Fatalf("SubmitTask TCP failed: %v", err)
	}
	if !res.IsSuccess {
		t.Fatalf("task failed on TCP container worker: %+v", res)
	}
	t.Log("✓ TCP container worker executed task and published port successfully")
}

// TestContainerSpawner_RealDocker_IfAvailable tests actual Docker execution via OSCommandRunner if Docker is running.
func TestContainerSpawner_RealDocker_IfAvailable(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not found on host, skipping live docker test")
	}

	checkCtx, checkCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer checkCancel()
	if err := exec.CommandContext(checkCtx, "docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running or accessible, skipping live docker test")
	}

	// Also verify that the target image exists, avoiding CI failures when docker daemon is up but image is unbuilt
	imgCtx, imgCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer imgCancel()
	if err := exec.CommandContext(imgCtx, "docker", "image", "inspect", "subzero-worker:latest").Run(); err != nil {
		t.Skip("docker image subzero-worker:latest not built or present on host, skipping live docker test")
	}

	// If docker is available and image is present, run a containerized worker test using default OSCommandRunner
	secretKey := []byte("docker-live-test-secret-key-32b!")
	spawner, err := supervisor.NewContainerSpawner(supervisor.ContainerSpawnerConfig{
		Engine:        "docker",
		Image:         "subzero-worker:latest",
		ContainerName: "subzero-live-worker-test",
		Scheme:        "tcp",
		Endpoint:      "127.0.0.1:9095",
		SecretKey:     secretKey,
		IdleTimeout:   5 * time.Second,
		ReadyTimeout:  5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewContainerSpawner failed: %v", err)
	}
	defer spawner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning with live docker failed: %v", err)
	}
	t.Log("✓ Live docker worker spawned and ready")
}

