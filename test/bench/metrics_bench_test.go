package bench_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/pkg/management"
	_ "github.com/rachmanzz/subzero/pkg/transport/ipc"
)

var (
	compiledWasmBytes []byte
	wasmCompileOnce   sync.Once
	workerBinPath     string
	workerBuildOnce   sync.Once
)

func getWorkerBinary(t testing.TB) string {
	workerBuildOnce.Do(func() {
		// Check if bin/subzero-worker already exists
		relBin := filepath.Join("..", "..", "bin", "subzero-worker")
		if _, err := os.Stat(relBin); err == nil {
			workerBinPath = relBin
			return
		}
		// Build dynamically into temp directory
		tmpDir, err := os.MkdirTemp("", "subzero-bench-worker-*")
		if err != nil {
			t.Fatalf("MkdirTemp failed: %v", err)
		}
		binPath := filepath.Join(tmpDir, "subzero-worker")
		cmd := exec.Command("go", "build", "-o", binPath, "../../cmd/subzero-worker")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("failed to build subzero-worker: %v, output: %s", err, string(out))
		}
		workerBinPath = binPath
	})
	return workerBinPath
}

func getPrecompiledWasm(t testing.TB) []byte {
	wasmCompileOnce.Do(func() {
		src := `package main
func main() {}
`
		compiler := management.NewGoCompiler("")
		res, err := compiler.Compile(context.Background(), []byte(src), management.CompileOptions{
			TenantID:   "bench-tenant",
			ModuleName: "bench-module",
			TargetOS:   "wasip1",
			Optimize:   true,
		})
		if err != nil {
			t.Fatalf("failed to compile bench wasm: %v", err)
		}
		compiledWasmBytes = res.WasmBytes
	})
	return compiledWasmBytes
}

// TestTargetMetric_LocalProcessColdStart verifies PRD Section 2:
// Cold start latency for local OS processes must be sub-100ms.
func TestTargetMetric_LocalProcessColdStart(t *testing.T) {
	workerBin := getWorkerBinary(t)
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "worker.sock")
	secretKey := []byte("bench-secret-32b-key-0123456789")

	spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: workerBin,
		Scheme:       "unix",
		Endpoint:     sockPath,
		SecretKey:    secretKey,
		IdleTimeout:  1 * time.Second,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProcessSpawner failed: %v", err)
	}
	defer spawner.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning cold-start failed: %v", err)
	}
	latency := time.Since(start)

	t.Logf("[Target Metric PRD 2] Local Process Cold-Start Latency: %v (Target: < 100ms)", latency)
	if latency >= 100*time.Millisecond {
		t.Errorf("Cold-start latency (%v) exceeded PRD target threshold of 100ms", latency)
	}
}

