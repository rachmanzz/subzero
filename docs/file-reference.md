# File-by-File Technical Reference

This document provides a comprehensive, file-by-file breakdown of the **Subzero** codebase. For each file, it describes its architectural purpose, key exported symbols, and interaction flows.

---

## 1. Protocol Layer (`pkg/protocol/`)

The protocol package defines the binary communication format, cryptographic integrity verification, and entity schemas exchanged between Producers, Supervisors, and Workers.

### [`pkg/protocol/messages.go`](../pkg/protocol/messages.go)
* **Purpose**: Defines standard data structures exchanged across network boundaries.
* **Key Types**:
  * `TaskMessage`: Encapsulates task parameters sent by the Producer to the Worker (`TaskID`, `TenantID`, `ActionTarget`, `SourceURI`, and `CustomConfig`).
  * `ResultMessage`: Represents the outcome of workload execution (`TaskID`, `IsSuccess`, `ProcessedRows`, `ErrorMessage`, `ExecutionTime`).
  * `UploadSlotResponse`: Returned by the control plane to frontend clients for direct object storage ingestion (`TargetURL`, `HTTPMethod`, `RequiredHeaders`, `StorageReference`, `ExpiredAt`).

### [`pkg/protocol/security.go`](../pkg/protocol/security.go)
* **Purpose**: Provides cryptographic integrity verification using HMAC-SHA256 to ensure messages cannot be forged or tampered with in transit.
* **Key Constants & Functions**:
  * `SignatureSize = 32`: The exact byte size of the HMAC-SHA256 digest.
  * `Sign(payload, secretKey []byte) []byte`: Computes the 32-byte HMAC-SHA256 signature for a given payload.
  * `Verify(payload, signature, secretKey []byte) bool`: Performs a constant-time cryptographic verification (`hmac.Equal`) to guard against timing attacks.

### [`pkg/protocol/framing.go`](../pkg/protocol/framing.go)
* **Purpose**: Implements the wire framing protocol for streaming connections (`io.Reader` and `io.Writer`).
* **Frame Layout**:
  ```text
  [4 Bytes: Uint32 BigEndian Length] [32 Bytes: HMAC-SHA256 Signature] [N Bytes: Payload Body]
  ```
* **Key Functions**:
  * `WriteFrame(w io.Writer, payload, secretKey []byte) error`: Encodes length prefix, computes signature, and writes the frame atomically.
  * `ReadFrame(r io.Reader, secretKey []byte, maxPayloadSize uint32) ([]byte, error)`: Reads the header, validates size against bounds, reads the payload, and validates cryptographic integrity.

### [`pkg/protocol/codec.go`](../pkg/protocol/codec.go)
* **Purpose**: Abstraction for serializing and deserializing message payloads into byte streams.
* **Key Types & Functions**:
  * `Codec`: Interface defining `Marshal(v any) ([]byte, error)` and `Unmarshal(data []byte, v any) error`.
  * `JSONCodec`: Default implementation using standard JSON serialization.
  * `EncodeTask`, `DecodeTask`, `EncodeResult`, `DecodeResult`: High-level convenience helpers.

---

## 2. Transport Layer (`pkg/transport/`)

The transport layer abstracts network connectivity into a pluggable adapter model, allowing Subzero to run over local sockets, cross-node streams, or testing pipes without changing business logic.

### [`pkg/transport/transport.go`](../pkg/transport/transport.go)
* **Purpose**: Defines the common interface contract and central registry for all transport drivers.
* **Key Interface**:
  * `Transport`: Defines `Scheme() string`, `Listen(ctx, endpoint) (net.Listener, error)`, and `Dial(ctx, endpoint) (net.Conn, error)`.
  * `Register(t Transport)` & `Get(scheme string) (Transport, error)`: Global driver registry for pluggable resolution.

### [`pkg/transport/ipc/unix.go`](../pkg/transport/ipc/unix.go)
* **Purpose**: Implements **Local Fast IPC** via Unix Domain Sockets (`scheme: "unix"` / `"ipc"`).
* **Key Features**:
  * Directory creation (`0750`) and socket file permission restriction (`0660`).
  * Automatic removal of stale socket files before binding.
  * `cleanupListener`: Automatically unlinks the socket file from the filesystem when `Close()` is called.

### [`pkg/transport/tcp/tcp.go`](../pkg/transport/tcp/tcp.go) & [`tls_config.go`](../pkg/transport/tcp/tls_config.go)
* **Purpose**: Implements cross-node host-to-host TCP transport (`scheme: "tcp"`).
* **Key Features**:
  * Optional TLS 1.3 encryption and mutual authentication (mTLS).
  * `TLSConfig.BuildServerTLS()`: Enforces `RequireAndVerifyClientCert` when a client CA certificate is supplied.
  * `TLSConfig.BuildClientTLS()`: Provides client certificates for authentication against server listeners.

### [`pkg/transport/http/http.go`](../pkg/transport/http/http.go)
* **Purpose**: Implements an optional HTTP streaming transport adapter (`scheme: "http"`).
* **Key Features**:
  * Uses standard **HTTP 101 Switching Protocols (HTTP Upgrade: subzero)** via `http.Hijacker`.
  * Operates through standard HTTP reverse proxies, Ingress Controllers, and load balancers while providing a genuine full-duplex `net.Conn`.

### [`pkg/transport/inproc/pipe.go`](../pkg/transport/inproc/pipe.go)
* **Purpose**: In-memory transport driver (`scheme: "inproc"`) built on `net.Pipe()`.
* **Key Features**:
  * Bypasses OS kernel network stacks completely.
  * Instantaneous spin-up for ultra-fast unit testing, benchmarks, and embedded Wasm coupling.

