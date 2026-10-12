package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/rachmanzz/subzero/internal/runtime/wasm"
	"github.com/rachmanzz/subzero/internal/storage"
	_ "github.com/rachmanzz/subzero/internal/storage/local"
	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/rachmanzz/subzero/pkg/protocol"
	"github.com/rachmanzz/subzero/pkg/sdk"
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

// ModuleRequestResolver resolves an action target to a ModuleRequest (from config.zero).
type ModuleRequestResolver func(target string) (*permission.ModuleRequest, error)

// WasmHandlerConfig configures the Wasm worker handler with capability policy.
type WasmHandlerConfig struct {
	Engine                *wasm.Engine
	Resolver              WasmModuleResolver
	WorkerPolicy          *permission.WorkerPolicy
	ModuleRequestResolver ModuleRequestResolver
	StorageGet            func(ctx context.Context, key string) ([]byte, error)
	StoragePut            func(ctx context.Context, key string, data []byte) error
	LogHandler            func(level int, msg string)
	MetricHandler         func(name string, value float64)
}

// NewWasmHandler creates a Worker HandlerFunc using default worker policy.
func NewWasmHandler(engine *wasm.Engine, resolver WasmModuleResolver) HandlerFunc {
	policy := permission.DefaultWorkerPolicy()
	return NewWasmHandlerWithConfig(WasmHandlerConfig{
		Engine:       engine,
		Resolver:     resolver,
		WorkerPolicy: &policy,
	})
}

