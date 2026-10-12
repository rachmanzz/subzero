package wasm_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/pkg/management"
)

func compileHelper(t *testing.T, src string) []byte {
	t.Helper()
	compiler := management.NewGoCompiler("")
	res, err := compiler.Compile(context.Background(), []byte(src), management.CompileOptions{
		TenantID:   "tenant-test",
		ModuleName: "mod-test",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("failed to compile wasm: %v", err)
	}
	return res.WasmBytes
}

func TestWasmEngineExecution(t *testing.T) {
	ctx := context.Background()

	sourceCode := `package main

import (
	"fmt"
	"os"
)

func main() {
	tenant := os.Getenv("SUBZERO_TENANT")
	fmt.Printf("Hello from Wasm Sandbox! Tenant: %s\n", tenant)
}
`
	wasmBytes := compileHelper(t, sourceCode)

	engine, err := wasm.NewEngine(ctx, wasm.DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	execRes, err := engine.Execute(ctx, wasmBytes, wasm.ExecOptions{
		Env: map[string]string{
			"SUBZERO_TENANT": "Acme-Logistics",
		},
		FuelLimit: 1_000_000,
		Timeout:   10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Wasm execution failed: %v", err)
	}

	expectedOutput := "Hello from Wasm Sandbox! Tenant: Acme-Logistics"
	if !strings.Contains(execRes.Stdout, expectedOutput) {
		t.Fatalf("expected stdout to contain %q, got: %q", expectedOutput, execRes.Stdout)
	}

	if execRes.Duration <= 0 {
		t.Fatal("expected positive duration")
	}

	if execRes.MemoryPages == 0 || execRes.MemoryBytes == 0 {
		t.Errorf("expected memory pages and bytes tracked, got pages=%d, bytes=%d", execRes.MemoryPages, execRes.MemoryBytes)
	}

	if execRes.OperationsExecuted == 0 {
		t.Errorf("expected metered operations > 0, got %d", execRes.OperationsExecuted)
	}
}

func TestWasmEngine_TimeoutPreemption(t *testing.T) {
	ctx := context.Background()

	// Infinite loop program
	infiniteLoopSource := `package main

func main() {
	for {
		// Busy loop attempting to starve CPU
		compute()
	}
}

//go:noinline
func compute() int {
	return 42
}
`
	wasmBytes := compileHelper(t, infiniteLoopSource)

	engine, err := wasm.NewEngine(ctx, wasm.DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// Strict timeout of 300 milliseconds
	execRes, err := engine.Execute(ctx, wasmBytes, wasm.ExecOptions{
		Timeout: 300 * time.Millisecond,
	})

	if err == nil {
		t.Fatal("expected execution timeout error, got nil")
	}

	if !errors.Is(err, wasm.ErrExecutionTimeout) {
		t.Fatalf("expected ErrExecutionTimeout, got: %v", err)
	}

	if execRes == nil {
		t.Fatal("expected execRes to be populated even on timeout")
	}

	if execRes.ExitCode != 124 {
		t.Errorf("expected exit code 124 (timeout), got %d", execRes.ExitCode)
	}

	// Ensure execution was actually interrupted quickly and did not run unbounded
	if execRes.Duration > 2*time.Second {
		t.Fatalf("timeout took too long to preempt: %v", execRes.Duration)
	}
}

func TestWasmEngine_FuelExhausted(t *testing.T) {
	ctx := context.Background()

	// Program making repeated function calls
	loopSource := `package main

func main() {
	for i := 0; i < 100000; i++ {
		step(i)
	}
}

//go:noinline
func step(n int) int {
	return n * 2
}
`
	wasmBytes := compileHelper(t, loopSource)

	engine, err := wasm.NewEngine(ctx, wasm.DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// Restrict to tiny fuel budget (50 operations)
	execRes, err := engine.Execute(ctx, wasmBytes, wasm.ExecOptions{
		FuelLimit: 50,
		Timeout:   15 * time.Second,
	})

	if err == nil {
		t.Fatal("expected fuel exhausted error, got nil")
	}

	if !errors.Is(err, wasm.ErrFuelExhausted) {
		t.Fatalf("expected ErrFuelExhausted, got: %v", err)
	}

	if execRes == nil {
		t.Fatal("expected execRes to be returned on fuel exhaustion")
	}

	if execRes.ExitCode != 137 {
		t.Errorf("expected exit code 137, got %d", execRes.ExitCode)
	}

	if execRes.OperationsExecuted < 50 {
		t.Errorf("expected operations to reach at least fuel limit, got %d", execRes.OperationsExecuted)
	}
}

func TestWasmEngine_MemoryLimitExceeded(t *testing.T) {
	ctx := context.Background()

	sourceCode := `package main

import "fmt"

func main() {
	fmt.Println("trying to initialize runtime")
}
`
	wasmBytes := compileHelper(t, sourceCode)

	// Restrict to only 2 pages (128 KB), insufficient for Go wasip1 runtime heap initialization
	strictCfg := wasm.Config{
		MemoryLimitPages: 2,
	}

	engine, err := wasm.NewEngine(ctx, strictCfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	_, err = engine.Execute(ctx, wasmBytes, wasm.ExecOptions{
		Timeout: 5 * time.Second,
	})

	if err == nil {
		t.Fatal("expected memory limit exceeded error, got nil")
	}

	if !errors.Is(err, wasm.ErrMemoryLimitExceeded) {
		t.Fatalf("expected ErrMemoryLimitExceeded, got: %v", err)
	}
}
