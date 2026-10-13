package wasm

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/rachmanzz/subzero/pkg/permission"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

type hostContextKey struct{}

// HostContext defines the active permissions and host callbacks for a Wasm invocation.
type HostContext struct {
	TenantID      string
	Permissions   *permission.EffectivePermissions
	StorageGet    func(ctx context.Context, key string) ([]byte, error)
	StoragePut    func(ctx context.Context, key string, data []byte) error
	LogHandler    func(level int, msg string)
	MetricHandler func(name string, value float64)
}

// WithHostContext attaches a HostContext to the execution context.
func WithHostContext(ctx context.Context, hc *HostContext) context.Context {
	return context.WithValue(ctx, hostContextKey{}, hc)
}

// GetHostContext retrieves the HostContext from context, or returns nil.
func GetHostContext(ctx context.Context) *HostContext {
	val := ctx.Value(hostContextKey{})
	if val == nil {
		return nil
	}
	return val.(*HostContext)
}

// RegisterHostModule registers the "subzero_host" host module with the Wazero runtime.
func RegisterHostModule(ctx context.Context, rt wazero.Runtime) error {
	builder := rt.NewHostModuleBuilder("subzero_host")

	// Helper to enforce fail-closed permission check supporting direct name and wildcard "*"
	checkPermission := func(hc *HostContext, perm string) bool {
		if hc == nil || hc.Permissions == nil {
			return false
		}
		if hc.Permissions.HostCalls[perm] || hc.Permissions.HostCalls["*"] {
			return true
		}
		// Also support category wildcard e.g. "logger:*"
		if idx := strings.Index(perm, ":"); idx != -1 {
			if hc.Permissions.HostCalls[perm[:idx]+":*"] {
				return true
			}
		}
		return false
	}

	// 1. host_storage_get(keyPtr, keyLen, outPtr, outMaxLen, outReadPtr uint32) (errCode uint32)
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, m api.Module, keyPtr, keyLen, outPtr, outMaxLen, outReadPtr uint32) uint32 {
			hc := GetHostContext(ctx)
			if !checkPermission(hc, "storage:get_object") {
				return 1 // Permission Denied
			}
			if hc.StorageGet == nil {
				return 4 // Internal error / not configured
			}

			mem := m.Memory()
			keyBytes, ok := mem.Read(keyPtr, keyLen)
			if !ok {
				return 4 // Invalid memory address
			}

			data, err := hc.StorageGet(ctx, string(keyBytes))
			if err != nil {
				return 2 // Not found or fetch failed
			}

			// Write actual data length to outReadPtr
			if outReadPtr != 0 {
				mem.WriteUint32Le(outReadPtr, uint32(len(data)))
			}

			if uint32(len(data)) > outMaxLen {
				return 3 // Buffer too small
			}

			if !mem.Write(outPtr, data) {
				return 4 // Memory write failed
			}

			return 0 // Success
		}).
		Export("host_storage_get")

	// 2. host_storage_put(keyPtr, keyLen, valPtr, valLen uint32) (errCode uint32)
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, m api.Module, keyPtr, keyLen, valPtr, valLen uint32) uint32 {
			hc := GetHostContext(ctx)
			if !checkPermission(hc, "storage:put_object") {
				return 1 // Permission Denied
			}
			if hc.StoragePut == nil {
				return 4 // StoragePut handler not configured
			}

			mem := m.Memory()
			keyBytes, ok := mem.Read(keyPtr, keyLen)
			if !ok {
				return 4
			}
			valBytes, ok := mem.Read(valPtr, valLen)
			if !ok {
				return 4
			}

			if err := hc.StoragePut(ctx, string(keyBytes), valBytes); err != nil {
				return 4
			}
			return 0 // Success
		}).
		Export("host_storage_put")

	// 3. host_log(level uint32, msgPtr, msgLen uint32) (errCode uint32)
	// level: 0=DEBUG, 1=INFO, 2=WARN, 3=ERROR
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, m api.Module, level, msgPtr, msgLen uint32) uint32 {
			hc := GetHostContext(ctx)
			// Check permission for log level
			levelPerm := "logger:debug"
			switch level {
			case 1:
				levelPerm = "logger:info"
			case 2:
				levelPerm = "logger:warn"
			case 3:
				levelPerm = "logger:error"
			}

			if !checkPermission(hc, levelPerm) {
				return 1 // Permission Denied (Fail-Closed)
			}

			mem := m.Memory()
			msgBytes, ok := mem.Read(msgPtr, msgLen)
			if !ok {
				return 4
			}

			msg := string(msgBytes)
			if hc != nil && hc.LogHandler != nil {
				hc.LogHandler(int(level), msg)
			} else {
				fmt.Fprintf(os.Stderr, "[Subzero Host Log | %s] %s\n", levelPerm, msg)
			}
			return 0
		}).
		Export("host_log")

	// 4. host_metrics_record(namePtr, nameLen uint32, val float64) (errCode uint32)
	builder.NewFunctionBuilder().
		WithFunc(func(ctx context.Context, m api.Module, namePtr, nameLen uint32, val float64) uint32 {
			hc := GetHostContext(ctx)
			if !checkPermission(hc, "metrics:record") {
				return 1 // Permission Denied
			}

			mem := m.Memory()
			nameBytes, ok := mem.Read(namePtr, nameLen)
			if !ok {
				return 4
			}

			rawName := strings.TrimSpace(string(nameBytes))
			if rawName == "" || len(rawName) > 128 {
				return 4 // Invalid metric name length
			}

			// Validate allowed metric name characters [a-zA-Z0-9_.-]
			for _, ch := range rawName {
				if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '.' || ch == '-') {
					return 4 // Forbidden character in metric name
				}
			}

			// Enforce tenant namespace to prevent cross-tenant collision / cardinality explosion
			tenantID := "default"
			if hc != nil && strings.TrimSpace(hc.TenantID) != "" {
				tenantID = strings.TrimSpace(hc.TenantID)
			}

			// Validate tenantID characters [a-zA-Z0-9_.-]
			for _, ch := range tenantID {
				if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '.' || ch == '-') {
					return 4 // Forbidden character in tenant ID
				}
			}

			metricName := rawName
			prefix := fmt.Sprintf("tenant.%s.", tenantID)
			if !strings.HasPrefix(rawName, prefix) {
				metricName = fmt.Sprintf("tenant.%s.%s", tenantID, rawName)
			}

			// Validate final metric name length and characters
			if len(metricName) > 256 {
				return 4 // Metric name too long
			}
			for _, ch := range metricName {
				if !((ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '.' || ch == '-') {
					return 4 // Forbidden character in final metric name
				}
			}

			if hc != nil && hc.MetricHandler != nil {
				hc.MetricHandler(metricName, val)
			}
			return 0
		}).
		Export("host_metrics_record")

	_, err := builder.Instantiate(ctx)
	return err
}
