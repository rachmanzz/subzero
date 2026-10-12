package storage

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/rachmanzz/subzero/pkg/protocol"
)

// Driver defines the common contract for object storage providers (AWS S3, MinIO, GCS, Local).
type Driver interface {
	// Scheme returns the storage scheme identifier (e.g. "s3", "minio", "gcs", "file")
	Scheme() string

	// GenerateUploadSlot creates a pre-signed upload URL for direct browser/client upload.
	GenerateUploadSlot(ctx context.Context, storageRef string, ttl time.Duration) (*protocol.UploadSlotResponse, error)

	// StreamRead returns a stream reader directly from the storage without buffering the whole file to disk.
	StreamRead(ctx context.Context, sourceURI string) (io.ReadCloser, error)

	// PutObject stores an object into the storage (primarily used in local dev, testing, or worker output).
	PutObject(ctx context.Context, storageRef string, r io.Reader, size int64) error

	// DeleteObject removes an object by storage reference.
	DeleteObject(ctx context.Context, storageRef string) error
}

var (
	registryMu sync.RWMutex
	drivers    = make(map[string]Driver)
)

// Register registers a storage driver under its scheme.
func Register(d Driver) {
	registryMu.Lock()
	defer registryMu.Unlock()
	drivers[d.Scheme()] = d
}

// Get returns the registered storage driver for a given scheme.
func Get(scheme string) (Driver, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	d, ok := drivers[scheme]
	if !ok {
		return nil, fmt.Errorf("storage: unsupported scheme %q", scheme)
	}
	return d, nil
}
