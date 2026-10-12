package management

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// GoCompiler implements Compiler using the native Go toolchain (`go build`).
type GoCompiler struct {
	goBinary string
}

// NewGoCompiler creates a compiler adapter using the host's `go` binary.
func NewGoCompiler(goBinary string) *GoCompiler {
	if goBinary == "" {
		goBinary = "go"
	}
	return &GoCompiler{goBinary: goBinary}
}

func (c *GoCompiler) Name() string {
	return "go"
}

// Compile compiles single source code bytes into WebAssembly.
func (c *GoCompiler) Compile(ctx context.Context, sourceCode []byte, opts CompileOptions) (*CompileResult, error) {
	tmpDir, err := os.MkdirTemp("", "subzero-wasm-build-*")
	if err != nil {
		return nil, fmt.Errorf("compiler: failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	srcFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(srcFile, sourceCode, 0644); err != nil {
		return nil, fmt.Errorf("compiler: failed to write source file: %w", err)
	}

	return c.CompileDir(ctx, tmpDir, opts)
}

// CompileDir compiles a Go package directory into WebAssembly bytecode using GOOS=wasip1 GOARCH=wasm.
func (c *GoCompiler) CompileDir(ctx context.Context, dir string, opts CompileOptions) (*CompileResult, error) {
	start := time.Now()

	targetOS := opts.TargetOS
	if targetOS == "" {
		targetOS = "wasip1"
	}

	// Ensure a go.mod exists in the build directory if missing
	goModPath := filepath.Join(dir, "go.mod")
	if _, err := os.Stat(goModPath); os.IsNotExist(err) {
		_ = os.WriteFile(goModPath, []byte("module subzero/userwasm\n\ngo 1.22\n"), 0644)
	}

	outFile := filepath.Join(dir, "output.wasm")

	args := []string{"build"}
	if opts.Optimize {
		args = append(args, "-ldflags", "-s -w")
	}
	args = append(args, "-o", outFile, ".")

	cmd := exec.CommandContext(ctx, c.goBinary, args...)
	cmd.Dir = dir

	// Enforce Wasm compilation environment variables
	cmd.Env = append(os.Environ(),
		"GOOS="+targetOS,
		"GOARCH=wasm",
		"CGO_ENABLED=0",
	)

	var logBuf bytes.Buffer
	cmd.Stdout = &logBuf
	cmd.Stderr = &logBuf

	err := cmd.Run()
	duration := time.Since(start)

	if err != nil {
		return &CompileResult{
			Success:     false,
			Duration:    duration,
			CompilerLog: logBuf.String(),
		}, fmt.Errorf("compiler: go build failed: %w (log: %s)", err, logBuf.String())
	}

	wasmBytes, err := os.ReadFile(outFile)
	if err != nil {
		return nil, fmt.Errorf("compiler: failed to read generated wasm: %w", err)
	}

	return &CompileResult{
		Success:     true,
		Size:        int64(len(wasmBytes)),
		Duration:    duration,
		CompilerLog: logBuf.String(),
		WasmBytes:   wasmBytes,
	}, nil
}
