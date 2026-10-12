# Pluggable Extension Guide: Writing Custom Adapters & Drivers

Subzero is built on the **Adapter / Plugin Pattern**. Every major subsystem exposes a clean, minimal interface contract. This document serves as a developer guide for extending Subzero with custom transport adapters, cloud storage drivers, alternative Wasm compilers, and persistence stores.

---

## 1. Writing a Custom Transport Driver

A transport driver enables Subzero to communicate across alternative networking layers (e.g., WebSockets, QUIC, or custom mesh protocols).

### Step 1: Implement the `transport.Transport` Interface
```go
package mytransport

import (
    "context"
    "net"
    "github.com/rachmanzz/subzero/pkg/transport"
)

const Scheme = "myproto"

func init() {
    // Automatically register the driver upon import
    transport.Register(&MyTransport{})
}

type MyTransport struct{}

func (t *MyTransport) Scheme() string {
    return Scheme
}

func (t *MyTransport) Listen(ctx context.Context, endpoint string) (net.Listener, error) {
    // Return a net.Listener implementation for your protocol
    return createMyListener(endpoint)
}

func (t *MyTransport) Dial(ctx context.Context, endpoint string) (net.Conn, error) {
    // Return a net.Conn connection
    return dialMyConnection(ctx, endpoint)
}
```

### Step 2: Use Your Custom Driver
Simply blank-import your driver package in your application entrypoint:
```go
import _ "myorg/subzero-mytransport"
```
The driver is now immediately available across Producers, Workers, and Supervisors using `scheme="myproto"`.

---

## 2. Writing a Custom Storage Driver

To connect Subzero to Amazon S3, Google Cloud Storage, or MinIO:

### Implement the `storage.Driver` Interface
```go
package s3driver

import (
    "context"
    "io"
    "time"
    "github.com/rachmanzz/subzero/internal/storage"
    "github.com/rachmanzz/subzero/pkg/protocol"
)

type S3Driver struct {
    bucket string
    // s3Client *s3.Client
}

func (d *S3Driver) Scheme() string {
    return "s3"
}

func (d *S3Driver) GenerateUploadSlot(ctx context.Context, storageRef string, ttl time.Duration) (*protocol.UploadSlotResponse, error) {
    // Call s3PresignClient.PutObject(...)
    return &protocol.UploadSlotResponse{
        TargetURL:        presignedURL,
        HTTPMethod:       "PUT",
        StorageReference: storageRef,
        ExpiredAt:        time.Now().Add(ttl),
    }, nil
}

func (d *S3Driver) StreamRead(ctx context.Context, sourceURI string) (io.ReadCloser, error) {
    // Call s3Client.GetObject(...). The Body is an io.ReadCloser.
    resp, err := d.s3Client.GetObject(ctx, &s3.GetObjectInput{...})
    if err != nil {
        return nil, err
    }
    return resp.Body, nil
}

func (d *S3Driver) PutObject(ctx context.Context, storageRef string, r io.Reader, size int64) error {
    // Call s3Client.PutObject(...)
    return nil
}

func (d *S3Driver) DeleteObject(ctx context.Context, storageRef string) error {
    // Call s3Client.DeleteObject(...)
    return nil
}
```

---

## 3. Writing an Alternative Wasm Compiler Adapter

If you want to use **TinyGo** (which produces ultra-compact Wasm binaries of ~100KB instead of ~2MB) or a Docker-based compiler:

### Implement `management.Compiler`
```go
package tinygo

import (
    "bytes"
    "context"
    "os/exec"
    "github.com/rachmanzz/subzero/pkg/management"
)

type TinyGoCompiler struct {
    tinygoBin string
}

func (c *TinyGoCompiler) Name() string {
    return "tinygo"
}

func (c *TinyGoCompiler) Compile(ctx context.Context, sourceCode []byte, opts management.CompileOptions) (*management.CompileResult, error) {
    // Write source to temp file...
    // Execute: tinygo build -target=wasi -o output.wasm main.go
    cmd := exec.CommandContext(ctx, c.tinygoBin, "build", "-target=wasi", "-o", outFile, srcFile)
    var logBuf bytes.Buffer
    cmd.Stdout = &logBuf
    cmd.Stderr = &logBuf

    err := cmd.Run()
    // Read outFile and return *management.CompileResult
    return result, err
}
```

### Plug into the Management Server
```go
server, err := management.NewServer(management.ServerConfig{
    Compiler: &TinyGoCompiler{tinygoBin: "/usr/local/bin/tinygo"},
    Store:    customStore,
})
```

---

## 4. Writing a Custom Module Store

To store compiled Wasm binaries and metadata in **PostgreSQL** or **Redis** instead of the local filesystem:

### Implement `management.Store`
```go
type PostgresStore struct {
    db *sql.DB
}

func (s *PostgresStore) Save(ctx context.Context, mod *management.WasmModule, wasmBytes, srcBytes []byte) error {
    // INSERT INTO wasm_modules (id, tenant_id, wasm_data, source_code, meta) VALUES (...)
    return nil
}

func (s *PostgresStore) List(ctx context.Context, tenantID string) ([]*management.WasmModule, error) {
    // SELECT meta FROM wasm_modules WHERE ...
    return modules, nil
}

func (s *PostgresStore) GetWasmBytes(ctx context.Context, moduleID string) ([]byte, error) {
    // SELECT wasm_data FROM wasm_modules WHERE id = ...
    return wasmBytes, nil
}

func (s *PostgresStore) Delete(ctx context.Context, moduleID string) error {
    // DELETE FROM wasm_modules WHERE id = ...
    return nil
}
```
All components seamlessly integrate without altering any core daemon logic.
