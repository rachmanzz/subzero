package management

import (
	"time"
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
	TenantID    string
	ModuleName  string
	TargetOS    string // Default: "wasip1"
	Optimize    bool   // Strip symbols (-ldflags="-s -w")
	BuildTags   string
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
