package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/rachmanzz/subzero/pkg/protocol"
)

const (
	// MaxEphemeralTTL defines the maximum allowed duration for pre-signed credentials (PRD 7: max 15 minutes).
	MaxEphemeralTTL = 15 * time.Minute
	// DefaultEphemeralTTL is the standard lifetime when unspecified or <= 0.
	DefaultEphemeralTTL = 15 * time.Minute
)

var (
	// ErrTTLExceeded is returned when a requested pre-signed TTL exceeds the 15-minute security limit.
	ErrTTLExceeded = errors.New("storage: presigned credential ttl exceeds maximum limit of 15 minutes (PRD 7)")
	// ErrSlotExpired is returned when attempting to access an expired upload slot or reference.
	ErrSlotExpired = errors.New("storage: presigned upload slot has expired (PRD 7)")
)

// ValidateEphemeralTTL validates and normalizes pre-signed credential TTL against PRD 7 (max 15 minutes).
func ValidateEphemeralTTL(ttl time.Duration) (time.Duration, error) {
	if ttl <= 0 {
		return DefaultEphemeralTTL, nil
	}
	if ttl > MaxEphemeralTTL {
		return 0, fmt.Errorf("%w: requested %v, maximum allowed %v", ErrTTLExceeded, ttl, MaxEphemeralTTL)
	}
	return ttl, nil
}

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

// ResolveDriver extracts the scheme from a storage URI or reference (e.g. "s3://bucket/key" -> "s3",
// "file:///tmp/doc" -> "file", "/tmp/doc" -> "file") and returns the matching Driver.
func ResolveDriver(storageRef string) (Driver, error) {
	scheme := "file"
	if idx := strings.Index(storageRef, "://"); idx != -1 {
		scheme = storageRef[:idx]
	}

	return Get(scheme)
}

// OpenStream resolves the storage driver and opens a direct streaming reader for the given storageRef.
// Callers are responsible for closing the returned io.ReadCloser.
func OpenStream(ctx context.Context, storageRef string) (io.ReadCloser, error) {
	d, err := ResolveDriver(storageRef)
	if err != nil {
		return nil, err
	}
	return d.StreamRead(ctx, storageRef)
}

// ReadAllStream reads the entire stream from storageRef directly into memory without saving to a temporary disk file.
func ReadAllStream(ctx context.Context, storageRef string) ([]byte, error) {
	rc, err := OpenStream(ctx, storageRef)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

