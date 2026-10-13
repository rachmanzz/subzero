package management

import (
	"time"

	"github.com/rachmanzz/subzero/pkg/permission"
)

// WasmModule represents a compiled WebAssembly artifact stored in Subzero.
type WasmModule struct {
	ID          string       `json:"id"`
	TenantID    string       `json:"tenant_id"`
	Name        string       `json:"name"`
	Target      string       `json:"target"` // e.g. "wasip1", "js"
	Size        int64        `json:"size"`
	WasmPath    string       `json:"wasm_path"`
	SourcePath  string       `json:"source_path,omitempty"`
	Status      string       `json:"status"` // e.g. "PUBLISHED", "REJECTED"
	Audit       *AuditReport `json:"audit,omitempty"`
	Config      *ConfigZero  `json:"config,omitempty"`
	Readme      string       `json:"readme,omitempty"`
	CreatedAt   time.Time    `json:"created_at"`
	CompileTime string       `json:"compile_time"`
}

// CompileOptions defines compiler flags and parameters.
type CompileOptions struct {
	TenantID   string
	ModuleName string
	TargetOS   string // Default: "wasip1"
	Optimize   bool   // Strip symbols (-ldflags="-s -w")
	BuildTags  string
}

// CompileResult represents the output of a Wasm compilation process.
type CompileResult struct {
	ModuleID    string        `json:"module_id"`
	Success     bool          `json:"success"`
	Size        int64         `json:"size"`
	Duration    time.Duration `json:"duration"`
	CompilerLog string        `json:"compiler_log"`
	Audit       *AuditReport  `json:"audit,omitempty"`
	WasmBytes   []byte        `json:"-"`
}

// InspectPolicyRequest defines the payload for testing module capability against a worker policy (PRD 5.5).
type InspectPolicyRequest struct {
	ModuleRequest *permission.ModuleRequest `json:"module_request,omitempty"`
	WorkerPolicy  *permission.WorkerPolicy  `json:"worker_policy,omitempty"`
}

// InspectPolicyResponse provides the evaluation outcome of two-sided capability negotiation (PRD 5.5).
type InspectPolicyResponse struct {
	ModuleID             string                           `json:"module_id,omitempty"`
	TenantID             string                           `json:"tenant_id,omitempty"`
	Allowed              bool                             `json:"allowed"`
	EffectivePermissions *permission.EffectivePermissions `json:"effective_permissions,omitempty"`
	RequestedPermissions *permission.ModuleRequest        `json:"requested_permissions,omitempty"`
	Error                string                           `json:"error,omitempty"`
}
