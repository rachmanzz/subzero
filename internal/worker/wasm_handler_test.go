package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

func TestWorkerWasmHandlerExecution(t *testing.T) {
	ctx := context.Background()

	// 1. Compile test Wasm
	goSource := `package main
import (
	"fmt"
	"os"
)
func main() {
	fmt.Printf("Task %s completed for %s\n", os.Getenv("SUBZERO_TASK_ID"), os.Getenv("SUBZERO_TENANT_ID"))
}
`
	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(goSource), management.CompileOptions{
		TenantID:   "tenant-t1",
		ModuleName: "transformer",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	// 2. Initialize Wazero Engine
	cfg := wasm.DefaultConfig()
	cfg.DefaultTimeout = 30 * time.Second
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// 3. Create handler with resolver returning the compiled bytes
	resolver := func(target string) ([]byte, error) {
		if target == "tenant-t1/transformer" {
			return compileRes.WasmBytes, nil
		}
		return nil, worker.ErrModuleNotFound
	}

	handler := worker.NewWasmHandler(engine, resolver)

	task := &protocol.TaskMessage{
		TaskID:       "task-uuid-42",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/transformer",
		SourceURI:    "file:///tmp/input.xlsx",
	}

	result, err := handler(ctx, task)
	if err != nil {
		t.Fatalf("handler returned unexpected error: %v", err)
	}

	if !result.IsSuccess || result.TaskID != task.TaskID {
		t.Fatalf("unexpected result: %+v", result)
	}
}
