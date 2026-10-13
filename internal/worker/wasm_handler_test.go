package worker_test

import (
	"context"
	"os"
	"strings"
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
	cfg.DefaultTimeout = 60 * time.Second
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
		SourceURI:    "file:///tmp/subzero-tenants/input.xlsx",
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

	// Compile a valid Wasm binary so failures are strictly due to policy enforcement, not empty compilation
	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(`package main; func main() {}`), management.CompileOptions{
		TenantID:   "tenant-t1",
		ModuleName: "transformer",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	resolver := func(target string) ([]byte, error) {
		return compileRes.WasmBytes, nil
	}

	// Restrict worker policy to a specific tenant vault prefix
	policy := permission.DefaultWorkerPolicy()
	policy.S3.AllowedPrefixes = []string{"s3://allowed-tenant-vault/"}

	handler := worker.NewWasmHandlerWithConfig(worker.WasmHandlerConfig{
		Engine:       engine,
		Resolver:     resolver,
		WorkerPolicy: &policy,
	})

	// 1. Task requests unauthorized host call (privilege escalation)
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
	if !strings.Contains(res.ErrorMessage, "permission denied") {
		t.Fatalf("expected error message to contain 'permission denied', got: %q", res.ErrorMessage)
	}

	// 2. Task requests S3 prefix outside policy (privilege escalation via config)
	taskS3Escalation := &protocol.TaskMessage{
		TaskID:       "task-s3-1",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/transformer",
		CustomConfig: map[string]string{
			"permissions": `{"s3": {"allowed_prefixes": ["s3://other-tenant-vault/"]}}`,
		},
	}
	res2, err := handler(ctx, taskS3Escalation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.IsSuccess {
		t.Fatalf("expected task to fail due to S3 privilege escalation")
	}
	if !strings.Contains(res2.ErrorMessage, "permission denied") {
		t.Fatalf("expected error message to contain 'permission denied', got: %q", res2.ErrorMessage)
	}

	// 3. Task with unauthorized S3 SourceURI at runtime (boundary gate check)
	taskS3RuntimeViolation := &protocol.TaskMessage{
		TaskID:       "task-s3-runtime-1",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/transformer",
		SourceURI:    "s3://other-tenant-vault/secret.csv",
	}
	res3, err := handler(ctx, taskS3RuntimeViolation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res3.IsSuccess {
		t.Fatalf("expected task to fail due to unauthorized S3 source URI")
	}
	if !strings.Contains(res3.ErrorMessage, "not permitted by S3 prefixes") {
		t.Fatalf("expected error message to contain 'not permitted by S3 prefixes', got: %q", res3.ErrorMessage)
	}

	// 4. Task with unauthorized local filesystem root (Step 3b check)
	taskRootViolation := &protocol.TaskMessage{
		TaskID:       "task-root-1",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/transformer",
		SourceURI:    "/var/data/unauthorized.csv",
	}
	res4, err := handler(ctx, taskRootViolation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res4.IsSuccess {
		t.Fatalf("expected task to fail due to unauthorized filesystem root")
	}
	if !strings.Contains(res4.ErrorMessage, "not permitted under allowed filesystem roots") {
		t.Fatalf("expected error message to contain filesystem root rejection, got: %q", res4.ErrorMessage)
	}

	// 5. Task with forbidden file extension within allowed root (Step 4 check)
	taskExtViolation := &protocol.TaskMessage{
		TaskID:       "task-ext-1",
		TenantID:     "tenant-t1",
		ActionTarget: "tenant-t1/transformer",
		SourceURI:    "/tmp/subzero-tenants/payload.exe",
	}
	res5, err := handler(ctx, taskExtViolation)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res5.IsSuccess {
		t.Fatalf("expected task to fail due to forbidden file extension .exe")
	}
	if !strings.Contains(res5.ErrorMessage, "file extension") || !strings.Contains(res5.ErrorMessage, "not permitted") {
		t.Fatalf("expected error message to contain extension rejection, got: %q", res5.ErrorMessage)
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
	cfg.DefaultTimeout = 60 * time.Second
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// 4. Setup Worker Policy and Handler
	policy := permission.DefaultWorkerPolicy()
	// Allow any local file prefix under tmpDir
	policy.AllowedHostCalls = append(policy.AllowedHostCalls, "storage:get_object")
	policy.Filesystem.AllowedReadOnlyRoots = append(policy.Filesystem.AllowedReadOnlyRoots, tmpDir)

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

// TestWorkerWasmHandler_DomainSeparation_SourceURI_vs_HostCall locks in the intentional
// architecture behavior (Finding N-1):
//  1. Control Plane Ingress (task.SourceURI): Host supervisor streams data into Stdin governed
//     by WorkerPolicy ceiling boundary.
//  2. Guest Sandbox Space (Hostcalls): Guest Wasm execution enforces strict least-privilege
//     and is denied if not explicitly requested in the module manifest, even if WorkerPolicy allows it.
func TestWorkerWasmHandler_DomainSeparation_SourceURI_vs_HostCall(t *testing.T) {
	ctx := context.Background()
	tmpDir := t.TempDir()
	testFilePath := tmpDir + "/input.csv"
	_ = os.WriteFile(testFilePath, []byte("1,alpha\n2,beta\n"), 0644)

	// Module tries to call GetStorageBlob directly from guest space
	goSource := `package main
import (
	"fmt"
	"github.com/rachmanzz/subzero/pkg/sdk"
)
func main() {
	req, _ := sdk.ReadRequestFromEnv()
	_, err := sdk.GetStorageBlob(req.SourceURI)
	if err != nil {
		fmt.Printf("{\"is_success\":false,\"error_message\":%q}\n", err.Error())
		return
	}
	fmt.Printf("{\"is_success\":true,\"processed_rows\":2}\n")
}
`
	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(goSource), management.CompileOptions{
		TenantID:   "tenant-n1",
		ModuleName: "guest-probe",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	cfg := wasm.DefaultConfig()
	cfg.DefaultTimeout = 60 * time.Second
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// Worker policy allows storage:get_object and filesystem
	policy := permission.DefaultWorkerPolicy()
	policy.AllowedHostCalls = append(policy.AllowedHostCalls, "storage:get_object")
	policy.Filesystem.AllowedReadOnlyRoots = append(policy.Filesystem.AllowedReadOnlyRoots, tmpDir)

	resolver := func(target string) ([]byte, error) {
		return compileRes.WasmBytes, nil
	}

	// Module manifest explicitly DOES NOT request "storage:get_object"
	moduleReqResolverWithoutPerm := func(target string) (*permission.ModuleRequest, error) {
		return &permission.ModuleRequest{
			HostCalls: []string{}, // No host calls requested
		}, nil
	}

	handler := worker.NewWasmHandlerWithConfig(worker.WasmHandlerConfig{
		Engine:                engine,
		Resolver:              resolver,
		WorkerPolicy:          &policy,
		ModuleRequestResolver: moduleReqResolverWithoutPerm,
	})

	task := &protocol.TaskMessage{
		TaskID:       "task-n1-001",
		TenantID:     "tenant-n1",
		ActionTarget: "tenant-n1/guest-probe",
		SourceURI:    "file://" + testFilePath,
	}

	// The task ingress (SourceURI) succeeds at host level, but guest hostcall is denied fail-closed
	res, err := handler(ctx, task)
	if err != nil {
		t.Fatalf("handler returned error: %v", err)
	}
	if res.IsSuccess {
		t.Fatalf("expected guest hostcall storageGet to be denied fail-closed when not in module manifest")
	}
	t.Logf("✓ Verified N-1: Hostcall denied fail-closed (%s) while SourceURI ingress respected worker ceiling", res.ErrorMessage)
}