// NewWasmHandlerWithConfig creates a Worker HandlerFunc with custom capability policy and request resolution.
func NewWasmHandlerWithConfig(cfg WasmHandlerConfig) HandlerFunc {
	resolver := cfg.Resolver
	if resolver == nil {
		resolver = DefaultFileResolver
	}

	workerPolicy := cfg.WorkerPolicy
	if workerPolicy == nil {
		p := permission.DefaultWorkerPolicy()
		workerPolicy = &p
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

		// 1. Resolve requested permissions for module (if any)
		var moduleReq *permission.ModuleRequest
		if cfg.ModuleRequestResolver != nil {
			if r, err := cfg.ModuleRequestResolver(task.ActionTarget); err == nil {
				moduleReq = r
			}
		}

		// Check if task.CustomConfig passes requested permissions in JSON
		if pJSON, ok := task.CustomConfig["permissions"]; ok && pJSON != "" {
			var r permission.ModuleRequest
			if err := json.Unmarshal([]byte(pJSON), &r); err == nil {
				moduleReq = &r
			}
		}

		// 2. Evaluate Effective Permissions (Privilege Escalation Check)
		effective, err := permission.Evaluate(workerPolicy, moduleReq)
		if err != nil {
			return &protocol.ResultMessage{
				TaskID:       task.TaskID,
				IsSuccess:    false,
				ErrorMessage: fmt.Sprintf("permission denied: %v", err),
			}, nil
		}

		// 3. Enforce S3 prefix policy on task.SourceURI
		if task.SourceURI != "" && strings.HasPrefix(task.SourceURI, "s3://") {
			allowed := false
			for _, prefix := range effective.S3.AllowedPrefixes {
				if prefix == "*" || prefix == "s3://" || strings.HasPrefix(task.SourceURI, prefix) {
					allowed = true
					break
				}
			}
			if !allowed {
				return &protocol.ResultMessage{
					TaskID:       task.TaskID,
					IsSuccess:    false,
					ErrorMessage: fmt.Sprintf("permission denied: source URI %q not permitted by S3 prefixes %v", task.SourceURI, effective.S3.AllowedPrefixes),
				}, nil
			}
		}

		// 3b. Enforce Local Filesystem root policy on task.SourceURI
		if task.SourceURI != "" && (strings.HasPrefix(task.SourceURI, "file://") || strings.HasPrefix(task.SourceURI, "/")) {
			localPath := strings.TrimPrefix(task.SourceURI, "file://")
			if len(effective.Filesystem.ReadOnly) > 0 && !permission.IsPathUnderRoots(localPath, effective.Filesystem.ReadOnly) {
				return &protocol.ResultMessage{
					TaskID:       task.TaskID,
					IsSuccess:    false,
					ErrorMessage: fmt.Sprintf("permission denied: source URI %q not permitted under allowed filesystem roots %v", task.SourceURI, effective.Filesystem.ReadOnly),
				}, nil
			}
		}

		// 4. Enforce File Extension policy on task.SourceURI
		if task.SourceURI != "" && len(effective.Files.AllowedExtensions) > 0 {
			hasWildcard := false
			for _, ext := range effective.Files.AllowedExtensions {
				if ext == "*" {
					hasWildcard = true
					break
				}
			}
			if !hasWildcard {
				sourceLower := strings.ToLower(task.SourceURI)
				extAllowed := false
				for _, ext := range effective.Files.AllowedExtensions {
					if strings.HasSuffix(sourceLower, ext) {
						extAllowed = true
						break
					}
				}
				if !extAllowed {
					return &protocol.ResultMessage{
						TaskID:       task.TaskID,
						IsSuccess:    false,
						ErrorMessage: fmt.Sprintf("permission denied: file extension of %q not permitted by policy %v", task.SourceURI, effective.Files.AllowedExtensions),
					}, nil
				}
			}
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

		// Mount effective read-only directories
		for _, ro := range effective.Filesystem.ReadOnly {
			execOpts.DirMounts = append(execOpts.DirMounts, wasm.DirMount{
				HostDir:   ro,
				GuestPath: ro,
				ReadOnly:  true,
			})
		}
		// Mount effective read-write scratch directories
		for _, rw := range effective.Filesystem.ReadWrite {
			execOpts.DirMounts = append(execOpts.DirMounts, wasm.DirMount{
				HostDir:   rw,
				GuestPath: rw,
				ReadOnly:  false,
			})
		}

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

		storageGet := cfg.StorageGet
		if storageGet == nil {
			storageGet = func(c context.Context, key string) ([]byte, error) {
				// Validate S3 prefix if key is an S3 URI
				if strings.HasPrefix(key, "s3://") {
					if !permission.IsS3PrefixAllowed(key, effective.S3.AllowedPrefixes) {
						return nil, fmt.Errorf("%w: key %q not allowed by S3 prefixes %v",
							permission.ErrUnauthorizedS3Prefix, key, effective.S3.AllowedPrefixes)
					}
				}
				// Validate local filesystem path if key is a file URI
				if strings.HasPrefix(key, "file://") || strings.HasPrefix(key, "/") {
					localPath := strings.TrimPrefix(key, "file://")
					if len(effective.Filesystem.ReadOnly) > 0 && !permission.IsPathUnderRoots(localPath, effective.Filesystem.ReadOnly) {
						return nil, fmt.Errorf("%w: path %q not allowed under filesystem roots %v",
							permission.ErrUnauthorizedPath, localPath, effective.Filesystem.ReadOnly)
					}
				}
				return storage.ReadAllStream(c, key)
			}
		}

		storagePut := cfg.StoragePut
		if storagePut == nil {
			storagePut = func(c context.Context, key string, data []byte) error {
				// Validate S3 prefix if key is an S3 URI
				if strings.HasPrefix(key, "s3://") {
					if !permission.IsS3PrefixAllowed(key, effective.S3.AllowedPrefixes) {
						return fmt.Errorf("%w: key %q not allowed by S3 prefixes %v",
							permission.ErrUnauthorizedS3Prefix, key, effective.S3.AllowedPrefixes)
					}
				}
				d, err := storage.ResolveDriver(key)
				if err != nil {
					return err
				}
				return d.PutObject(c, key, strings.NewReader(string(data)), int64(len(data)))
			}
		}

		// Attach HostContext with effective permissions and callbacks
		hostCtx := &wasm.HostContext{
			Permissions:   effective,
			StorageGet:    storageGet,
			StoragePut:    storagePut,
			LogHandler:    cfg.LogHandler,
			MetricHandler: cfg.MetricHandler,
		}
		execCtx := wasm.WithHostContext(ctx, hostCtx)

		execRes, err := cfg.Engine.Execute(execCtx, wasmBytes, execOpts)
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

		// Parse structured sdk.Response if emitted by guest module
		var sdkResp sdk.Response
		trimmedStdout := strings.TrimSpace(execRes.Stdout)
		if strings.HasPrefix(trimmedStdout, "{") && json.Unmarshal([]byte(trimmedStdout), &sdkResp) == nil {
			errMsg := sdkResp.ErrorMessage
			if errMsg == "" {
				errMsg = execRes.Stderr
			}
			return &protocol.ResultMessage{
				TaskID:        task.TaskID,
				IsSuccess:     sdkResp.IsSuccess,
				ProcessedRows: int64(sdkResp.ProcessedRows),
				ErrorMessage:  errMsg,
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
