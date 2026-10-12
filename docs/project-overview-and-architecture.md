# Subzero: Architectural Overview, Problem Space & Core Mechanics

This document provides a comprehensive technical overview of **Subzero**, explaining the underlying business challenges it addresses, the end-to-end execution pipeline, the internal mechanics of the `v3/worker` concurrency pattern, the sandboxed worker runtime, capability negotiation, and security isolation.

---

## 1. Project Introduction & The B2B Problem Space

### 1.1 What is Subzero?
**Subzero** is an ultra-lightweight, high-performance, and secure background data computing engine written in Go. It is purpose-built for multi-tenant B2B platforms that must ingest, validate, sanitize, and transform heterogeneous customer data (spreadsheets, documents, feeds, and exports) into canonical database schemas without server bloat, memory exhaustion, or security compromises.

### 1.2 The Core B2B Business Challenge
In modern B2B SaaS and enterprise ecosystems, external partners, vendors, and clients inevitably submit data in disparate, unstandardized formats:

1. **Format Fragmentation**:
   - **Spreadsheets (`.xlsx`, `.csv`)**: Bank Alpha formats dates as `DD/MM/YYYY` with dot-separated currencies; FinTech Beta submits `YYYY-MM-DD` with custom header offsets on row 5; Logistics Gamma submits multi-sheet workbooks with varying column ordering.
   - **Documents & Semi-Structured Data (`.docx`, `.pdf`, `.xml`, `.json`)**: Every business partner enforces distinct schemas and structures that must be extracted and normalized before persisting into internal relational databases.

2. **The "Fat Payload" API Dilemma**:
   - When a standard API Gateway attempts to parse a 200 MB or 1,000,000-row file in memory, memory consumption surges, Garbage Collection pauses degrade gateway responsiveness, and the system risks Out-Of-Memory (OOM) crashes affecting all tenants.

3. **The Multi-Tenant Security Risk**:
   - Allowing tenants to customize parsing scripts or upload transformation logic often introduces severe attack vectors:
     - Remote Code Execution (RCE) via malicious script injection.
     - Filesystem traversal (reading `/etc/passwd` or neighboring tenant directories).
     - Network exfiltration (sending internal data to external command-and-control servers).
     - Database credential theft if guest modules are granted direct connection strings.

4. **Resource Waste in Traditional Worker Queues**:
   - Traditional background workers (Celery, Sidekiq, daemon pools) remain resident in memory 24/7, consuming infrastructure cost even during prolonged idle periods.

---

## 2. How Subzero Solves the Problem: The End-to-End Pipeline

Subzero addresses these problems through a decoupled, multi-stage architecture governed by three foundational design principles:
1. **Zero Payload in Control Plane**: API servers and supervisors handle only lightweight references, never the physical raw data.
2. **Zero-Disk Streaming**: Raw bytes flow directly from storage to the memory sandbox without touching local worker disks.
3. **Scale-to-Zero & Sandboxed Multi-Tenancy**: Workers spin up on demand in isolated WebAssembly runtimes and terminate when idle.

```
[ B2B Client / Frontend ]
          │
          ├─── 1. Request Upload Slot ────► [ Main API / Subzero Producer ]
          │                                         │
          │◄── 2. Ephemeral Presigned URL ──────────┤ (TTL: 15 Minutes)
          │                                         │
          ├─── 3. Direct HTTP PUT ─────────┐        ▼
          │                                │   [ Subzero Supervisor ]
          ▼                                │   ┌─────────────────────────────────────┐
  [ Cloud Object Storage ] ◄───────────────┘   │ - Task Dispatcher (v3/worker)       │
  (AWS S3 / MinIO / Local)                     │ - Throttling Semaphore Pool         │
          ▲                                    │ - OS / Container Spawner            │
          │                                    └──────────────────┬──────────────────┘
          │                                                       │ 4. Cold-Start / Dispatch
          │                                                       ▼ (Framed TaskMessage)
          └──────── 5. Zero-Disk Stream Read ──────────── [ Subzero Worker ]
                                                          ┌───────────────────────────────────┐
                                                          │ - Idle Watchdog (Scale-to-Zero)   │
                                                          │ - Wazero WebAssembly Sandbox      │
                                                          │   • Memory Limit Enforced         │
                                                          │   • AST-Audited Pure Logic        │
                                                          │   • Canonical Normalization       │
                                                          └──────────────────┬────────────────┘
                                                                             │
                                                                             ▼ 6. ResultMessage
                                                          [ Main DB / Canonical Storage ]
```

