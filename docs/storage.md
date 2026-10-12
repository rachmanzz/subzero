# Storage Drivers & Direct Ingestion

Subzero operates on a strict **"Zero Payload in Control Plane"** architectural principle. This document explains how large binary documents (e.g. 100MB+ Excel spreadsheets) are uploaded and consumed without passing through the central API gateway or consuming memory on the control plane.

---

## 1. Zero Payload in Control Plane

In traditional monolithic architectures, spreadsheet uploads pass directly through the API server:
```text
[ Browser ] ──(Upload 100MB)──► [ API Server (RAM Bloat) ] ──► [ Worker ]
```
This causes heavy memory consumption, spikes in garbage collection pauses, and potential Out-Of-Memory (OOM) crashes across all tenants.

### Subzero Direct Ingestion Flow:
```text
1. Client requests upload slot ──► [ Subzero Producer ]
                                           │
2. Returns Pre-signed URL ◄────────────────┘
        │
3. Client uploads file DIRECTLY
        ▼
   [ Cloud Object Storage (S3 / MinIO / GCS) ]
        ▲
4. Worker streams bytes DIRECTLY from Storage ◄── [ Subzero Worker ]
```

At no point does the raw document payload enter the Producer or Supervisor memory space. The control plane only handles metadata references (`s3://bucket/key` or `file:///path`).

---

## 2. The `Driver` Interface

Located in [`internal/storage/storage.go`](../internal/storage/storage.go):

```go
type Driver interface {
    // Scheme returns the storage scheme identifier (e.g. "s3", "minio", "gcs", "file")
    Scheme() string

    // GenerateUploadSlot creates a pre-signed upload URL for direct browser/client upload.
    GenerateUploadSlot(ctx context.Context, storageRef string, ttl time.Duration) (*protocol.UploadSlotResponse, error)

    // StreamRead returns a stream reader directly from storage without buffering to disk.
    StreamRead(ctx context.Context, sourceURI string) (io.ReadCloser, error)

    // PutObject stores an object (useful for testing and worker output).
    PutObject(ctx context.Context, storageRef string, r io.Reader, size int64) error

    // DeleteObject removes an object by storage reference.
    DeleteObject(ctx context.Context, storageRef string) error
}
```

---

## 3. Streaming Readers vs. Disk Caching

When a worker processes a document:
1. It requests `StreamRead(ctx, sourceURI)`.
2. The storage driver returns an `io.ReadCloser`.
3. The parser consumes rows sequentially via streaming chunks (e.g., streaming XML parser for XLSX), discarding processed rows immediately.
4. **Result**: A 500,000-row spreadsheet can be parsed with a flat, predictable memory profile (< 64MB RAM) instead of consuming gigabytes on disk or heap.

---

## 4. Local Filesystem Driver (`scheme: "file"`)

* **Implementation**: [`internal/storage/local/local.go`](../internal/storage/local/local.go)
* **Purpose**: Provides full offline and test environment functionality.
* **Behavior**:
  * `GenerateUploadSlot`: Generates a `file://` URI with target directory creation.
  * `StreamRead`: Direct `os.Open` stream reader.
  * `PutObject`: Direct streaming file creation via `io.Copy`.
