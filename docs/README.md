# Subzero Technical & Architectural Documentation

Welcome to the official technical documentation for **Subzero**, a distributed *scale-to-zero* and *sandboxed computing* runtime engine designed for multi-tenant, massive document processing (such as large spreadsheets and Excel files).

---

## 📚 Documentation Index

| Document | Description |
| :--- | :--- |
| [**1. Complete File-by-File Reference (`file-reference.md`)**](file-reference.md) | Exhaustive, file-by-file breakdown of every single file in the codebase, detailing responsibilities, exported types, functions, and dependency flows. |
| [**2. Protocol & Binary Framing (`protocol.md`)**](protocol.md) | Technical specifications of the binary wire framing (4-byte length prefix + 32-byte HMAC-SHA256 signature), message entity schemas, and serialization codecs. |
| [**3. Network Transports & Adapters (`transports.md`)**](transports.md) | The `Transport` interface abstraction, Unix Domain Sockets (`ipc`), Raw TCP with mTLS (`tcp`), HTTP Upgrade streaming (`http`), and In-Memory Pipe (`inproc`). |
| [**4. Storage Drivers & Direct Ingestion (`storage.md`)**](storage.md) | The "Zero Payload in Control Plane" architecture, pre-signed URL generation, streaming readers, and storage provider implementations. |
| [**5. Supervisor, Dispatcher & Worker Lifecycle (`supervisor-and-worker.md`)**](supervisor-and-worker.md) | Internal task dispatcher (`v3/worker` pattern), throttling semaphores, panic recovery, idle watchdog timers, and the scale-to-zero state machine. |
| [**6. API Management, Dashboard & Wasm (`management-and-wasm.md`)**](management-and-wasm.md) | The Chi-based management control plane, embedded dashboard UI, native Go-to-Wasm (`wasip1`) compiler adapter, and module repository. |
| [**7. Dynamic Schema Normalization (`dynamic-data-transformation.md`)**](dynamic-data-transformation.md) | Deep-dive on solving the "Canonical Schema vs Disparate Tenant Formats" problem (e.g. mapping `acdeab` or `adbca` to `abcd`) for Banks, FinTechs, and SaaS clients. |
| [**8. Pluggable Extension Guide (`pluggable-guide.md`)**](pluggable-guide.md) | Step-by-step developer tutorial on creating custom adapters and plugins (custom network transports, storage drivers, and alternative Wasm compilers). |
| [**9. Module Packaging Specification (`module-packaging-specification.md`)**](module-packaging-specification.md) | Official packaging standard for Wasm modules (`.tar.gz`), `config.zero` manifest, `readme.md`, entrypoint rules, AST backdoor auditing, and strict rejection policies. |

---

## 🏛️ System Architecture Topology

```
[ B2B Client / Frontend ]
        │
        ├─── 1. Request Upload Slot ────► [ Main API / Subzero Producer ]
        │                                         │
        │◄── 2. Presigned Upload URL ─────────────┤
        │                                         │
        ├─── 3. Direct Binary Upload ────┐        ▼
        │                                │   [ Subzero Supervisor ]
        ▼                                │   ┌─────────────────────────────────────┐
[ Cloud Object Storage ] ◄───────────────┘   │ - Task Dispatcher (v3/worker pattern)│
(S3 / MinIO / Local)                   │ - Throttling Semaphore Pool         │
        ▲                                    │ - Pluggable Transport (IPC/TCP/HTTP)│
        │                                    └──────────────────┬──────────────────┘
        │                                                       │ 4. Warm / Cold Spawn
        │                                                       │    & Binary Dispatch
        │                                                       ▼
        └──────── 5. Stream Read Data ──────────────── [ Subzero Worker Sandbox ]
                                                       [ Native OS / Wasm Runtime ]
                                                                │
                                                       (Watchdog Timeout)
                                                       (Auto-Terminate / Scale-to-Zero)
```

---

## ⚡ Quick Start Commands

```bash
# Build all executable binaries (supervisor, worker, cli, admin)
make build

# Run all unit and integration tests with data race detection
make test

# Launch the Subzero Management & Wasm Compiler Dashboard
./bin/subzero-admin -port 8080 -data-dir ./data/wasm

# Start a worker with a 30-second idle timeout
./bin/subzero-worker -scheme unix -endpoint /tmp/subzero-worker.sock -idle-timeout 30s

# Trigger a test task execution via CLI
./bin/subzero-cli -scheme unix -endpoint /tmp/subzero-worker.sock -tenant tenant-acme
```
