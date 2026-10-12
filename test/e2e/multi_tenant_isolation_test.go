package e2e_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/internal/storage/local"
	"github.com/rachmanzz/subzero/internal/supervisor"
	"github.com/rachmanzz/subzero/internal/worker"
	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

// TestMultiTenant_StorageAndSandboxIsolation verifies that:
// 1. Tenant Alpha and Tenant Bravo have strictly isolated storage policies.
// 2. Tenant Alpha cannot access Tenant Bravo's storage even if requested.
// 3. A panic or failure in Tenant Alpha's sandbox is contained (Blast Radius Containment)
//    without impacting Supervisor or Tenant Bravo's execution.
func TestMultiTenant_StorageAndSandboxIsolation(t *testing.T) {
	ctx := context.Background()

	// 1. Setup isolated directories for Tenant Alpha and Tenant Bravo
	tmpDir := t.TempDir()
	alphaStorageDir := filepath.Join(tmpDir, "storage-alpha")
	bravoStorageDir := filepath.Join(tmpDir, "storage-bravo")
	_ = os.MkdirAll(alphaStorageDir, 0755)
	_ = os.MkdirAll(bravoStorageDir, 0755)

	alphaFile := filepath.Join(alphaStorageDir, "doc.csv")
	bravoFile := filepath.Join(bravoStorageDir, "doc.csv")

	_ = os.WriteFile(alphaFile, []byte("tenant,data\nalpha,100\nalpha,200\n"), 0644)
	_ = os.WriteFile(bravoFile, []byte("tenant,data\nbravo,999\n"), 0644)

	// Register local storage driver for tmpDir
	_ = local.New(tmpDir)

	// 2. Compile guest Wasm modules
	// Parser module: reads sourceURI via SDK GetStorageBlob
	parserSrc := `package main
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
	// Crashing module: panics to verify blast radius containment
	crashingSrc := `package main
func main() {
	panic("fatal unhandled error in tenant-alpha code!")
}
`
	compiler := management.NewGoCompiler("")
	parserCompiled, err := compiler.Compile(ctx, []byte(parserSrc), management.CompileOptions{
		TenantID:   "common",
		ModuleName: "parser",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("failed to compile parser module: %v", err)
	}

	crashingCompiled, err := compiler.Compile(ctx, []byte(crashingSrc), management.CompileOptions{
		TenantID:   "alpha",
		ModuleName: "crashing",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("failed to compile crashing module: %v", err)
	}

	// 3. Initialize Shared Wazero Engine
	cfg := wasm.DefaultConfig()
	cfg.DefaultTimeout = 30 * time.Second
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("failed to init wazero engine: %v", err)
	}
	defer engine.Close(ctx)

	// 4. Configure Isolated Policies:
	// Worker Alpha policy only allows files under alphaStorageDir
	policyAlpha := permission.DefaultWorkerPolicy()
	policyAlpha.AllowedHostCalls = []string{"storage:get_object"}
	policyAlpha.Filesystem.AllowedReadOnlyRoots = []string{alphaStorageDir}
	policyAlpha.Files.AllowedExtensions = []string{".csv"}

	// Worker Bravo policy only allows files under bravoStorageDir
	policyBravo := permission.DefaultWorkerPolicy()
	policyBravo.AllowedHostCalls = []string{"storage:get_object"}
	policyBravo.Filesystem.AllowedReadOnlyRoots = []string{bravoStorageDir}
	policyBravo.Files.AllowedExtensions = []string{".csv"}

	moduleResolver := func(target string) ([]byte, error) {
		switch target {
		case "parser":
			return parserCompiled.WasmBytes, nil
		case "crashing":
			return crashingCompiled.WasmBytes, nil
		default:
			return nil, fmt.Errorf("module not found")
		}
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

	handlerAlpha := worker.NewWasmHandlerWithConfig(worker.WasmHandlerConfig{
		Engine:                engine,
		Resolver:              moduleResolver,
		WorkerPolicy:          &policyAlpha,
		ModuleRequestResolver: moduleReqResolver,
	})

	handlerBravo := worker.NewWasmHandlerWithConfig(worker.WasmHandlerConfig{
		Engine:                engine,
		Resolver:              moduleResolver,
		WorkerPolicy:          &policyBravo,
		ModuleRequestResolver: moduleReqResolver,
	})

	// 5. Initialize Supervisor Dispatcher
	dispatcher := supervisor.NewDispatcher(supervisor.DispatcherConfig{
		Concurrency: 4,
		SubLimit:    4,
	})
	dispatcher.Start()
	defer dispatcher.Stop()

	// SCENARIO 1: Tenant Alpha accesses own data -> SUCCESS
	taskAlphaValid := &protocol.TaskMessage{
		TaskID:       "task-alpha-1",
		TenantID:     "tenant-alpha",
		ActionTarget: "parser",
		SourceURI:    "file://" + alphaFile,
	}
	resAlpha, err := handlerAlpha(ctx, taskAlphaValid)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if !resAlpha.IsSuccess || resAlpha.ProcessedRows != 3 {
		t.Fatalf("expected Tenant Alpha to successfully read 3 rows, got: %+v", resAlpha)
	}

	// SCENARIO 2: Tenant Alpha attempts to access Tenant Bravo's file -> REJECTED
	taskAlphaIllegal := &protocol.TaskMessage{
		TaskID:       "task-alpha-hack",
		TenantID:     "tenant-alpha",
		ActionTarget: "parser",
		SourceURI:    "file://" + bravoFile,
	}
	resAlphaIllegal, err := handlerAlpha(ctx, taskAlphaIllegal)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	// The storage reader or permission checks reject foreign tenant access
	if resAlphaIllegal.IsSuccess {
		t.Fatalf("security violation: Tenant Alpha succeeded in reading Tenant Bravo's data!")
	}

	// SCENARIO 3: Tenant Alpha crashes inside sandbox -> BLAST RADIUS CONTAINED
	taskAlphaCrash := &protocol.TaskMessage{
		TaskID:       "task-alpha-crash",
		TenantID:     "tenant-alpha",
		ActionTarget: "crashing",
		SourceURI:    "file://" + alphaFile,
	}
	resCrash, err := handlerAlpha(ctx, taskAlphaCrash)
	if err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if resCrash.IsSuccess {
		t.Fatal("expected crashing task to report failure")
	}

	// SCENARIO 4: Supervisor & Tenant Bravo continue uninterrupted
	taskBravoValid := &protocol.TaskMessage{
		TaskID:       "task-bravo-1",
		TenantID:     "tenant-bravo",
		ActionTarget: "parser",
		SourceURI:    "file://" + bravoFile,
	}
	resBravo, err := handlerBravo(ctx, taskBravoValid)
	if err != nil {
		t.Fatalf("unexpected handler error for Tenant Bravo: %v", err)
	}
	if !resBravo.IsSuccess || resBravo.ProcessedRows != 2 {
		t.Fatalf("expected Tenant Bravo to execute cleanly after Tenant Alpha crash, got: %+v", resBravo)
	}
}
