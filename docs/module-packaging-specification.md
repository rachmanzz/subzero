# Subzero WebAssembly Module Packaging Specification

This document provides the official technical specification for authoring, packaging, validating, and publishing WebAssembly (Wasm) data transformation modules within the **Subzero** ecosystem.

---

## 1. Philosophy & Design Goals

The Subzero B2B SaaS platform processes document files (spreadsheets, CSVs, JSON, or raw byte streams) from dozens or hundreds of tenants, each with disparate column layouts, validation rules, and business logic.

To protect the integrity of the host runtime and prevent cross-tenant cascading failures, all tenant-specific parsing logic is isolated into **Self-Contained Micro-Packages** compiled into sandboxed WebAssembly bytecode (`GOOS=wasip1 GOARCH=wasm`).

Every module submitted to Subzero must be packaged as a `.tar.gz` archive adhering to a strict directory hierarchy and security policy. **Any violation of this specification will result in an immediate rejection (*Strict Rejection Policy*).**

---

## 2. Standard Directory Structure

A module's `.tar.gz` archive must conform to the following directory hierarchy:

```text
<module-name>/
├── config.zero          # [REQUIRED] Runtime manifest, metadata & quota limits
├── readme.md            # [REQUIRED] Module documentation, tenant scope & data schema
├── main.go              # [REQUIRED] Primary Go entrypoint (package main, func main)
├── go.mod               # [REQUIRED if importing modules] Go module declaration
├── go.sum               # [OPTIONAL] Third-party dependency checksum verification
├── parsers/             # [OPTIONAL] Internal sub-packages supporting domain logic
│   └── normalizer.go
└── schema/              # [OPTIONAL] Sample data and validation fixtures
    ├── sample.input.csv
    └── expected.output.json
```

---

## 3. `config.zero` Manifest Specification

The `config.zero` file is the **Module Contract Manifest** in Subzero. Located at the root of the archive, it can be formatted as either **YAML / Key-Value** or **JSON**.

### Schema Reference

| Field | Data Type | Requirement | Default | Description |
| :--- | :--- | :--- | :--- | :--- |
| `name` | String | **Required** | - | Unique module identifier (alphanumeric, `-`, `_` only). |
| `version` | String | Optional | `1.0.0` | Semantic version string (*Semantic Versioning*). |
| `tenant` | String | Optional | `default` | Tenant identity owning the module. |
| `target` | String | Optional | `wasip1` | Target Wasm runtime architecture (default: `wasip1`). |
| `entrypoint`| String | Optional | `main.go` | Go source file containing `package main` & `func main()`. |
| `description`| String | Optional | - | Summary of module functionality. |
| `memory_limit_pages` | Integer | Optional | `512` | Maximum RAM quota in Wasm page units (1 page = 64 KB; `512` = 32 MB). |
| `timeout` | Duration | Optional | `10s` | Maximum CPU execution time before forced termination. |

### Sample `config.zero` Formats

#### Option A: YAML / Key-Value (Recommended)
```yaml
# config.zero
name: bca-statement-normalizer
version: 1.0.0
tenant: client-bank-bca
target: wasip1
entrypoint: main.go
description: Normalize Bank BCA bank statement exports into Subzero canonical format
memory_limit_pages: 512
timeout: 15s
```

#### Option B: JSON Format
```json
{
  "name": "bca-statement-normalizer",
  "version": "1.0.0",
  "tenant": "client-bank-bca",
  "target": "wasip1",
  "entrypoint": "main.go",
  "description": "Normalize Bank BCA bank statement exports into Subzero canonical format",
  "memory_limit_pages": 512,
  "timeout": "15s"
}
```

---

## 4. `readme.md` Specification

As a multi-tenant enterprise system, every module **must include clear documentation** in a non-empty `readme.md` (or `README.md`) file.

This documentation must cover:
1. **Business Objective**: Overview of the module and target tenant organization.
2. **Input Format Specifications**:
   * Raw header column names and ordering (e.g. `AccountNo, Currency, GrossBalance, Notes, FullName`).
   * Numerical and date conventions (e.g. comma vs period decimal separators, amounts stored in cents/satoshis).
3. **Canonical Output Format**:
   * Target schema produced by the module (e.g. canonical JSON/CSV with standard fields `account_id, currency, balance, holder_name`).
