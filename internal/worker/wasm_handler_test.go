package worker_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

func TestWorkerWasmHandlerExecution(t *testing.T) {
	ctx := context.Background()

	// 1. Compile test Wasm
	goSource := `package main
import (
	"fmt"
)
func main() {
	fmt.Println("{\"is_success\":true,\"processed_rows\":250}")
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

	if !result.IsSuccess || result.TaskID != task.TaskID || result.ProcessedRows != 250 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestWorkerWasmHandler_PermissionPolicyEnforcement(t *testing.T) {
	ctx := context.Background()

	cfg := wasm.DefaultConfig()
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	resolver := func(target string) ([]byte, error) {
		return []byte{}, nil
	}

	handler := worker.NewWasmHandler(engine, resolver)

	// 1. Task requests unauthorized host call
	taskEscalation := &protocol.TaskMessage{
		TaskID:       "task-esc-1",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/transformer",
		CustomConfig: map[string]string{
			"permissions": `{"host_calls": ["forbidden:kernel_trap"]}`,
		},
	}
	res, err := handler(ctx, taskEscalation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsSuccess {
		t.Fatalf("expected task to fail due to unauthorized host call")
	}

	// 2. Task requests S3 prefix outside policy
	taskS3Violation := &protocol.TaskMessage{
		TaskID:       "task-s3-1",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/transformer",
		SourceURI:    "s3://other-tenant-vault/secret.csv",
		CustomConfig: map[string]string{
			"permissions": `{"s3": {"allowed_prefixes": ["s3://other-tenant-vault/"]}}`,
		},
	}
	res2, err := handler(ctx, taskS3Violation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.IsSuccess {
		t.Fatalf("expected task to fail due to S3 prefix violation")
	}

	// 3. Task with forbidden file extension
	taskExtViolation := &protocol.TaskMessage{
		TaskID:       "task-ext-1",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/transformer",
		SourceURI:    "/tmp/payload.exe",
	}
	res3, err := handler(ctx, taskExtViolation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res3.IsSuccess {
		t.Fatalf("expected task to fail due to forbidden file extension .exe")
	}
}

func TestWorkerWasmHandler_DirectStorageStreaming(t *testing.T) {
	ctx := context.Background()

	// 1. Prepare test file content in local storage
	tmpDir := t.TempDir()
	testFilePath := tmpDir + "/input.csv"
	csvContent := []byte("id,name,role\n1,alice,admin\n2,bob,operator\n3,charlie,viewer\n")
	if err := os.WriteFile(testFilePath, csvContent, 0644); err != nil {
		t.Fatalf("write test file failed: %v", err)
	}

	// 2. Compile guest module that calls sdk.GetStorageBlob
	goSource := `package main
import (
	"bytes"
	"fmt"
	"github.com/rachmanzz/subzero/pkg/sdk"
)
func main() {
	req, err := sdk.ReadRequestFromEnv()
	if err != nil {
		fmt.Printf("{\"is_success\":false,\"error_message\":%q}\n", err.Error())
		return
	}
	blob, err := sdk.GetStorageBlob(req.SourceURI)
	if err != nil {
		fmt.Printf("{\"is_success\":false,\"error_message\":%q}\n", err.Error())
		return
	}
	lines := bytes.Count(blob, []byte("\n"))
	fmt.Printf("{\"is_success\":true,\"processed_rows\":%d}\n", lines)
}
`
	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(goSource), management.CompileOptions{
		TenantID:   "tenant-t1",
		ModuleName: "stream-parser",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	// 3. Initialize Engine
	cfg := wasm.DefaultConfig()
	cfg.DefaultTimeout = 30 * time.Second
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// 4. Setup Worker Policy and Handler
	policy := permission.DefaultWorkerPolicy()
	// Allow any local file prefix
	policy.AllowedHostCalls = append(policy.AllowedHostCalls, "storage:get_object")

	resolver := func(target string) ([]byte, error) {
		if target == "tenant-t1/stream-parser" {
			return compileRes.WasmBytes, nil
		}
		return nil, worker.ErrModuleNotFound
	}

	moduleReqResolver := func(target string) (*permission.ModuleRequest, error) {
		return &permission.ModuleRequest{
			HostCalls: []string{"storage:get_object"},
			Files: permission.RequestFiles{
				AllowedExtensions: []string{".csv"},
				MaxFileSizeMB:     10,
			},
		}, nil
	}

	handler := worker.NewWasmHandlerWithConfig(worker.WasmHandlerConfig{
		Engine:                engine,
		Resolver:              resolver,
		WorkerPolicy:          &policy,
		ModuleRequestResolver: moduleReqResolver,
	})

	task := &protocol.TaskMessage{
		TaskID:       "task-stream-001",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/stream-parser",
		SourceURI:    "file://" + testFilePath,
	}

	result, err := handler(ctx, task)
	if err != nil {
		t.Fatalf("handler execution error: %v", err)
	}

	if !result.IsSuccess {
		t.Fatalf("expected successful execution, got error: %s", result.ErrorMessage)
	}
	if result.ProcessedRows != 4 {
		t.Fatalf("expected 4 rows parsed from stream, got: %d", result.ProcessedRows)
	}
}


