package wasm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

var (
	// ErrExecutionFailed is returned when Wasm guest code execution fails.
	ErrExecutionFailed = errors.New("wasm: module execution failed")

	// ErrExecutionTimeout is returned when execution exceeds the allocated wall-clock timeout.
	ErrExecutionTimeout = errors.New("wasm: execution timed out (CPU time budget exceeded)")

	// ErrFuelExhausted is returned when guest function call/operation budget is exhausted.
	ErrFuelExhausted = errors.New("wasm: execution fuel exhausted (operation quota exceeded)")

	// ErrMemoryLimitExceeded is returned when the module exceeds its Wasm memory page quota.
	ErrMemoryLimitExceeded = errors.New("wasm: memory limit exceeded (out of memory quota)")
)

// Config configures the Wazero WebAssembly sandbox.
type Config struct {
	MemoryLimitPages         uint32        // Memory limit in 64KB Wasm pages (e.g. 512 pages = 32MB)
	DefaultTimeout           time.Duration // Default timeout per execution (e.g. 10s)
	DefaultFuel              uint64        // Default operation quota (0 = disabled/unlimited)
	EnableCloseOnContextDone bool          // Preempt execution on context cancel/timeout (default: true)
}

// DefaultConfig returns safe multi-tenant sandbox defaults.
func DefaultConfig() Config {
	return Config{
		MemoryLimitPages:         512, // 32 MB
		DefaultTimeout:           10 * time.Second,
		DefaultFuel:              0, // Unlimited by default, can be set per-task
		EnableCloseOnContextDone: true,
	}
}

// Engine manages WebAssembly execution environments using Wazero (pure-Go, zero CGo).
type Engine struct {
	cfg   Config
	rt    wazero.Runtime
	cache sync.Map // map[string]wazero.CompiledModule (keyed by sha256 of bytecode)
}

// NewEngine initializes a Wazero runtime with WASI preview1 support and resource sandboxing.
func NewEngine(ctx context.Context, cfg Config) (*Engine, error) {
	if cfg.MemoryLimitPages == 0 {
		cfg.MemoryLimitPages = 512
	}
	if cfg.DefaultTimeout == 0 {
		cfg.DefaultTimeout = 10 * time.Second
	}

	rtCfg := wazero.NewRuntimeConfig().
		WithCloseOnContextDone(true).
		WithMemoryLimitPages(cfg.MemoryLimitPages)

	rt := wazero.NewRuntimeWithConfig(ctx, rtCfg)

	// Instantiate WASI snapshot preview1 (needed for Go wasip1 targets)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wasm: failed to instantiate wasi: %w", err)
	}

	// Instantiate subzero_host host function module
	if err := RegisterHostModule(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("wasm: failed to instantiate subzero_host: %w", err)
	}

	return &Engine{
		cfg: cfg,
		rt:  rt,
	}, nil
}

// ExecResult contains execution metrics and outputs from a Wasm module invocation.
type ExecResult struct {
	ExitCode           uint32
	Stdout             string
	Stderr             string
	Duration           time.Duration
	MemoryPages        uint32 // Final memory pages used (each page is 64KB)
	MemoryBytes        uint64 // Final memory bytes allocated
	OperationsExecuted uint64 // Number of operations/calls metered
	FuelRemaining      uint64 // Remaining fuel budget (if metering was active)
}

// DirMount specifies a host directory mounted into the guest WASI filesystem.
type DirMount struct {
	HostDir   string
	GuestPath string
	ReadOnly  bool
}

// ExecOptions defines runtime parameters passed into the Wasm instance.
type ExecOptions struct {
	Stdin     io.Reader
	Env       map[string]string
	Args      []string
	Timeout   time.Duration
	FuelLimit uint64 // Maximum operations allowed (0 = use default or unlimited)
	DirMounts []DirMount
}

