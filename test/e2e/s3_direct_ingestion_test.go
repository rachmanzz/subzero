package e2e_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/storage"
	"github.com/rachmanzz/subzero/internal/storage/s3"
	"github.com/rachmanzz/subzero/pkg/protocol"
)

// mockS3Server provides an in-memory HTTP server compliant with AWS S3 Path-Style protocol.
type mockS3Server struct {
	mu      sync.RWMutex
	objects map[string][]byte
}

func newMockS3Server() (*mockS3Server, *httptest.Server) {
	m := &mockS3Server{
		objects: make(map[string][]byte),
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		switch r.Method {
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			m.mu.Lock()
			m.objects[key] = body
			m.mu.Unlock()
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			m.mu.RLock()
			data, ok := m.objects[key]
			m.mu.RUnlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`))
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(data)
		case http.MethodDelete:
			m.mu.Lock()
			delete(m.objects, key)
			m.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))

	return m, ts
}

// TestS3_DirectIngestion_E2E validates the PRD Sections 5.3 & 7 requirements:
// 1. Ephemeral credential 15-minute expiration limit enforcement
// 2. Pre-signed upload slot allocation (UploadSlotResponse)
// 3. Direct browser/client HTTP PUT upload to S3/MinIO
// 4. Direct stream reading without saving intermediate files to disk
// 5. Zero-disk payload stream integrity verification
func TestS3_DirectIngestion_E2E(t *testing.T) {
	_, s3Server := newMockS3Server()
	defer s3Server.Close()

	bucketName := "tenant-data-vault"
	s3Driver, err := s3.New(s3.Config{
		Bucket:       bucketName,
		Region:       "us-east-1",
		Endpoint:     s3Server.URL,
		AccessKey:    "test-access-key",
		SecretKey:    "test-secret-key",
		UsePathStyle: true,
	})
	if err != nil {
		t.Fatalf("failed to initialize S3 storage driver: %v", err)
	}

	storage.Register(s3Driver)

	ctx := context.Background()

	// =========================================================================
	// 1. Enforce Ephemeral Credentials Max 15-Minute Expiration Limit (PRD 7)
	// =========================================================================
	t.Run("SecurityTTL_EnforceMax15Minutes", func(t *testing.T) {
		// Valid TTL within 15 minutes
		validTTL := 10 * time.Minute
		slot, err := s3Driver.GenerateUploadSlot(ctx, "s3://tenant-data-vault/dataset.csv", validTTL)
		if err != nil {
			t.Fatalf("expected valid 10m TTL to succeed, got: %v", err)
		}
		if slot.TargetURL == "" {
			t.Fatal("expected non-empty presigned TargetURL")
		}
		if slot.HTTPMethod != http.MethodPut {
			t.Fatalf("expected HTTP PUT, got: %s", slot.HTTPMethod)
		}
		if slot.IsExpired() {
			t.Fatal("slot should not be expired immediately upon generation")
		}

		// Rejection of TTL exceeding 15 minutes (PRD 7)
		forbiddenTTL := 16 * time.Minute
		_, err = s3Driver.GenerateUploadSlot(ctx, "s3://tenant-data-vault/dataset.csv", forbiddenTTL)
		if err == nil {
			t.Fatal("expected ErrTTLExceeded for 16-minute TTL, got nil")
		}
		if !errors.Is(err, storage.ErrTTLExceeded) {
			t.Fatalf("expected error wrapping ErrTTLExceeded, got: %v", err)
		}

		// Expired slot simulation
		expiredSlot := &protocol.UploadSlotResponse{
			TargetURL:        slot.TargetURL,
			HTTPMethod:       http.MethodPut,
			StorageReference: "s3://tenant-data-vault/dataset.csv",
			ExpiredAt:        time.Now().Add(-1 * time.Minute),
		}
		if !expiredSlot.IsExpired() {
			t.Fatal("expected expiredSlot.IsExpired() to return true")
		}
	})

	// =========================================================================
	// 2. Direct Pre-signed Upload and Streaming Ingestion (PRD 5.3)
	// =========================================================================
	t.Run("DirectUpload_And_StreamIngestion", func(t *testing.T) {
		storageRef := "s3://tenant-data-vault/tenants/acme/sales_records.csv"

		// Step A: Request pre-signed upload slot
		slot, err := s3Driver.GenerateUploadSlot(ctx, storageRef, 15*time.Minute)
		if err != nil {
			t.Fatalf("GenerateUploadSlot failed: %v", err)
		}
		t.Logf("✓ Pre-signed URL generated: %s", slot.TargetURL)

		// Step B: Direct client HTTP PUT upload into storage
		samplePayload := []byte("id,product,amount\n101,Widget-A,49.99\n102,Widget-B,199.50\n103,Service-C,89.00\n")
		expectedHash := sha256.Sum256(samplePayload)

		putReq, err := http.NewRequest(slot.HTTPMethod, slot.TargetURL, bytes.NewReader(samplePayload))
		if err != nil {
			t.Fatalf("failed to create PUT request: %v", err)
		}
		putReq.Header.Set("Content-Type", "text/csv")

		putResp, err := http.DefaultClient.Do(putReq)
		if err != nil {
			t.Fatalf("direct PUT upload failed: %v", err)
		}
		defer putResp.Body.Close()

		if putResp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(putResp.Body)
			t.Fatalf("expected HTTP 200 OK from storage upload, got %d: %s", putResp.StatusCode, string(body))
		}
		t.Log("✓ Direct HTTP PUT upload to S3 endpoint succeeded")

		// Step C: Direct Streaming Read via storage.OpenStream (Zero local disk files)
		readStream, err := storage.OpenStream(ctx, storageRef)
		if err != nil {
			t.Fatalf("storage.OpenStream failed: %v", err)
		}
		defer readStream.Close()

		readBytes, err := io.ReadAll(readStream)
		if err != nil {
			t.Fatalf("failed to read from storage stream: %v", err)
		}

		if len(readBytes) != len(samplePayload) {
			t.Fatalf("data length mismatch: expected %d bytes, got %d bytes", len(samplePayload), len(readBytes))
		}

		actualHash := sha256.Sum256(readBytes)
		if actualHash != expectedHash {
			t.Fatalf("data integrity mismatch: SHA256 hashes differ")
		}
		t.Log("✓ Streaming read verified byte-for-byte with SHA-256 integrity")

		// Step D: Stream reading via S3 driver directly
		directStream, err := s3Driver.StreamRead(ctx, storageRef)
		if err != nil {
			t.Fatalf("s3Driver.StreamRead failed: %v", err)
		}
		defer directStream.Close()

		var directBuf bytes.Buffer
		copied, err := io.Copy(&directBuf, directStream)
		if err != nil {
			t.Fatalf("io.Copy from directStream failed: %v", err)
		}
		if copied != int64(len(samplePayload)) {
			t.Fatalf("expected %d copied bytes, got %d", len(samplePayload), copied)
		}
		t.Log("✓ Driver StreamRead verified with direct copy pipe")

		// Step E: Clean up via DeleteObject
		if err := s3Driver.DeleteObject(ctx, storageRef); err != nil {
			t.Fatalf("DeleteObject failed: %v", err)
		}

		// Subsequent read should fail (NoSuchKey)
		_, err = s3Driver.StreamRead(ctx, storageRef)
		if err == nil {
			t.Fatal("expected error reading deleted object, got nil")
		}
		t.Log("✓ Object successfully deleted from storage")
	})

	// =========================================================================
	// 3. Multi-Megabyte High-Throughput Stream Ingestion
	// =========================================================================
	t.Run("LargeDataset_HighThroughput_Streaming", func(t *testing.T) {
		largeRef := "s3://tenant-data-vault/benchmark/large_stream.dat"

		slot, err := s3Driver.GenerateUploadSlot(ctx, largeRef, 15*time.Minute)
		if err != nil {
			t.Fatalf("GenerateUploadSlot failed: %v", err)
		}

		// Generate 4MB repeatable binary data
		chunkSize := 1024 * 1024 // 1MB
		totalChunks := 4
		hasher := sha256.New()

		var uploadBuf bytes.Buffer
		for c := 0; c < totalChunks; c++ {
			chunk := bytes.Repeat([]byte{byte(c + 1)}, chunkSize)
			uploadBuf.Write(chunk)
			hasher.Write(chunk)
		}
		expectedDigest := hasher.Sum(nil)

		// Upload directly
		putReq, err := http.NewRequest(http.MethodPut, slot.TargetURL, &uploadBuf)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(putReq)
		if err != nil {
			t.Fatalf("PUT 4MB upload failed: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("upload returned HTTP %d", resp.StatusCode)
		}

		// Stream read in 64KB chunks to simulate worker chunking
		stream, err := storage.OpenStream(ctx, largeRef)
		if err != nil {
			t.Fatalf("OpenStream failed: %v", err)
		}
		defer stream.Close()

		readHasher := sha256.New()
		buf := make([]byte, 64*1024)
		var totalRead int64

		for {
			n, rErr := stream.Read(buf)
			if n > 0 {
				readHasher.Write(buf[:n])
				totalRead += int64(n)
			}
			if rErr == io.EOF {
				break
			}
			if rErr != nil {
				t.Fatalf("stream read error: %v", rErr)
			}
		}

		if totalRead != int64(totalChunks*chunkSize) {
			t.Fatalf("expected %d bytes, got %d", totalChunks*chunkSize, totalRead)
		}

		actualDigest := readHasher.Sum(nil)
		if !bytes.Equal(actualDigest, expectedDigest) {
			t.Fatal("SHA256 digest of 4MB stream did not match uploaded stream")
		}
		t.Logf("✓ Successfully streamed 4MB payload through memory pipe in %d chunks", totalRead/(64*1024))
	})
}
