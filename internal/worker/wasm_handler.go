package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

var (
	// ErrModuleNotFound is returned when a requested wasm module cannot be resolved.
	ErrModuleNotFound = errors.New("wasm: module not found")
)

// WasmModuleResolver resolves an action target or module ID to raw .wasm bytecode.
type WasmModuleResolver func(target string) ([]byte, error)

// DefaultFileResolver reads .wasm bytes directly from a filesystem path.
func DefaultFileResolver(target string) ([]byte, error) {
	if strings.HasSuffix(target, ".wasm") || strings.HasPrefix(target, "/") || strings.HasPrefix(target, "./") {
		return os.ReadFile(target)
	}
	return nil, fmt.Errorf("wasm resolver: target %q is not a valid wasm file path", target)
}

// NewWasmHandler creates a Worker HandlerFunc that routes incoming TaskMessages to the Wazero Wasm sandbox.
func NewWasmHandler(engine *wasm.Engine, resolver WasmModuleResolver) HandlerFunc {
	if resolver == nil {
		resolver = DefaultFileResolver
	}

	return func(ctx context.Context, task *protocol.TaskMessage) (*protocol.ResultMessage, error) {
		wasmBytes, err := resolver(task.ActionTarget)
		if err != nil {
			return &protocol.ResultMessage{
				TaskID:       task.TaskID,
				IsSuccess:    false,
				ErrorMessage: fmt.Sprintf("failed to resolve wasm module %q: %v", task.ActionTarget, err),
			}, nil
		}

		// Inject task parameters into Wasm environment variables
		env := map[string]string{
			"SUBZERO_TASK_ID":       task.TaskID,
			"SUBZERO_TENANT_ID":     task.TenantID,
			"SUBZERO_ACTION_TARGET": task.ActionTarget,
			"SUBZERO_SOURCE_URI":    task.SourceURI,
		}

		for k, v := range task.CustomConfig {
			env["SUBZERO_CFG_"+strings.ToUpper(k)] = v
		}

		var execOpts wasm.ExecOptions
		execOpts.Env = env

		if tStr, ok := task.CustomConfig["timeout_ms"]; ok {
			if ms, err := strconv.Atoi(tStr); err == nil && ms > 0 {
				execOpts.Timeout = time.Duration(ms) * time.Millisecond
			}
		}

		if fStr, ok := task.CustomConfig["fuel_limit"]; ok {
			if fl, err := strconv.ParseUint(fStr, 10, 64); err == nil {
				execOpts.FuelLimit = fl
			}
		}

		execRes, err := engine.Execute(ctx, wasmBytes, execOpts)
		var execDuration string
		if execRes != nil {
			execDuration = execRes.Duration.String()
		}

		if err != nil {
			return &protocol.ResultMessage{
				TaskID:        task.TaskID,
				IsSuccess:     false,
				ErrorMessage:  fmt.Sprintf("wasm execution failed: %v", err),
				ExecutionTime: execDuration,
			}, nil
		}

		return &protocol.ResultMessage{
			TaskID:        task.TaskID,
			IsSuccess:     true,
			ErrorMessage:  execRes.Stderr,
			ExecutionTime: execDuration,
		}, nil
	}
}
