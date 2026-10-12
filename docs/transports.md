# Network Transports & Pluggable Adapters

Subzero is decoupled from the underlying network infrastructure through its pluggable `Transport` driver architecture. This allows workloads to run on a single machine, across distributed clouds, or entirely in-memory for testing, without altering protocol or business code.

---

## 1. The `Transport` Interface

Located in [`pkg/transport/transport.go`](../pkg/transport/transport.go), every transport driver implements:

```go
type Transport interface {
    // Scheme returns the unique identifier for the transport.
    Scheme() string

    // Listen creates a network listener for receiving framed connections.
    Listen(ctx context.Context, endpoint string) (net.Listener, error)

    // Dial establishes a network connection to the target endpoint.
    Dial(ctx context.Context, endpoint string) (net.Conn, error)
}
```

Drivers register themselves into the global registry during initialization via `transport.Register(driver)`.

---

## 2. Available Transport Adapters

### 2.1 Local Fast IPC (`scheme: "unix"`, alias `"ipc"`)
* **Implementation**: [`pkg/transport/ipc/unix.go`](../pkg/transport/ipc/unix.go)
* **Underlying Mechanism**: POSIX Unix Domain Sockets (`net.Listen("unix", ...)`).
* **Use Case**: Single-node server deployments, multi-process worker pools on localhost.
* **Key Advantages**:
  * Bypasses the entire TCP/IP network stack (routing, checksums, loopback latency).
  * Eliminates TCP port collision issues.
  * Secured via filesystem permissions (`0660`).
  * Automatic socket unlinking upon server shutdown (`cleanupListener`).

### 2.2 Cross-Node Host-to-Host Stream (`scheme: "tcp"`)
* **Implementation**: [`pkg/transport/tcp/tcp.go`](../pkg/transport/tcp/tcp.go) & [`tls_config.go`](../pkg/transport/tcp/tls_config.go)
* **Underlying Mechanism**: Raw TCP connections with optional TLS 1.3 mutual authentication (mTLS).
* **Use Case**: Multi-node clusters, private VPC clouds, distributed supervisor-to-worker topologies.
* **Security**:
  * Server enforces client certificates (`tls.RequireAndVerifyClientCert`) when a client CA pool is specified.
  * Direct binary wire frames travel over an encrypted transport without HTTP envelope overhead.

### 2.3 HTTP Streaming Transport (`scheme: "http"`)
* **Implementation**: [`pkg/transport/http/http.go`](../pkg/transport/http/http.go)
* **Underlying Mechanism**: Standard **HTTP 101 Switching Protocols** (HTTP Upgrade) via `http.Hijacker`.
* **Use Case**: Environments where traffic must traverse strict HTTP reverse proxies, Ingress Controllers, or CDNs (such as NGINX, Traefik, Envoy, or Cloudflare) that block raw TCP ports.
* **How It Works**:
  1. The client sends `GET /subzero/stream HTTP/1.1` with `Upgrade: subzero`.
  2. The server hijacks the HTTP connection, writes `HTTP/1.1 101 Switching Protocols`, and wraps the raw underlying socket into a standard full-duplex `net.Conn`.
  3. Binary wire frames then stream continuously in both directions.

### 2.4 In-Memory Pipe (`scheme: "inproc"`)
* **Implementation**: [`pkg/transport/inproc/pipe.go`](../pkg/transport/inproc/pipe.go)
* **Underlying Mechanism**: Go standard library `net.Pipe()`.
* **Use Case**: Unit tests, integration test suites, benchmarks, and embedded Wasm worker coupling.
* **Key Advantages**:
  * Zero operating system overhead (runs entirely within process heap memory).
  * No open ports or filesystem artifacts.
  * Zero latency (instantaneous packet delivery via in-memory channels).

---

## 3. Comparison Matrix

| Driver | Latency | Encryption | OS Compatibility | Deployment Scenario |
| :--- | :--- | :--- | :--- | :--- |
| **`unix` / `ipc`** | Sub-millisecond (< 0.1ms) | OS Permissions | Linux, macOS, BSD | Single VM, local daemon |
| **`tcp`** | Sub-millisecond (LAN) | mTLS (TLS 1.3) | Cross-platform | Distributed cluster, VPC |
| **`http`** | Low (~1ms) | TLS / HTTPS | Cross-platform | Ingress, API Gateway, CDN |
| **`inproc`** | Instant (< 0.01ms) | N/A (Memory) | Cross-platform | Testing, Embedded runtimes |
