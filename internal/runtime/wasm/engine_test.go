package wasm_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/pkg/management"
)

func TestWasmEngineExecution(t *testing.T) {
	ctx := context.Background()

	// 1. Compile a Go program to wasip1
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

	compiler := management.NewGoCompiler("")
	compileRes, err := compiler.Compile(ctx, []byte(sourceCode), management.CompileOptions{
		TenantID:   "tenant-wazero",
		ModuleName: "demo-task",
		TargetOS:   "wasip1",
		Optimize:   true,
	})
	if err != nil {
		t.Fatalf("Wasm compilation failed: %v", err)
	}

	// 2. Initialize Wazero Engine
	engine, err := wasm.NewEngine(ctx, wasm.DefaultConfig())
	if err != nil {
		t.Fatalf("NewEngine failed: %v", err)
	}
	defer engine.Close(ctx)

	// 3. Execute compiled Wasm
	execRes, err := engine.Execute(ctx, compileRes.WasmBytes, wasm.ExecOptions{
		Env: map[string]string{
			"SUBZERO_TENANT": "Acme-Logistics",
		},
		Timeout: 5 * time.Second,
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
}
