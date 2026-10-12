package local

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rachmanzz/subzero/internal/storage"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

const Scheme = "file"

func init() {
	storage.Register(New(os.TempDir()))
}

// Driver implements storage.Driver using the local filesystem.
type Driver struct {
	baseDir string
}

// New creates a new local filesystem storage driver rooted at baseDir.
func New(baseDir string) *Driver {
	_ = os.MkdirAll(baseDir, 0755)
	return &Driver{baseDir: baseDir}
}

func (d *Driver) Scheme() string {
	return Scheme
}

// resolvePath translates storageRef (e.g. "file:///tmp/doc.xlsx" or "tenant/doc.xlsx") to an absolute path.
func (d *Driver) resolvePath(storageRef string) string {
	cleaned := strings.TrimPrefix(storageRef, "file://")
	if filepath.IsAbs(cleaned) {
		return cleaned
	}
	return filepath.Join(d.baseDir, cleaned)
}

// GenerateUploadSlot returns an upload slot representation for the local target.
func (d *Driver) GenerateUploadSlot(ctx context.Context, storageRef string, ttl time.Duration) (*protocol.UploadSlotResponse, error) {
	targetPath := d.resolvePath(storageRef)
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("storage/local: failed to create parent dir: %w", err)
	}

	return &protocol.UploadSlotResponse{
		TargetURL:        fmt.Sprintf("file://%s", targetPath),
		HTTPMethod:       http.MethodPut,
		StorageReference: storageRef,
		ExpiredAt:        time.Now().Add(ttl),
	}, nil
}

// StreamRead returns an io.ReadCloser directly streaming from the local file.
func (d *Driver) StreamRead(ctx context.Context, sourceURI string) (io.ReadCloser, error) {
	targetPath := d.resolvePath(sourceURI)
	f, err := os.Open(targetPath)
	if err != nil {
		return nil, fmt.Errorf("storage/local: open %q failed: %w", targetPath, err)
	}
	return f, nil
}

// PutObject writes content directly to the target file.
func (d *Driver) PutObject(ctx context.Context, storageRef string, r io.Reader, size int64) error {
	targetPath := d.resolvePath(storageRef)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return fmt.Errorf("storage/local: create dir failed: %w", err)
	}

	f, err := os.Create(targetPath)
	if err != nil {
		return fmt.Errorf("storage/local: create file %q failed: %w", targetPath, err)
	}
	defer f.Close()

	if _, err := io.Copy(f, r); err != nil {
		return fmt.Errorf("storage/local: write error: %w", err)
	}
	return nil
}

// DeleteObject removes the specified file.
func (d *Driver) DeleteObject(ctx context.Context, storageRef string) error {
	targetPath := d.resolvePath(storageRef)
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("storage/local: remove %q failed: %w", targetPath, err)
	}
	return nil
}
