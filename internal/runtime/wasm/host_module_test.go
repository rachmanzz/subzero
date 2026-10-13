package wasm_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/tetratelabs/wazero"
)

func TestHostModule_Execution(t *testing.T) {
	ctx := context.Background()

	// Wasm source that imports subzero_host and calls host_log
	goSource := `package main

import "unsafe"

//go:wasmimport subzero_host host_log
func hostLog(level uint32, msgPtr, msgLen uint32) uint32

//go:wasmimport subzero_host host_metrics_record
func hostMetricsRecord(namePtr, nameLen uint32, val float64) uint32

func main() {
	callLog("hello from wasm guest hostcall")
	callMetric("custom_metric", 42.5)
}

//go:noinline
func callLog(s string) {
	b := []byte(s)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	hostLog(1, ptr, uint32(len(b)))
}

//go:noinline
func callMetric(name string, v float64) {
	b := []byte(name)
	ptr := uint32(uintptr(unsafe.Pointer(&b[0])))
	hostMetricsRecord(ptr, uint32(len(b)), v)
}
`

	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(goSource), management.CompileOptions{
		TenantID:   "tenant-hostcall",
		ModuleName: "hostcall-mod",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	cfg := wasm.DefaultConfig()
	cfg.DefaultTimeout = 30 * time.Second
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	var loggedMessages []string
	var recordedMetrics = make(map[string]float64)

	perms := &permission.EffectivePermissions{
		HostCalls: map[string]bool{
			"logger:info":    true,
			"metrics:record": true,
		},
	}

	hostCtx := &wasm.HostContext{
		TenantID:    "tenant-alpha",
		Permissions: perms,
		LogHandler: func(level int, msg string) {
			loggedMessages = append(loggedMessages, msg)
		},
		MetricHandler: func(name string, value float64) {
			recordedMetrics[name] = value
		},
	}

	execCtx := wasm.WithHostContext(ctx, hostCtx)
	execRes, err := engine.Execute(execCtx, compileRes.WasmBytes, wasm.ExecOptions{
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("Execute failed: %v (stderr: %s)", err, execRes.Stderr)
	}

	if len(loggedMessages) == 0 {
		t.Fatal("expected log message to be received by host LogHandler, got 0")
	}
	if !strings.Contains(loggedMessages[0], "hello from wasm guest hostcall") {
		t.Errorf("unexpected logged message: %v", loggedMessages[0])
	}

	// Verify metric is recorded with proper tenant namespace
	expectedMetricName := "tenant.tenant-alpha.custom_metric"
	if val, ok := recordedMetrics[expectedMetricName]; !ok || val != 42.5 {
		t.Errorf("expected namespaced metric %q = 42.5, got map: %+v", expectedMetricName, recordedMetrics)
	}
}

func TestHostModule_FailClosedWithoutPermissions(t *testing.T) {
	ctx := context.Background()

	// Wasm program checking hostcall return codes
	goSource := `package main

import (
	"os"
	"unsafe"
)

//go:wasmimport subzero_host host_log
func hostLog(level uint32, msgPtr, msgLen uint32) uint32

//go:wasmimport subzero_host host_storage_get
func hostStorageGet(keyPtr, keyLen, outPtr, outMaxLen, outReadPtr uint32) uint32

//go:wasmimport subzero_host host_storage_put
func hostStoragePut(keyPtr, keyLen, valPtr, valLen uint32) uint32

//go:wasmimport subzero_host host_metrics_record
func hostMetricsRecord(namePtr, nameLen uint32, val float64) uint32

func main() {
	msg := []byte("unauthorized log")
	ptr := uint32(uintptr(unsafe.Pointer(&msg[0])))
	if code := hostLog(1, ptr, uint32(len(msg))); code != 1 {
		os.Exit(101) // Expected permission denied code 1
	}

	key := []byte("secret-key")
	kptr := uint32(uintptr(unsafe.Pointer(&key[0])))
	var outBuf [64]byte
	optr := uint32(uintptr(unsafe.Pointer(&outBuf[0])))
	if code := hostStorageGet(kptr, uint32(len(key)), optr, 64, 0); code != 1 {
		os.Exit(102) // Expected permission denied code 1
	}

	val := []byte("val")
	vptr := uint32(uintptr(unsafe.Pointer(&val[0])))
	if code := hostStoragePut(kptr, uint32(len(key)), vptr, uint32(len(val))); code != 1 {
		os.Exit(103) // Expected permission denied code 1
	}

	mName := []byte("unauthorized_metric")
	mptr := uint32(uintptr(unsafe.Pointer(&mName[0])))
	if code := hostMetricsRecord(mptr, uint32(len(mName)), 1.0); code != 1 {
		os.Exit(104) // Expected permission denied code 1
	}
}
`

	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(goSource), management.CompileOptions{
		TenantID:   "tenant-failclose",
		ModuleName: "failclose-mod",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	cfg := wasm.DefaultConfig()
	cfg.DefaultTimeout = 30 * time.Second
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// Test 1: Empty permissions (nil HostContext) -> must all return 1 (denied)
	execRes, err := engine.Execute(ctx, compileRes.WasmBytes, wasm.ExecOptions{
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("execution failed when expecting guest handled denial: %v (exit: %d)", err, execRes.ExitCode)
	}
	if execRes.ExitCode != 0 {
		t.Fatalf("expected guest to successfully verify all denials (exit code 0), got: %d", execRes.ExitCode)
	}

	// Test 2: HostContext present but empty HostCalls map -> all denied
	hostCtx := &wasm.HostContext{
		Permissions: &permission.EffectivePermissions{
			HostCalls: make(map[string]bool),
		},
	}
	execCtx := wasm.WithHostContext(ctx, hostCtx)
	execRes2, err := engine.Execute(execCtx, compileRes.WasmBytes, wasm.ExecOptions{
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("execution 2 failed: %v", err)
	}
	if execRes2.ExitCode != 0 {
		t.Fatalf("expected guest to verify empty map denials (exit code 0), got: %d", execRes2.ExitCode)
	}
}

func TestHostModule_DynamicExportEnumeration_FailClosed(t *testing.T) {
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)

	if err := wasm.RegisterHostModule(ctx, rt); err != nil {
		t.Fatalf("RegisterHostModule failed: %v", err)
	}

	hostMod := rt.Module("subzero_host")
	if hostMod == nil {
		t.Fatalf("module subzero_host not found in runtime")
	}

	exportedDefs := hostMod.ExportedFunctionDefinitions()
	if len(exportedDefs) == 0 {
		t.Fatalf("expected host module to export functions, found none")
	}

	// Known verified host functions that are tested for fail-closed permission enforcement
	verifiedExports := map[string]bool{
		"host_storage_get":    true,
		"host_storage_put":    true,
		"host_log":            true,
		"host_metrics_record": true,
	}

	// 1. Ensure every currently exported function in subzero_host is in verifiedExports
	for name := range exportedDefs {
		if !verifiedExports[name] {
			t.Errorf("SECURITY REGRESSION: unverified hostcall export %q found in subzero_host! All host calls must be tested for fail-closed permission enforcement.", name)
		}
	}

	// 2. Ensure all expected exports are present in subzero_host
	for expected := range verifiedExports {
		if _, ok := exportedDefs[expected]; !ok {
			t.Errorf("expected hostcall export %q was not found in subzero_host", expected)
		}
	}
}

func TestHostModule_MetricNamespaceValidation(t *testing.T) {
	ctx := context.Background()

	// Wasm program that records metrics
	goSource := `package main

import (
	"os"
	"unsafe"
)

//go:wasmimport subzero_host host_metrics_record
func hostMetricsRecord(namePtr, nameLen uint32, val float64) uint32

func main() {
	metricName := []byte("job_latency_ms")
	ptr := uint32(uintptr(unsafe.Pointer(&metricName[0])))
	if code := hostMetricsRecord(ptr, uint32(len(metricName)), 123.45); code != 0 {
		os.Exit(int(code))
	}
}
`
	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(goSource), management.CompileOptions{
		TenantID:   "tenant-metric-test",
		ModuleName: "metric-test-mod",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Compile failed: %v", err)
	}

	cfg := wasm.DefaultConfig()
	engine, err := wasm.NewEngine(ctx, cfg)
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// Case 1: Empty TenantID -> must fallback to default namespace "tenant.default.job_latency_ms"
	var recordedMetricsDefault = make(map[string]float64)
	hostCtx1 := &wasm.HostContext{
		TenantID: "",
		Permissions: &permission.EffectivePermissions{
			HostCalls: map[string]bool{"metrics:record": true},
		},
		MetricHandler: func(name string, value float64) {
			recordedMetricsDefault[name] = value
		},
	}
	execCtx1 := wasm.WithHostContext(ctx, hostCtx1)
	res1, err := engine.Execute(execCtx1, compileRes.WasmBytes, wasm.ExecOptions{
		Timeout: 30 * time.Second,
	})
	if err != nil || res1.ExitCode != 0 {
		t.Fatalf("exec 1 failed: %v (exit: %d)", err, res1.ExitCode)
	}
	if val, ok := recordedMetricsDefault["tenant.default.job_latency_ms"]; !ok || val != 123.45 {
		t.Errorf("expected fallback metric %q, got map: %+v", "tenant.default.job_latency_ms", recordedMetricsDefault)
	}

	// Case 2: TenantID with invalid characters (e.g. "../bad") -> must be rejected (code 4)
	hostCtx2 := &wasm.HostContext{
		TenantID: "evil/tenant",
		Permissions: &permission.EffectivePermissions{
			HostCalls: map[string]bool{"metrics:record": true},
		},
	}
	execCtx2 := wasm.WithHostContext(ctx, hostCtx2)
	res2, err := engine.Execute(execCtx2, compileRes.WasmBytes, wasm.ExecOptions{
		Timeout: 30 * time.Second,
	})
	if res2 == nil || res2.ExitCode != 4 {
		t.Errorf("expected exit code 4 for malformed tenantID, got res: %+v, err: %v", res2, err)
	}
}