type fuelListener struct {
	cancel    context.CancelCauseFunc
	fuel      *atomic.Int64
	consumed  *atomic.Uint64
	exhausted *atomic.Bool
}

func (l *fuelListener) NewFunctionListener(api.FunctionDefinition) experimental.FunctionListener {
	return l
}

func (l *fuelListener) Before(ctx context.Context, mod api.Module, def api.FunctionDefinition, params []uint64, stackIterator experimental.StackIterator) {
	if l.consumed != nil {
		l.consumed.Add(1)
	}
	if l.fuel != nil {
		if l.fuel.Add(-1) <= 0 {
			if l.exhausted != nil && l.exhausted.CompareAndSwap(false, true) {
				if l.cancel != nil {
					l.cancel(ErrFuelExhausted)
				}
				_ = mod.CloseWithExitCode(ctx, 137)
			}
		}
	}
}

func (l *fuelListener) After(ctx context.Context, mod api.Module, def api.FunctionDefinition, results []uint64) {
}

func (l *fuelListener) Abort(ctx context.Context, mod api.Module, def api.FunctionDefinition, err error) {
}

// Execute runs a compiled .wasm binary in a sandboxed WASI environment with resource metering.
func (e *Engine) Execute(ctx context.Context, wasmBytes []byte, opts ExecOptions) (*ExecResult, error) {
	timeout := opts.Timeout
	if timeout == 0 && e.cfg.DefaultTimeout > 0 {
		timeout = e.cfg.DefaultTimeout
	}

	execCtx := ctx
	if timeout > 0 {
		var timeoutCancel context.CancelFunc
		execCtx, timeoutCancel = context.WithTimeout(ctx, timeout)
		defer timeoutCancel()
	}

	execCtx, cancelCause := context.WithCancelCause(execCtx)
	defer cancelCause(nil)

	// Setup fuel & operation metering ONLY if fuelLimit is specified
	var opsCount atomic.Uint64
	var fuelLeft atomic.Int64
	var exhausted atomic.Bool

	fuelLimit := opts.FuelLimit
	if fuelLimit == 0 && e.cfg.DefaultFuel > 0 {
		fuelLimit = e.cfg.DefaultFuel
	}

	var listener *fuelListener
	if fuelLimit > 0 {
		fuelLeft.Store(int64(fuelLimit))
		listener = &fuelListener{
			cancel:    cancelCause,
			fuel:      &fuelLeft,
			consumed:  &opsCount,
			exhausted: &exhausted,
		}
		execCtx = experimental.WithFunctionListenerFactory(execCtx, listener)
	}

	// 1. Get or compile Wasm bytecode into CompiledModule using parent ctx
	compiled, err := e.compileOrGet(ctx, wasmBytes, listener)
	if err != nil {
		return nil, err
	}

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

	// Mount configured sandboxed directories
	if len(opts.DirMounts) > 0 {
		fsCfg := wazero.NewFSConfig()
		for _, m := range opts.DirMounts {
			if m.ReadOnly {
				fsCfg = fsCfg.WithReadOnlyDirMount(m.HostDir, m.GuestPath)
			} else {
				fsCfg = fsCfg.WithDirMount(m.HostDir, m.GuestPath)
			}
		}
		modCfg = modCfg.WithFSConfig(fsCfg)
	}

	// Instantiate and execute _start (WASI main entrypoint)
	execStart := time.Now()
	mod, err := e.rt.InstantiateModule(execCtx, compiled, modCfg)
	duration := time.Since(execStart)

	if err != nil {
		cause := context.Cause(execCtx)
		if errors.Is(cause, ErrFuelExhausted) {
			return &ExecResult{
				ExitCode:           137,
				Stdout:             stdoutBuf.String(),
				Stderr:             stderrBuf.String(),
				Duration:           duration,
				OperationsExecuted: opsCount.Load(),
			}, fmt.Errorf("%w (budget was %d operations)", ErrFuelExhausted, fuelLimit)
		}

		if execCtx.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) {
			return &ExecResult{
				ExitCode:           124,
				Stdout:             stdoutBuf.String(),
				Stderr:             stderrBuf.String(),
				Duration:           duration,
				OperationsExecuted: opsCount.Load(),
			}, fmt.Errorf("%w (timeout was %v)", ErrExecutionTimeout, timeout)
		}

		if execCtx.Err() == context.Canceled {
			return &ExecResult{
				ExitCode:           130,
				Stdout:             stdoutBuf.String(),
				Stderr:             stderrBuf.String(),
				Duration:           duration,
				OperationsExecuted: opsCount.Load(),
			}, fmt.Errorf("wasm: execution canceled: %w", err)
		}

		errStr := err.Error()
		if strings.Contains(errStr, "memory") || strings.Contains(errStr, "grow") || strings.Contains(stderrBuf.String(), "out of memory") {
			return &ExecResult{
				ExitCode:           1,
				Stdout:             stdoutBuf.String(),
				Stderr:             stderrBuf.String(),
				Duration:           duration,
				OperationsExecuted: opsCount.Load(),
			}, fmt.Errorf("%w: %v (stderr: %s)", ErrMemoryLimitExceeded, err, stderrBuf.String())
		}

		return &ExecResult{
			ExitCode:           1,
			Stdout:             stdoutBuf.String(),
			Stderr:             stderrBuf.String(),
			Duration:           duration,
			OperationsExecuted: opsCount.Load(),
		}, fmt.Errorf("%w: %v (stderr: %s)", ErrExecutionFailed, err, stderrBuf.String())
	}
	defer mod.Close(ctx)

	var memPages uint32
	var memBytes uint64
	if mem := mod.Memory(); mem != nil {
		memPages = mem.Size()
		memBytes = uint64(memPages) * 65536
	}

	var remainingFuel uint64
	if fuelLimit > 0 {
		fl := fuelLeft.Load()
		if fl > 0 {
			remainingFuel = uint64(fl)
		}
	}

	return &ExecResult{
		ExitCode:           0,
		Stdout:             stdoutBuf.String(),
		Stderr:             stderrBuf.String(),
		Duration:           duration,
		MemoryPages:        memPages,
		MemoryBytes:        memBytes,
		OperationsExecuted: opsCount.Load(),
		FuelRemaining:      remainingFuel,
	}, nil
}