### Execution Steps:
1. **Upload Slot Allocation**: The client requests an upload slot from the Main API Gateway via `pkg/producer`.
2. **Ephemeral Presigned URL**: The storage subsystem generates an ephemeral pre-signed URL with a strict maximum TTL of 15 minutes (`storage.MaxEphemeralTTL`).
3. **Direct Binary Upload**: The client uploads the raw document directly to Object Storage (S3/MinIO). The API Gateway's RAM remains completely untouched.
4. **Task Dispatch**: The API Gateway transmits a lightweight `TaskMessage` containing cryptographic HMAC signatures and metadata references (`s3://bucket/key`) to the Subzero Supervisor.
5. **Zero-Disk Streaming**: The Supervisor dispatches the job to a worker. The worker initiates an HTTP `io.ReadCloser` stream from Object Storage and pipes it directly into the execution engine's standard input (`Stdin`).
6. **Sandboxed Normalization**: The tenant's dedicated WebAssembly module parses the raw stream, applies business rules, and outputs canonical JSON data ready for database insertion.

---

## 3. The Orchestration Layer & The `v3/worker` Concurrency Pattern

At the core of the Subzero Supervisor daemon ([`internal/supervisor/dispatcher.go`](../internal/supervisor/dispatcher.go)) lies the **`v3/worker` pattern**, adapted from the high-throughput `fiber-extras` ecosystem.

The `v3/worker` engine solves the challenge of high-burst traffic, panic safety, and parallel task distribution:

```
                  ┌─────────────────────────────────────────────────────────┐
                  │                  v3/worker Dispatcher                   │
Task Ingress ────►│                                                         │
                  │  1. Throttling Semaphore (Configurable Concurrency)    │
                  │     [ Slot 1 ] [ Slot 2 ] ... [ Slot N ]                │
                  │                                                         │
                  │  2. Panic Recovery Guard (Deferred Stack Trace Catch)   │
                  │                                                         │
                  │  3. SubWorkerPool (Parallel Fan-Out Execution)          │
                  │     ├── Sub-Worker A (Sheet 1 / Rows 1-50k)             │
                  │     ├── Sub-Worker B (Sheet 2 / Rows 50k-100k)          │
                  │     └── Sub-Worker C (Sheet 3 / Rows 100k-150k)         │
                  └─────────────────────────────────────────────────────────┘
```

### 3.1 Throttling Semaphore (Preventing Server Overload)
Without concurrency controls, receiving 100 simultaneous 500 MB transformation requests would trigger 100 parallel workers, exhausting host RAM and causing kernel OOM panics.
- The `Dispatcher` implements a channel-based semaphore (`sem chan struct{}`) bounded by `Concurrency` (e.g., 16).
- Incoming tasks acquire an execution token before execution. If all slots are occupied, subsequent tasks queue gracefully until a running job completes.

### 3.2 Panic Recovery Guard & Blast-Radius Containment
User-defined or third-party transformation logic is susceptible to runtime exceptions (nil pointer dereferences, array index out of bounds).
- In Subzero, every task execution is wrapped in a deferred recovery handler:
  ```go
  defer func() {
      if r := recover(); r != nil {
          err = fmt.Errorf("panic recovered during task execution: %v\n%s", r, debug.Stack())
      }
  }()
  ```
- If Tenant Alpha's module encounters a panic, it is safely captured and reported as a failed `ResultMessage`. **The Supervisor daemon never crashes**, and sibling tasks running in other slots continue completely unaffected.

### 3.3 `SubWorkerPool` Fan-Out (Parallel Multi-Sheet / Large Dataset Processing)
For massive workbooks or high-volume datasets, the `v3/worker` pattern provides the [`SubWorkerPool`](../internal/supervisor/dispatcher.go) abstraction:
```go
type SubWorkerPool interface {
    Go(fn func(ctx context.Context) error)
    Wait() error
}
```
- A parent task can partition a million-row workbook across sheets or row intervals via [`internal/supervisor/chunker.go`](../internal/supervisor/chunker.go).
- Each chunk executes in parallel across CPU cores.
- **Fail-Fast Cancellation**: If any sub-worker encounters an unrecoverable business rule violation or corrupted row, `Wait()` immediately cancels the parent context, terminating sibling sub-workers and conserving compute cycles.

---

## 4. The Sandboxed Worker & Scale-to-Zero Engine

Workers in Subzero ([`cmd/subzero-worker`](../cmd/subzero-worker/main.go)) operate as standalone, on-demand execution nodes managed by the Supervisor.

### 4.1 The Scale-to-Zero State Machine
To eliminate idle infrastructure costs, workers implement an automated lifecycle driven by the [`Watchdog`](../internal/worker/watchdog.go):