4. **Edge Case Handling**: Policies for handling empty rows, subtotal summary rows, duplicate headers, or invalid encodings.

---

## 5. Entrypoint `main.go` & Code Modularity

### A. Entrypoint Requirements
* The file specified by `entrypoint` (default: `main.go`) must declare `package main` and implement `func main()`.
* **Data Stream I/O Protocol**:
  * **Input**: Ingests raw input streams directly via Standard Input (`os.Stdin`) or environment variables.
  * **Output**: Writes sanitized, normalized canonical results to Standard Output (`os.Stdout`).
  * **Logging & Errors**: Diagnostic logs and warnings are streamed to Standard Error (`os.Stderr`).

### B. Internal Sub-package Imports
Code does not need to be condensed into a single file. You can organize your module into multiple `.go` files or internal sub-directories:
```go
package main

import (
    "os"
    "my-module/parsers" // Importing internal sub-package within the archive
)

func main() {
    parsers.Process(os.Stdin, os.Stdout)
}
```
The Subzero compiler compiles the **entire package directory**, automatically linking all internal source files.

### C. External Dependencies via `go.mod`
Modules can import third-party Go libraries under the following conditions:
1. The archive includes valid `go.mod` (and `go.sum`) files.
2. All dependencies are **Pure Go** compatible with `GOOS=wasip1 GOARCH=wasm` (no native CGo bindings).

---

## 6. Processing Interface Contract (Subzero Go SDK)

To streamline module development and guarantee predictable execution, Subzero provides the official **`pkg/sdk`** library (`github.com/rachmanzz/subzero/pkg/sdk`).

### A. Two-Phase Contract: `Receive` and `Return`

Every transformation module implements the `sdk.Transformer` interface:

```go
type Transformer interface {
    // Receive ingests and validates the task metadata and raw payload from the host.
    Receive(ctx context.Context, req *sdk.Request) error

    // Return executes data processing and emits canonical results and row counts back to Subzero.
    Return(ctx context.Context) (*sdk.Response, error)
}
```

#### Input Data Structure: `sdk.Request`
```go
type Request struct {
    TaskID       string            // Unique task execution ID
    TenantID     string            // Tenant identifier
    ActionTarget string            // Action target (e.g. "normalize", "parse")
    SourceURI    string            // Source file URI (S3 / local) if applicable
    CustomConfig map[string]string // Dynamic parameters passed via SUBZERO_CFG_*
    Payload      []byte            // Raw input bytes (CSV, JSON, text)
}
```

#### Output Data Structure: `sdk.Response`
```go
type Response struct {
    IsSuccess     bool              // Execution status flag
    ProcessedRows int               // Count of valid records/rows successfully processed
    Data          []byte            // Normalized canonical output bytes
    OutputURI     string            // Destination URI if writing directly to object store
    Metadata      map[string]string // Additional metadata (e.g. schema_version, encoding)
    ErrorMessage  string            // Failure message if processing encountered an error
}
```

### B. Complete Implementation Example: `main.go`

```go
package main

import (
    "bytes"
    "context"
    "encoding/csv"
    "fmt"
    "io"

    "github.com/rachmanzz/subzero/pkg/sdk"
)

type StatementNormalizer struct {
    req  *sdk.Request
    rows [][]string
}

// 1. Receive: Ingests metadata and validates the raw payload
func (n *StatementNormalizer) Receive(ctx context.Context, req *sdk.Request) error {
    if len(req.Payload) == 0 {
        return fmt.Errorf("payload cannot be empty")
    }
    n.req = req

    r := csv.NewReader(bytes.NewReader(req.Payload))
    for {
        record, err := r.Read()
        if err == io.EOF {
            break
        }
        if err != nil {
            return fmt.Errorf("failed to parse CSV row: %w", err)
        }
        n.rows = append(n.rows, record)
    }
    return nil
}

// 2. Return: Emits normalized canonical data and row count metrics
func (n *StatementNormalizer) Return(ctx context.Context) (*sdk.Response, error) {
    var outBuf bytes.Buffer
    w := csv.NewWriter(&outBuf)

    // Write standard canonical Subzero header
    _ = w.Write([]string{"tenant_id", "row_index", "raw_content"})

    for i, row := range n.rows {
        _ = w.Write([]string{n.req.TenantID, fmt.Sprintf("%d", i+1), fmt.Sprintf("%v", row)})
    }
    w.Flush()

    return &sdk.Response{
        IsSuccess:     true,
        ProcessedRows: len(n.rows),
        Data:          outBuf.Bytes(),
        Metadata: map[string]string{
            "schema_version": "v1.0",
        },
    }, nil
}

func main() {
    // Run transformer via Subzero SDK runtime
    sdk.Run(&StatementNormalizer{})
}
```

