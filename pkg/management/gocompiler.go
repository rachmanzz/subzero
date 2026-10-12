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

// Compile compiles the given Go source code into WebAssembly bytecode using GOOS=wasip1 GOARCH=wasm.
func (c *GoCompiler) Compile(ctx context.Context, sourceCode []byte, opts CompileOptions) (*CompileResult, error) {
	start := time.Now()

	targetOS := opts.TargetOS
	if targetOS == "" {
		targetOS = "wasip1"
	}

	tmpDir, err := os.MkdirTemp("", "subzero-wasm-build-*")
	if err != nil {
		return nil, fmt.Errorf("compiler: failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	srcFile := filepath.Join(tmpDir, "main.go")
	if err := os.WriteFile(srcFile, sourceCode, 0644); err != nil {
		return nil, fmt.Errorf("compiler: failed to write source file: %w", err)
	}

	outFile := filepath.Join(tmpDir, "output.wasm")

	args := []string{"build"}
	if opts.Optimize {
		args = append(args, "-ldflags", "-s -w")
	}
	args = append(args, "-o", outFile, srcFile)

	cmd := exec.CommandContext(ctx, c.goBinary, args...)
	cmd.Dir = tmpDir

	// Set Wasm compilation environment variables
	cmd.Env = append(os.Environ(),
		"GOOS="+targetOS,
		"GOARCH=wasm",
		"CGO_ENABLED=0",
	)

	var logBuf bytes.Buffer
	cmd.Stdout = &logBuf
	cmd.Stderr = &logBuf

	err = cmd.Run()
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