func (e *Engine) compileOrGet(ctx context.Context, wasmBytes []byte, listener *fuelListener) (wazero.CompiledModule, error) {
	h := sha256.Sum256(wasmBytes)
	key := string(h[:])
	if listener != nil {
		key += ":metered"
	} else {
		key += ":unmetered"
	}

	if val, ok := e.cache.Load(key); ok {
		return val.(wazero.CompiledModule), nil
	}

	compileCtx := ctx
	if listener != nil {
		compileCtx = experimental.WithFunctionListenerFactory(compileCtx, listener)
	}

	compiled, err := e.rt.CompileModule(compileCtx, wasmBytes)
	if err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "over limit of") || strings.Contains(errStr, "section memory") || strings.Contains(errStr, "memory") {
			return nil, fmt.Errorf("%w: %v", ErrMemoryLimitExceeded, err)
		}
		return nil, fmt.Errorf("wasm: compilation failed: %w", err)
	}

	actual, loaded := e.cache.LoadOrStore(key, compiled)
	if loaded {
		_ = compiled.Close(ctx)
		return actual.(wazero.CompiledModule), nil
	}
	return compiled, nil
}

// Close shuts down the Wazero runtime and releases all compiled modules.
func (e *Engine) Close(ctx context.Context) error {
	e.cache.Range(func(_, val any) bool {
		if cm, ok := val.(wazero.CompiledModule); ok {
			_ = cm.Close(ctx)
		}
		return true
	})
	return e.rt.Close(ctx)
}
