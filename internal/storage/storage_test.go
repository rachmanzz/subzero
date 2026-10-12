package storage_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/storage"
	"github.com/rachmanzz/subzero/internal/storage/local"
	"github.com/rachmanzz/subzero/pkg/protocol"
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

	// Test ReadAllStream helper
	fullURI := "file://" + filepath.Join(tmpDir, "tenant1/report.csv")
	helperBytes, err := storage.ReadAllStream(ctx, fullURI)
	if err != nil {
		t.Fatalf("ReadAllStream failed: %v", err)
	}
	if !bytes.Equal(helperBytes, content) {
		t.Fatalf("ReadAllStream mismatch: expected %q, got %q", string(content), string(helperBytes))
	}

	// Delete
	if err := d.DeleteObject(ctx, "tenant1/report.csv"); err != nil {
		t.Fatalf("DeleteObject failed: %v", err)
	}
}

func TestEphemeralCredentialsTTLValidation(t *testing.T) {
	// 1. Direct ValidateEphemeralTTL checks
	// TTL <= 0 defaults to 15 minutes
	ttl, err := storage.ValidateEphemeralTTL(0)
	if err != nil || ttl != 15*time.Minute {
		t.Fatalf("expected 15m default for 0, got %v, err: %v", ttl, err)
	}

	ttl, err = storage.ValidateEphemeralTTL(5 * time.Minute)
	if err != nil || ttl != 5*time.Minute {
		t.Fatalf("expected 5m for 5m, got %v, err: %v", ttl, err)
	}

	ttl, err = storage.ValidateEphemeralTTL(15 * time.Minute)
	if err != nil || ttl != 15*time.Minute {
		t.Fatalf("expected 15m for 15m, got %v, err: %v", ttl, err)
	}

	// Exceeding 15 minutes must be rejected per PRD 7
	_, err = storage.ValidateEphemeralTTL(16 * time.Minute)
	if !errors.Is(err, storage.ErrTTLExceeded) {
		t.Fatalf("expected ErrTTLExceeded for 16m, got: %v", err)
	}

	// 2. Driver GenerateUploadSlot enforcement
	tmpDir := t.TempDir()
	d := local.New(tmpDir)

	// Valid 10m slot
	slot, err := d.GenerateUploadSlot(context.Background(), "tenant-a/file.xlsx", 10*time.Minute)
	if err != nil {
		t.Fatalf("GenerateUploadSlot failed for valid 10m TTL: %v", err)
	}
	if slot.IsExpired() {
		t.Errorf("fresh upload slot should not be expired")
	}

	// Reject TTL > 15m
	_, err = d.GenerateUploadSlot(context.Background(), "tenant-a/file.xlsx", 20*time.Minute)
	if !errors.Is(err, storage.ErrTTLExceeded) {
		t.Fatalf("expected ErrTTLExceeded for 20m, got: %v", err)
	}

	// 3. Verify expired slot detection
	expiredSlot := &protocol.UploadSlotResponse{
		TargetURL: "file:///tmp/old.csv",
		ExpiredAt: time.Now().Add(-1 * time.Minute),
	}
	if !expiredSlot.IsExpired() {
		t.Errorf("slot with past ExpiredAt should return IsExpired = true")
	}
}