> **Functional Alternative:** You can also register a one-line functional handler:
> ```go
> sdk.Handle(func(ctx context.Context, req *sdk.Request) (*sdk.Response, error) {
>     return &sdk.Response{IsSuccess: true, ProcessedRows: 1, Data: req.Payload}, nil
> })
> ```

---

## 7. Security Model & Static AST Auditing

Prior to compilation, Subzero subjects the module codebase to AST (*Abstract Syntax Tree Static Analysis*) scanning to identify security threats and policy violations:

### Forbidden Imports
| Forbidden Import | Rejection Rationale |
| :--- | :--- |
| `os/exec` | Prevents operating system shell invocation and remote command execution. |
| `syscall` | Prevents direct kernel trap calls and low-level process manipulation. |
| `unsafe` | Prevents unrestricted arbitrary memory pointer manipulation outside Wasm sandbox bounds. |
| `plugin` | Prevents runtime loading of unverified native dynamic libraries (`.so`). |
| `runtime/cgo` | Prevents invocation of un-sandboxed native C binaries. |

### Network & Shell Vector Rules
* **Network Server Listeners**: Calls to `net.Listen` or `net.ListenPacket` are strictly rejected to prevent worker processes from opening rogue backdoor listeners.
* **Suspicious Shell Literals**: Dangerous shell strings such as `/bin/sh`, `/bin/bash`, `cmd.exe`, or `powershell` combined with system invocations trigger instant rejection.

### Archive Integrity Rules
* **Zip-Slip Protection**: File paths within the archive must not contain directory traversal sequences (e.g. `../../etc/passwd`).
* **Decompression Bomb Protection**: Uncompressed payload size is capped at **25 MB** and total entry count is limited to **50 files**.

---

## 8. System Rejection Matrix (*Rejection Policy*)

| Violation | HTTP Status | Response Payload |
| :--- | :--- | :--- |
| File format is not `.tar.gz` / `.tgz` | `422 Unprocessable Entity` | `{"stage": "structure_verification", "error": "uploaded file must be a .tar.gz bundle..."}` |
| `config.zero` file missing or empty | `422 Unprocessable Entity` | `{"stage": "structure_verification", "error": "structure: package must contain config.zero..."}` |
| `readme.md` file missing or empty | `422 Unprocessable Entity` | `{"stage": "structure_verification", "error": "structure: package must contain readme.md..."}` |
| `main.go` lacks `package main` / `func main()` | `422 Unprocessable Entity` | `{"stage": "structure_verification", "error": "structure: entrypoint does not contain package main..."}` |
| Forbidden import or backdoor detected | `422 Unprocessable Entity` | `{"stage": "security_audit", "error": "Security validation failed...", "audit": {...}}` |

---

## 9. Developer Workflow via `subzero-cli`

Subzero includes an integrated CLI tool to streamline the development lifecycle:

### 1. Initialize New Module Scaffold
```bash
subzero-cli module init --name currency-normalizer --tenant acme-bank ./currency-normalizer
```
This generates the complete standard structure:
* `config.zero` populated with manifest defaults.
* `readme.md` populated with documentation template.
* `main.go` implementing the `sdk.Transformer` interface (`Receive` and `Return`).
* `go.mod` initialized.

### 2. Audit Structure & Security Locally
```bash
subzero-cli module audit ./currency-normalizer
```
Validates directory structure and runs static AST scanning locally without uploading to the server.

### 3. Package for Distribution
```bash
subzero-cli module pack -o currency-normalizer.tar.gz ./currency-normalizer
```
Packs the project into a sanitized `.tar.gz` archive ready for upload.

### 4. Publish to Subzero Server
```bash
subzero-cli module publish --server http://localhost:8080 --tenant acme-bank --name currency-normalizer ./currency-normalizer
```
The server will verify package structure, audit AST security, compile to WebAssembly bytecode, and register the module into the active registry.
