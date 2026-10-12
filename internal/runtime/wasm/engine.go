package wasm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

var (
	ErrExecutionFailed = errors.New("wasm: module execution failed")
)

// Config configures the Wazero WebAssembly sandbox.
type Config struct {
	MemoryLimitPages uint32 // Memory limit in 64KB Wasm pages (e.g. 1024 pages = 64MB)
}

// DefaultConfig returns default sandbox parameters.
func DefaultConfig() Config {
	return Config{
		MemoryLimitPages: 1024, // 64 MB
	}
}

// Engine manages WebAssembly execution environments using Wazero (pure-Go, zero CGo).
type Engine struct {
	cfg Config
	rt  wazero.Runtime
}

// NewEngine initializes a Wazero runtime with WASI preview1 support.
func NewEngine(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.MemoryLimitPages == 0 {
		cfg = DefaultConfig()
	}

	// Create Wazero runtime (uses JIT compiler on amd64/arm64 or interpreter fallback)
	rt := wazero.NewRuntime(ctx)

	// Instantiate WASI snapshot preview1 (needed for Go wasip1 targets)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wasm: failed to instantiate wasi: %w", err)
	}

	return &Engine{
		cfg: cfg,
		rt:  rt,
	}, nil
}

// ExecResult contains metrics and output from a Wasm module invocation.
type ExecResult struct {
	ExitCode uint32
	Stdout   string
	Stderr   string
	Duration time.Duration
}

// ExecOptions defines runtime parameters passed into the Wasm instance.
type ExecOptions struct {
	Stdin   io.Reader
	Env     map[string]string
	Args    []string
	Timeout time.Duration
}

// Execute runs a compiled .wasm binary in a sandboxed WASI environment.
func (e *Engine) Execute(ctx context.Context, wasmBytes []byte, opts ExecOptions) (*ExecResult, error) {
	start := time.Now()

	execCtx := ctx
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		execCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	// Compile the Wasm bytecode
	compiled, err := e.rt.CompileModule(execCtx, wasmBytes)
	if err != nil {
		return nil, fmt.Errorf("wasm: compilation failed: %w", err)
	}
	defer compiled.Close(execCtx)

	var stdoutBuf, stderrBuf bytes.Buffer

	// Build module configuration
	modCfg := wazero.NewModuleConfig().
		WithStdout(&stdoutBuf).
		WithStderr(&stderrBuf)

	if opts.Stdin != nil {
		modCfg = modCfg.WithStdin(opts.Stdin)
	}

	// Pass program arguments
	if len(opts.Args) > 0 {
		modCfg = modCfg.WithArgs(opts.Args...)
	} else {
		modCfg = modCfg.WithArgs("subzero-wasm")
	}

	// Pass environment variables
	for k, v := range opts.Env {
		modCfg = modCfg.WithEnv(k, v)
	}

	// Instantiate and execute _start (WASI main entrypoint)
	mod, err := e.rt.InstantiateModule(execCtx, compiled, modCfg)
	duration := time.Since(start)

	if err != nil {
		return &ExecResult{
			Stdout:   stdoutBuf.String(),
			Stderr:   stderrBuf.String(),
			Duration: duration,
		}, fmt.Errorf("%w: %v (stderr: %s)", ErrExecutionFailed, err, stderrBuf.String())
	}
	defer mod.Close(execCtx)

	return &ExecResult{
		ExitCode: 0,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		Duration: duration,
	}, nil
}

// Close shuts down the Wazero runtime and releases all compiled modules.
func (e *Engine) Close(ctx context.Context) error {
	return e.rt.Close(ctx)
}