```text
[ DORMANT ] (Worker process does not exist; 0 RAM / 0 CPU consumption)
     │
     │ 1. Task queued ──► Supervisor triggers Cold-Start (sub-100ms)
     ▼
[ PROVISIONING ] (Worker initializes, opens IPC socket / TCP port)
     │
     │ 2. Ready probe succeeds
     ▼
[ EXECUTING TASK ] (Worker processes data stream in Wazero sandbox)
     │   ▲
     │   │ New task arrives (Watchdog timer resets)
     ▼   │
[ WARM STANDBY ] (Idle timer countdown begins, e.g., 30 seconds)
     │
     │ 3. Idle timeout expires with zero traffic
     ▼
[ GRACEFUL TERMINATION ] ──► Worker exits cleanly (State returns to DORMANT)
```

1. **Dormant**: By default, no worker processes run. Infrastructure utilization is zero.
2. **Cold-Start Provisioning**: When a task arrives, the Supervisor spawner launches the worker. Ready checks succeed within milliseconds.
3. **Warm Standby**: If batch workloads or repeated uploads occur, the worker remains warm, executing subsequent tasks with zero startup overhead.
4. **Auto-Termination**: When no new jobs arrive within the configured `idle-timeout`, the worker terminates itself cleanly (`os.Exit(0)`).

### 4.2 Spawner Modes: Native Process vs Containerized
The Supervisor supports two interchangeable spawner mechanisms ([`internal/supervisor/spawner.go`](../internal/supervisor/spawner.go) and [`internal/supervisor/container_spawner.go`](../internal/supervisor/container_spawner.go)):

| Spawner Mode | Underlying Mechanism | Best For | Cold-Start Overhead |
| :--- | :--- | :--- | :--- |
| **Native Process (`-spawner=process`)** | Spawns `subzero-worker` executable via `os/exec` over local Unix Domain Socket (`.sock`). | Bare-metal, virtual machines, local edge instances. | **< 15 milliseconds** |
| **Containerized (`-spawner=container`)** | Spawns worker containers (`subzero-worker:latest`) via Docker / Podman CLI with volume-mounted sockets or TCP ports. | Cloud environments requiring kernel-level namespace/cgroup isolation. | **50 - 150 milliseconds** |

---

## 5. WebAssembly (Wazero) Sandbox Deep-Dive

When a worker processes data, it does so within an isolated WebAssembly sandbox powered by **Wazero** ([`internal/runtime/wasm/engine.go`](../internal/runtime/wasm/engine.go)). Wazero is a pure-Go WebAssembly runtime requiring **zero CGo dependencies**.

### 5.1 Strict Memory Limiting & Quota Enforcement
Tenant code cannot allocate unbounded memory. The runtime enforces strict memory page bounds:
- Guest modules are initialized with configured maximum memory limits (e.g., 32 MB or 64 MB).
- If tenant transformation code attempts an infinite loop allocation or encounters an oversized data bomb, the Wazero engine immediately traps the execution:
  ```go
  err == wasm.ErrMemoryLimitExceeded
  ```
- The memory allocation is rejected, the guest module halts, and the host worker remains stable.

### 5.2 Controlled Host Calls (`hostcalls`)
Guest WebAssembly code is decoupled from host system calls. It cannot make arbitrary OS calls. Subzero provides registered host functions via [`internal/runtime/wasm/host_module.go`](../internal/runtime/wasm/host_module.go):
- `storage:get_object`: Read approved objects from storage within authorized boundaries.
- `logger:debug`: Emit diagnostic log lines back to the host logger.
- Standard input/output: Pure streaming via WASI `Stdin` and `Stdout`.

---

## 6. Security Architecture & Permission Management

Subzero enforces end-to-end security through multiple layers of cryptographic verification, static code analysis, and capability negotiation.

```
                          ┌────────────────────────────────────────────────────────┐
                          │               SUBZERO SECURITY LAYERS                  │
                          ├────────────────────────────────────────────────────────┤
1. Network Ingress        │ Wire Protocol Framing (HMAC-SHA256 Signature Validation)│
                          ├────────────────────────────────────────────────────────┤
2. Pre-Compilation        │ AST Security Validator (Rejects unsafe, syscall, net)  │
                          ├────────────────────────────────────────────────────────┤
3. Runtime Authorization  │ Two-Sided Capability Negotiation (config.zero ∩ Policy)│
                          ├────────────────────────────────────────────────────────┤
4. Sandboxed Execution    │ Wazero WASI Memory-Bounded Sandbox (No host filesystem)│
                          ├────────────────────────────────────────────────────────┤
5. Data Egress            │ Least-Privilege Persistence (Database handled by Host) │
                          └────────────────────────────────────────────────────────┘
```