// TestTargetMetric_WasmSandboxColdStart verifies PRD Section 2:
// Cold start latency for isolated sandbox Wasm must be sub-second (< 1s).
func TestTargetMetric_WasmSandboxColdStart(t *testing.T) {
	wasmBytes := getPrecompiledWasm(t)
	ctx := context.Background()

	engine, err := wasm.NewEngine(ctx, wasm.DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	execTimeout := 10 * time.Second
	if RaceDetectorEnabled {
		execTimeout = 30 * time.Second
	}

	start := time.Now()
	_, err = engine.Execute(ctx, wasmBytes, wasm.ExecOptions{
		Timeout: execTimeout,
	})
	if err != nil {
		t.Fatalf("Wasm Execute failed: %v", err)
	}
	latency := time.Since(start)

	if RaceDetectorEnabled {
		t.Logf("[Target Metric PRD 2] Wasm Sandbox Cold-Start Latency under -race: %v (TSan instrumentation overhead active)", latency)
	} else {
		t.Logf("[Target Metric PRD 2] Wasm Sandbox Cold-Start Latency: %v (Target: < 1s)", latency)
		if latency >= 1*time.Second {
			t.Errorf("Wasm cold-start latency (%v) exceeded PRD target threshold of 1s", latency)
		}
	}
}

// TestTargetMetric_ZeroIdleCostReclamation verifies PRD Section 2 & Section 8:
// Consumers must automatically release system resources upon idle watchdog expiry.
func TestTargetMetric_ZeroIdleCostReclamation(t *testing.T) {
	workerBin := getWorkerBinary(t)
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "worker.sock")
	secretKey := []byte("bench-secret-32b-key-0123456789")

	idleTimeout := 200 * time.Millisecond
	spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
		WorkerBinary: workerBin,
		Scheme:       "unix",
		Endpoint:     sockPath,
		SecretKey:    secretKey,
		IdleTimeout:  idleTimeout,
		ReadyTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProcessSpawner failed: %v", err)
	}
	defer spawner.Stop()

	// Initial State: DORMANT
	if spawner.State() != supervisor.StateDormant {
		t.Fatalf("expected initial state DORMANT, got %v", spawner.State())
	}

	// Trigger Cold-Start
	ctx := context.Background()
	if err := spawner.EnsureRunning(ctx); err != nil {
		t.Fatalf("EnsureRunning failed: %v", err)
	}
	if spawner.State() != supervisor.StateRunning {
		t.Fatalf("expected state RUNNING, got %v", spawner.State())
	}

	// Wait for idle watchdog window to expire + buffer
	deadline := time.Now().Add(idleTimeout + 500*time.Millisecond)
	scaledToZero := false
	for time.Now().Before(deadline) {
		if spawner.State() == supervisor.StateDormant {
			scaledToZero = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !scaledToZero {
		t.Errorf("Scale-to-zero failed: worker remained in state %v after idle window", spawner.State())
	} else {
		t.Logf("[Target Metric PRD 2 & 8] Worker successfully reclaimed resources and returned to DORMANT")
	}
}

// TestTargetMetric_ConcurrencyThrottling verifies PRD Section 5.1 & Section 9:
// Supervisor controls concurrency using dispatcher pool with throttling semaphore.
func TestTargetMetric_ConcurrencyThrottling(t *testing.T) {
	concurrencyLimit := 8
	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: concurrencyLimit,
		QueueSize:   128,
		SubLimit:    4,
	})
	dispatcher.Start()
	defer dispatcher.Stop()

	var activeWorkers int32
	var maxObservedActive int32
	var completedTasks int32
	totalTasks := 100

	var wg sync.WaitGroup
	wg.Add(totalTasks)

	ctx := context.Background()
	start := time.Now()

	for i := 0; i < totalTasks; i++ {
		taskID := fmt.Sprintf("task-%d", i)
		err := dispatcher.Dispatch(ctx, &benchmarkTask{
			id: taskID,
			fn: func() error {
				curr := atomic.AddInt32(&activeWorkers, 1)
				// Track max concurrent workers
				for {
					max := atomic.LoadInt32(&maxObservedActive)
					if curr <= max || atomic.CompareAndSwapInt32(&maxObservedActive, max, curr) {
						break
					}
				}

				// Simulate lightweight work
				time.Sleep(2 * time.Millisecond)

				atomic.AddInt32(&activeWorkers, -1)
				atomic.AddInt32(&completedTasks, 1)
				wg.Done()
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Dispatch failed: %v", err)
		}
	}

	wg.Wait()
	duration := time.Since(start)

	t.Logf("[NFR PRD 9] Processed %d tasks in %v (Max concurrent workers: %d / limit: %d)",
		completedTasks, duration, maxObservedActive, concurrencyLimit)

	if maxObservedActive > int32(concurrencyLimit) {
		t.Errorf("Concurrency limit exceeded: max observed %d, limit %d", maxObservedActive, concurrencyLimit)
	}
}

// BenchmarkLocalProcessColdStart measures continuous cold-start cycles.
func BenchmarkLocalProcessColdStart(b *testing.B) {
	workerBin := getWorkerBinary(b)
	tmpDir := b.TempDir()
	secretKey := []byte("bench-secret-32b-key-0123456789")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sockPath := filepath.Join(tmpDir, fmt.Sprintf("worker-%d.sock", i))
		spawner, err := supervisor.NewProcessSpawner(supervisor.SpawnerConfig{
			WorkerBinary: workerBin,
			Scheme:       "unix",
			Endpoint:     sockPath,
			SecretKey:    secretKey,
			IdleTimeout:  100 * time.Millisecond,
			ReadyTimeout: 5 * time.Second,
		})
		if err != nil {
			b.Fatalf("NewProcessSpawner failed: %v", err)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := spawner.EnsureRunning(ctx); err != nil {
			cancel()
			spawner.Stop()
			b.Fatalf("EnsureRunning failed: %v", err)
		}
		cancel()
		spawner.Stop()
	}
}

// BenchmarkWasmSandboxColdStart measures execution sandboxing cold-starts.
func BenchmarkWasmSandboxColdStart(b *testing.B) {
	wasmBytes := getPrecompiledWasm(b)
	ctx := context.Background()

	engine, err := wasm.NewEngine(ctx, wasm.DefaultConfig())
	if err != nil {
		b.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, wasmBytes, wasm.ExecOptions{
			Timeout: 2 * time.Second,
		})
		if err != nil {
			b.Fatalf("Wasm Execute failed: %v", err)
		}
	}
}

type benchmarkTask struct {
	id string
	fn func() error
}

func (t *benchmarkTask) Name() string { return t.id }
func (t *benchmarkTask) Execute(ctx context.Context, pool supervisor.SubWorkerPool) error {
	return t.fn()
}
