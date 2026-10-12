package storage_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/storage"
	"github.com/rachmanzz/subzero/internal/storage/local"
)

func TestLocalStorageDriver(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "subzero-storage-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	d := local.New(tmpDir)
	storage.Register(d)

	ctx := context.Background()
	slot, err := d.GenerateUploadSlot(ctx, "tenant1/report.csv", 10*time.Minute)
	if err != nil {
		t.Fatalf("GenerateUploadSlot failed: %v", err)
	}
	if slot.TargetURL == "" || slot.HTTPMethod != "PUT" {
		t.Fatalf("invalid slot response: %+v", slot)
	}

	// Put an object
	content := []byte("id,name,value\n1,alpha,100\n2,beta,200\n")
	err = d.PutObject(ctx, "tenant1/report.csv", bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("PutObject failed: %v", err)
	}

	// StreamRead the object
	rc, err := d.StreamRead(ctx, filepath.Join(tmpDir, "tenant1/report.csv"))
	if err != nil {
		t.Fatalf("StreamRead failed: %v", err)
	}
	defer rc.Close()

	readData, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll failed: %v", err)
	}

	if !bytes.Equal(readData, content) {
		t.Fatalf("content mismatch: expected %q, got %q", string(content), string(readData))
	}

	// Delete
	if err := d.DeleteObject(ctx, "tenant1/report.csv"); err != nil {
		t.Fatalf("DeleteObject failed: %v", err)
	}
}