### 6.1 Cryptographic Wire Framing
All inter-process communication (Producer $\rightarrow$ Supervisor $\rightarrow$ Worker) is protected by binary wire framing ([`pkg/protocol/framing.go`](../pkg/protocol/framing.go)):
- Every message payload is preceded by a **36-byte header**:
  - `4-byte uint32`: Exact payload length (prevents buffer overflow / unbounded allocations).
  - `32-byte HMAC-SHA256`: Signature calculated using a shared secret key.
- Tampered or corrupted messages are immediately rejected at the transport layer before deserialization.

### 6.2 Pre-Compilation AST Security Auditing
Before any tenant module is compiled by the Management Server ([`cmd/subzero-admin`](../cmd/subzero-admin/main.go)), the source AST is statically inspected ([`pkg/management/validator.go`](../pkg/management/validator.go)):
- **Forbidden Packages**: Imports of `os/exec`, `syscall`, `unsafe`, and unauthorized runtime internals are blocked.
- **Forbidden Network Calls**: Calls attempting to open listening sockets (`net.Listen`, `net.ListenPacket`) are immediately rejected.
- **Archive Protection**: The module packager ([`pkg/management/archive.go`](../pkg/management/archive.go)) actively enforces Zip-Slip path sanitization and decompression bomb ratio limits.

### 6.3 Two-Sided Capability Negotiation
A tenant's WebAssembly module includes a manifest file, [`config.zero`](../examples/wasm-excel-transformer/config.zero), declaring requested permissions:
```json
{
  "module_name": "acme-excel-normalizer",
  "requested_permissions": {
    "storage_read": ["tenant-acme/*"],
    "storage_write": ["tenant-acme/normalized/*"],
    "allowed_extensions": [".xlsx", ".csv"]
  }
}
```
The Subzero host enforces a master `WorkerPolicy`:
```json
{
  "max_allowed_prefixes": ["tenant-acme/*"],
  "allow_write": true
}
```
The [`Evaluator`](../pkg/permission/evaluator.go) calculates the intersection:
$$\text{Effective Permissions} = \text{ModuleRequest} \cap \text{WorkerPolicy}$$
If a tenant requests access outside their tenant namespace (e.g., `tenant-other/*`), the evaluator denies execution before the module ever launches.

### 6.4 How Database Access Works (Zero Credential Exposure)
A frequent security concern in multi-tenant background computing is: *How does guest code access the database?*

In Subzero: **Tenant modules NEVER receive direct database connection strings.**
1. Allowing tenant code to establish raw TCP connections to PostgreSQL/MySQL exposes connection credentials and risks cross-tenant SQL injection.
2. Instead, **the tenant module acts purely as a deterministic transformer**:
   - **Input**: Raw document stream.
   - **Output**: Clean, validated, canonical JSON records.
3. The trusted host application (API Gateway or Supervisor) takes the validated canonical output and executes the parameterized database insertion using its own secure connection pool.

---

## 7. Comparative Analysis: Subzero vs Traditional Solutions

| Feature | Traditional Worker Queues (Celery, Sidekiq) | AWS Lambda / Serverless Functions | Subzero |
| :--- | :--- | :--- | :--- |
| **Idle Infrastructure Cost** | High (Workers run 24/7 consuming RAM/CPU) | Zero (Scale-to-Zero) | **Zero (Native Scale-to-Zero via Watchdog)** |
| **Cold-Start Latency** | N/A (Always running) | 200 ms - 2,000 ms (VPC / Container initialization) | **< 15 ms (Native) / < 100 ms (Container)** |
| **Payload in Control Plane** | High (Payloads often sent through Redis/RabbitMQ) | Limited (Payload size limits, e.g., 6 MB) | **Zero Payload (Ephemeral Direct S3 Streaming)** |
| **Tenant Isolation** | None (Tenant code runs directly inside host process) | Container boundary (Strong, but heavyweight) | **WebAssembly (Wazero) Memory Sandbox + Strict AST Audit** |
| **Large File Handling** | Requires saving files to local worker disk (`/tmp`) | Limited by ephemeral disk quotas | **Zero-Disk Streaming (`io.ReadCloser` direct pipe)** |
| **Infrastructure Footprint** | Heavy Python/Ruby/Node runtimes | Vendor lock-in (Cloud provider specific) | **Single compiled Go binary, zero CGo dependencies** |

---

## 8. Summary

Subzero provides a robust, production-grade foundation for B2B data integration. By uniting **Direct Storage Ingestion**, the **`v3/worker` Concurrency Pattern**, **Scale-to-Zero Watchdog Lifecycles**, and **Wazero WebAssembly Sandboxing**, it enables enterprises to accept, normalize, and persist unpredictable multi-tenant documents with high throughput, minimal operational costs, and airtight security.
