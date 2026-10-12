# API Management, Dashboard & WebAssembly Control Plane

Subzero includes an integrated control plane package ([`pkg/management`](../pkg/management)) built with the **Chi v5** HTTP router. It provides a visual management dashboard and an automated WebAssembly (Wasm) compilation engine for tenant transformation scripts.

---

## 1. Pluggable Architecture

The management server is designed as a modular component that can run as a standalone server or be embedded directly into an existing Chi application:

```go
package main

import (
    "net/http"
    "github.com/go-chi/chi/v5"
    "github.com/rachmanzz/subzero/pkg/management"
)

func main() {
    r := chi.NewRouter()

    // Initialize Subzero Management Server
    adminServer, err := management.NewServer(management.ServerConfig{})
    if err != nil {
        panic(err)
    }

    // Mount Subzero Control Plane under /admin
    r.Mount("/admin", adminServer.Routes())

    http.ListenAndServe(":8080", r)
}
```

---

## 2. Secure WebAssembly (Wasm) Ingestion & Compilation Pipeline

Subzero accepts tenant transformation packages as `.tar.gz` / `.tgz` archives (or single `.go` files). To ensure enterprise multi-tenant isolation and security, uploads must pass through a strict 5-stage verification gate before publication:

```text
[ Tenant / Developer ] ──(Upload package.tar.gz)──► [ POST /api/modules/compile ]
                                                              │
                                                              ▼
                                                   [ Stage 1: Safe Unpack ]
                                                   • Zip-Slip Traversal Protection
                                                   • Decompression Bomb Limits
                                                   • Extension Whitelist (.go, go.mod)
                                                              │
                                                              ▼
                                                   [ Stage 2: Structure Verify ]
                                                   • package main validation
                                                   • func main() entrypoint check
                                                              │
                                                              ▼
                                                   [ Stage 3: Backdoor & AST Audit ]
                                                   • Forbidden Imports: os/exec, syscall, unsafe, C, plugin
                                                   • Network Server Listeners: net.Listen, ListenPacket
                                                   • Suspicious Shell Literals: /bin/sh, /bin/bash, cmd.exe
                                                              │
                                                              ▼
                                                    Passed? ──┬── No ──► [ REJECT 422: Return Violations ]
                                                              │
                                                             Yes
                                                              ▼
                                                   [ Stage 4: Native Build ]
                                                   (GOOS=wasip1 GOARCH=wasm)
                                                              │
                                                              ▼
                                                   [ Stage 5: Publish & Store ]
                                                   (Persisted as PUBLISHED & Verified Safe)
```

### The `Compiler` Interface
Located in [`pkg/management/compiler.go`](../pkg/management/compiler.go):
```go
type Compiler interface {
    Name() string
    Compile(ctx context.Context, sourceCode []byte, opts CompileOptions) (*CompileResult, error)
    CompileDir(ctx context.Context, dir string, opts CompileOptions) (*CompileResult, error)
}
```

### Default Implementation: `GoCompiler`
Located in [`pkg/management/gocompiler.go`](../pkg/management/gocompiler.go):
* Uses the host's standard Go compiler (`go build`).
* Compiles for **WASI** (`GOOS=wasip1 GOARCH=wasm`).
* Automatically applies optimization flags (`-ldflags="-s -w"`) to strip debug symbols and minimize binary size.
* Average compilation time is under **100 milliseconds** for typical transformation scripts.

---

## 3. Module Repository: The `Store` Interface

Located in [`pkg/management/store.go`](../pkg/management/store.go):
```go
type Store interface {
    Save(ctx context.Context, module *WasmModule, wasmBytes []byte, sourceBytes []byte) error
    List(ctx context.Context, tenantID string) ([]*WasmModule, error)
    Get(ctx context.Context, moduleID string) (*WasmModule, error)
    GetWasmBytes(ctx context.Context, moduleID string) ([]byte, error)
    Delete(ctx context.Context, moduleID string) error
}
```
* **Default `FileStore`**: Stores `.wasm` binaries, original `source.tar.gz`/`source.go` files, security audit reports, and `meta.json` records in organized directories on disk.

---

## 4. REST API Endpoints

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `GET` | `/` | Renders the embedded dark-themed HTML Dashboard UI. |
| `GET` | `/api/status` | Returns system health, active compiler name, and module statistics. |
| `GET` | `/api/modules` | Lists all registered Wasm modules (optional filter: `?tenant_id=...`). |
| `POST` | `/api/modules/compile` | Multipart upload of a `.tar.gz`, `.tgz`, or `.go` archive; validates, audits, compiles, and publishes the Wasm module. |
| `GET` | `/api/modules/{id}/download` | Downloads the compiled `.wasm` binary file (`application/wasm`). |
| `DELETE` | `/api/modules/{id}` | Deletes a module and its on-disk artifacts. |

---

## 5. Embedded Dashboard UI

The dashboard UI ([`pkg/management/ui/dashboard.html`](../pkg/management/ui/dashboard.html)) is compiled directly into the Go binary via `//go:embed`.
* **Zero External Dependencies**: Uses vanilla HTML5, CSS3, and modern JavaScript. Requires no external CDN, working 100% offline or inside air-gapped private networks.
* **Features**:
  * Live status counters (Modules, Compiler Adapter, Scale-to-Zero status).
  * 5-Stage visual pipeline indicator: Safe Unpack ➔ Structure Check ➔ AST Security Audit ➔ Native Build ➔ Publish.
  * Drag-and-drop `.tar.gz` and `.go` upload with detailed audit and compilation logs.
  * Security status badges (`🛡️ Verified Safe` vs `⛔ Rejected`) with violation breakdown.
  * Interactive module management table with download and delete capabilities.
