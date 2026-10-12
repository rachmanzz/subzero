package wasm_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/pkg/management"
	"github.com/rachmanzz/subzero/pkg/permission"
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

	if val, ok := recordedMetrics["custom_metric"]; !ok || val != 42.5 {
		t.Errorf("expected metric 'custom_metric' = 42.5, got %v", val)
	}
}
