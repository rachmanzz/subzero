package s3_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/rachmanzz/subzero/internal/storage/s3"
)

func TestS3DriverPresignUploadSlot(t *testing.T) {
	driver, err := s3.New(s3.Config{
		Bucket:    "my-test-bucket",
		Region:    "us-east-1",
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
	})
	if err != nil {
		t.Fatalf("s3.New failed: %v", err)
	}

	if driver.Scheme() != "s3" {
		t.Fatalf("expected scheme s3, got %s", driver.Scheme())
	}

	slot, err := driver.GenerateUploadSlot(context.Background(), "tenant-corp/data.xlsx", 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateUploadSlot failed: %v", err)
	}

	if slot.HTTPMethod != "PUT" {
		t.Errorf("expected PUT method, got %s", slot.HTTPMethod)
	}

	if !strings.Contains(slot.TargetURL, "my-test-bucket") {
		t.Errorf("target URL does not contain bucket: %s", slot.TargetURL)
	}

	if !strings.Contains(slot.TargetURL, "X-Amz-Signature") {
		t.Errorf("target URL is not pre-signed: %s", slot.TargetURL)
	}
}

func TestMinIORegistration(t *testing.T) {
	driver, err := s3.RegisterMinIO(s3.Config{
		Bucket:    "minio-bucket",
		Endpoint:  "http://127.0.0.1:9000",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
	})
	if err != nil {
		t.Fatalf("RegisterMinIO failed: %v", err)
	}

	slot, err := driver.GenerateUploadSlot(context.Background(), "minio://minio-bucket/october.csv", 10*time.Minute)
	if err != nil {
		t.Fatalf("GenerateUploadSlot failed on MinIO: %v", err)
	}

	if !strings.Contains(slot.TargetURL, "127.0.0.1:9000") {
		t.Errorf("expected minio endpoint in URL, got %s", slot.TargetURL)
	}
}

func TestS3DriverBucketIsolation(t *testing.T) {
	// Configure driver with a designated bucket
	driver, err := s3.New(s3.Config{
		Bucket:    "tenant-vault",
		Region:    "us-east-1",
		AccessKey: "test-access-key",
		SecretKey: "test-secret-key",
	})
	if err != nil {
		t.Fatalf("s3.New failed: %v", err)
	}

	// Caller specifies a different bucket name in the URI: should not switch target bucket
	slot, err := driver.GenerateUploadSlot(context.Background(), "s3://unauthorized-bucket/secret.csv", 15*time.Minute)
	if err != nil {
		t.Fatalf("GenerateUploadSlot failed: %v", err)
	}

	if strings.Contains(slot.TargetURL, "//unauthorized-bucket.") || strings.HasPrefix(slot.TargetURL, "https://unauthorized-bucket.") {
		t.Errorf("vulnerability detected: target bucket switched to unauthorized-bucket: %s", slot.TargetURL)
	}
	if !strings.Contains(slot.TargetURL, "tenant-vault.s3.us-east-1.amazonaws.com") {
		t.Errorf("expected target host to remain pinned to tenant-vault: %s", slot.TargetURL)
	}
}
