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

## 2. WebAssembly (Wasm) Compilation Flow

To support custom tenant transformations without recompiling the main binary, Subzero allows uploading raw Go source files (`.go`) and compiling them directly into WebAssembly bytecode:

```text
[ Browser / Developer ] ──(Upload main.go)──► [ POST /api/modules/compile ]
                                                      │
                                                      ▼
                                            [ Compiler Adapter ]
                                            (GOOS=wasip1 GOARCH=wasm)
                                                      │
                                                      ▼
                                            [ Output: module.wasm ]
                                                      │
                                                      ▼
                                            [ Module Store ]
                                            (Persisted with metadata)
```

### The `Compiler` Interface
Located in [`pkg/management/compiler.go`](../pkg/management/compiler.go):
```go
type Compiler interface {
    Name() string
    Compile(ctx context.Context, sourceCode []byte, opts CompileOptions) (*CompileResult, error)
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
* **Default `FileStore`**: Stores `.wasm` binaries, original `source.go` files, and `meta.json` records in organized directories on disk.

---

## 4. REST API Endpoints

| Method | Endpoint | Description |
| :--- | :--- | :--- |
| `GET` | `/` | Renders the embedded dark-themed HTML Dashboard UI. |
| `GET` | `/api/status` | Returns system health, active compiler name, and module statistics. |
| `GET` | `/api/modules` | Lists all registered Wasm modules (optional filter: `?tenant_id=...`). |
| `POST` | `/api/modules/compile` | Multipart upload of a `.go` file; compiles it and stores the resulting Wasm module. |
| `GET` | `/api/modules/{id}/download` | Downloads the compiled `.wasm` binary file (`application/wasm`). |
| `DELETE` | `/api/modules/{id}` | Deletes a module and its on-disk artifacts. |

---

## 5. Embedded Dashboard UI

The dashboard UI ([`pkg/management/ui/dashboard.html`](../pkg/management/ui/dashboard.html)) is compiled directly into the Go binary via `//go:embed`.
* **Zero External Dependencies**: Uses vanilla HTML5, CSS3, and modern JavaScript. Requires no external CDN, working 100% offline or inside air-gapped private networks.
* **Features**:
  * Live status counters (Modules, Compiler Adapter, Scale-to-Zero status).
  * Drag-and-drop `.go` file upload with real-time compilation console logs.
  * Interactive module management table with download and delete capabilities.