---

## 3. Producer SDK (`pkg/producer/`)

### [`pkg/producer/client.go`](../pkg/producer/client.go)
* **Purpose**: Public client SDK used by Main API Gateways (e.g., Fiber or Chi backend services) to communicate with Subzero.
* **Key Methods**:
  * `New(cfg Config) (*Client, error)`: Initializes transport dialer, secret keys, and storage drivers.
  * `RequestUploadSlot(ctx, storageRef, ttl) (*protocol.UploadSlotResponse, error)`: Delegates to the storage driver to allocate pre-signed upload URLs.
  * `SubmitTask(ctx, task *protocol.TaskMessage) (*protocol.ResultMessage, error)`: Encodes tasks into framed binary packets, transmits them over the configured transport, and awaits the execution result.

---

## 4. Storage Subsystem (`internal/storage/`)

### [`internal/storage/storage.go`](../internal/storage/storage.go)
* **Purpose**: Pluggable storage abstraction for object storage providers.
* **Key Interface**:
  * `Driver`: Defines `Scheme()`, `GenerateUploadSlot()`, `StreamRead()`, `PutObject()`, and `DeleteObject()`.
  * `Register(d Driver)` & `Get(scheme string) (Driver, error)`: Global driver factory.

### [`internal/storage/local/local.go`](../internal/storage/local/local.go)
* **Purpose**: Local filesystem implementation (`scheme: "file"`) of `storage.Driver`.
* **Key Features**:
  * Used for single-node deployments, offline environments, and CI tests.
  * Implements `StreamRead` via direct `os.Open` without loading whole files into memory.

---

## 5. Supervisor Engine (`internal/supervisor/`)

### [`internal/supervisor/dispatcher.go`](../internal/supervisor/dispatcher.go)
* **Purpose**: Concurrency controller and task dispatcher based on the minimal-dependency `v3/worker` architectural pattern.
* **Key Features**:
  * `Throttling Semaphore`: Restricts maximum concurrent tasks to prevent memory exhaustion.
  * `SubWorkerPool`: Allows dividing massive tasks (e.g. multi-sheet workbooks) into parallel sub-workers (`pool.Go(...)`).
  * `Panic Recovery`: Catches unhandled panics per task, preventing host daemon crashes.
  * `Dispatch` (async non-blocking) and `ExecuteSync` (synchronous wait).

---

## 6. Worker Engine (`internal/worker/`)

### [`internal/worker/watchdog.go`](../internal/worker/watchdog.go)
* **Purpose**: Idle watchdog countdown timer responsible for the **Scale-to-Zero** lifecycle.
* **Key Features**:
  * `ActivityStarted()`: Increments active worker counter and pauses the timer.
  * `ActivityFinished()`: Resets countdown when active tasks drop to zero.
  * `onTimeout`: Invokes graceful termination when the idle window elapses.

### [`internal/worker/worker.go`](../internal/worker/worker.go)
* **Purpose**: Workload execution runtime server.
* **Key Features**:
  * Accepts connections from listeners, decodes wire frames, and routes tasks to registered `HandlerFunc`.
  * Encapsulates timing metrics and returns signed `ResultMessage` frames.
  * Coordinates auto-termination on watchdog timeout.

---

## 7. Management & Wasm Control Plane (`pkg/management/`)

### [`pkg/management/types.go`](../pkg/management/types.go)
* **Purpose**: Entity structures for the control plane (`WasmModule`, `CompileOptions`, `CompileResult`).

### [`pkg/management/compiler.go`](../pkg/management/compiler.go)
* **Purpose**: Pluggable interface for WebAssembly compilers (`Compiler` with `Compile(...)`).

### [`pkg/management/gocompiler.go`](../pkg/management/gocompiler.go)
* **Purpose**: Default Wasm compiler adapter using the native Go toolchain.
* **Compilation Command**:
  ```bash
  GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -ldflags="-s -w" -o output.wasm main.go
  ```

### [`pkg/management/store.go`](../pkg/management/store.go)
* **Purpose**: Pluggable repository interface (`Store`) and filesystem implementation (`FileStore`) for persisting compiled `.wasm` artifacts, sources, and metadata.

### [`pkg/management/server.go`](../pkg/management/server.go)
* **Purpose**: Chi v5 HTTP router and management API handler.
* **Key Features**:
  * Pluggable: exports `Routes() chi.Router` for mounting into existing Chi applications.
  * Exposes endpoints for module listing, compilation, downloads, deletion, and status.

### [`pkg/management/ui/`](../pkg/management/ui/)
* **`dashboard.html`**: Modern, responsive dark-themed dashboard UI for monitoring and direct drag-and-drop Go-to-Wasm compilation.
* **`embed.go`**: Embeds `dashboard.html` into the binary via `//go:embed` for 100% offline operation.

---

## 8. Executable Entrypoints (`cmd/`)

### [`cmd/subzero-supervisor/main.go`](../cmd/subzero-supervisor/main.go)
* Standalone daemon process coordinating worker allocation, throttling, and task routing.

### [`cmd/subzero-worker/main.go`](../cmd/subzero-worker/main.go)
* Standalone scale-to-zero worker process that automatically terminates upon idle timeout.

### [`cmd/subzero-cli/main.go`](../cmd/subzero-cli/main.go)
* Multi-command command-line tool for module management (init, pack, audit, publish, inspect-policy), task submissions, and benchmark execution.

### [`cmd/subzero-admin/main.go`](../cmd/subzero-admin/main.go)
* Standalone HTTP server serving the Management Dashboard and Wasm compilation API.
