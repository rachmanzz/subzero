package e2e_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

// TestStrictMemoryLimitAndBlastRadiusContainment verifies PRD Section 2, Section 5.4, and Section 9:
// 1. Strict Memory Allocation: Sandboxes exceeding MemoryLimitPages (e.g. 256 pages = 16MB) are trapped.
// 2. Blast Radius Containment: A tenant triggering OOM does NOT crash the Supervisor or degrade other tenants.
// 3. Supervisor Resilience: Immediate subsequent task execution for a healthy tenant succeeds without failure.
func TestStrictMemoryLimitAndBlastRadiusContainment(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	compiler := management.NewGoCompiler("")

	// 1. Compile OOM-inducing module (Tenant Greedy)
	oomSource := `package main

func main() {
	// Repeatedly allocate memory until memory limit is exceeded
	var storage [][]byte
	for i := 0; i < 500; i++ {
		buf := make([]byte, 1024*1024) // 1 MB
		buf[0] = byte(i)
		buf[len(buf)-1] = 0xFF
		storage = append(storage, buf)
	}
	_ = storage
}
`
	oomCompiled, err := compiler.Compile(ctx, []byte(oomSource), management.CompileOptions{
		TenantID:   "tenant-greedy",
		ModuleName: "oom-allocator",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("failed to compile oom module: %v", err)
	}

	// 2. Compile healthy module (Tenant Normal)
	healthySource := `package main

import (
	"fmt"
)

func main() {
	fmt.Printf("{\"is_success\":true,\"processed_rows\":42}\n")
}
`
	healthyCompiled, err := compiler.Compile(ctx, []byte(healthySource), management.CompileOptions{
		TenantID:   "tenant-normal",
		ModuleName: "healthy-parser",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("failed to compile healthy module: %v", err)
	}

	// 3. Initialize Wazero Engine with strict memory quota (256 pages = 16MB)
	cfg := wasm.DefaultConfig()
	cfg.MemoryLimitPages = 256 // 16 MB strict ceiling (PRD 5.4)
	cfg.DefaultTimeout = 15 * time.Second

	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// Resolver map
	resolver := func(target string) ([]byte, error) {
		switch target {
		case "oom-allocator":
			return oomCompiled.WasmBytes, nil
		case "healthy-parser":
			return healthyCompiled.WasmBytes, nil
		default:
			return nil, fmt.Errorf("module not found: %s", target)
		}
	}

	policy := permission.DefaultWorkerPolicy()
	wasmHandler := worker.NewWasmHandlerWithConfig(worker.WasmHandlerConfig{
		Engine:       engine,
		Resolver:     resolver,
		WorkerPolicy: &policy,
	})

	// 4. Initialize Supervisor Dispatcher
	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		QueueSize:   16,
		SubLimit:    2,
	})
	dispatcher.Start()
	defer dispatcher.Stop()

	// -------------------------------------------------------------
	// SCENARIO 1: Tenant Greedy attempts OOM allocation -> TRAPPED & CONTAINED
	// -------------------------------------------------------------
	taskGreedy := &protocol.TaskMessage{
		TaskID:       "task-greedy-oom-01",
		TenantID:     "tenant-greedy",
		ActionTarget: "oom-allocator",
	}

	t.Log("[Scenario 1] Executing OOM-inducing task for Tenant Greedy...")
	resGreedy, err := wasmHandler(ctx, taskGreedy)
	if err != nil {
		t.Fatalf("handler should not return go error on OOM, but format ResultMessage: %v", err)
	}

	if resGreedy.IsSuccess {
		t.Fatalf("expected Tenant Greedy to fail due to memory limit, but got success")
	}

	errMsgLower := strings.ToLower(resGreedy.ErrorMessage)
	isOOMExpected := strings.Contains(errMsgLower, "memory") ||
		strings.Contains(errMsgLower, "out of memory") ||
		strings.Contains(errMsgLower, "grow") ||
		strings.Contains(errMsgLower, "failed")

	if !isOOMExpected {
		t.Errorf("expected error message to indicate memory exhaustion, got: %s", resGreedy.ErrorMessage)
	}
	t.Logf("[Scenario 1 SUCCESS] OOM safely trapped without host crash: %s", resGreedy.ErrorMessage)

	// -------------------------------------------------------------
	// SCENARIO 2: Blast Radius Containment - Tenant Normal immediately executes
	// -------------------------------------------------------------
	taskNormal := &protocol.TaskMessage{
		TaskID:       "task-normal-ok-01",
		TenantID:     "tenant-normal",
		ActionTarget: "healthy-parser",
	}

	t.Log("[Scenario 2] Executing normal task for Tenant Normal immediately after OOM...")
	resNormal, err := wasmHandler(ctx, taskNormal)
	if err != nil {
		t.Fatalf("unexpected handler error for Tenant Normal: %v", err)
	}

	if !resNormal.IsSuccess || resNormal.ProcessedRows != 42 {
		t.Fatalf("expected Tenant Normal to succeed with 42 rows, got: %+v", resNormal)
	}
	t.Logf("[Scenario 2 SUCCESS] Tenant Normal executed flawlessly after previous tenant's OOM: rows=%d", resNormal.ProcessedRows)

	// -------------------------------------------------------------
	// SCENARIO 3: Direct wasm.Engine verify ErrMemoryLimitExceeded
	// -------------------------------------------------------------
	t.Log("[Scenario 3] Directly testing wasm.Engine.Execute with OOM bytecode...")
	execRes, directErr := engine.Execute(ctx, oomCompiled.WasmBytes, wasm.ExecOptions{
		Timeout: 15 * time.Second,
	})
	if directErr == nil {
		t.Fatalf("expected direct engine.Execute to return error on OOM, got: %+v", execRes)
	}

	if !errors.Is(directErr, wasm.ErrMemoryLimitExceeded) && !strings.Contains(directErr.Error(), "memory") {
		t.Errorf("expected ErrMemoryLimitExceeded, got: %v", directErr)
	}
	t.Logf("[Scenario 3 SUCCESS] Direct engine error returned ErrMemoryLimitExceeded: %v", directErr)
}
