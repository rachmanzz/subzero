package sdk

import (
	"testing"
)

func TestNativeHostCalls(t *testing.T) {
	// 1. PutStorageBlob & GetStorageBlob
	key := "test-key-1"
	expected := []byte("hello world payload from host")

	if err := PutStorageBlob(key, expected); err != nil {
		t.Fatalf("PutStorageBlob failed: %v", err)
	}

	actual, err := GetStorageBlob(key)
	if err != nil {
		t.Fatalf("GetStorageBlob failed: %v", err)
	}

	if string(actual) != string(expected) {
		t.Fatalf("expected %s, got %s", expected, actual)
	}

	// 2. GetStorageBlob with non-existent key
	_, err = GetStorageBlob("unknown-key")
	if err != ErrKeyNotFound {
		t.Fatalf("expected ErrKeyNotFound, got %v", err)
	}

	// 3. Log
	if err := Log(LogLevelInfo, "testing log message"); err != nil {
		t.Fatalf("Log failed: %v", err)
	}

	// 4. RecordMetric
	if err := RecordMetric("processed_rows", 1250); err != nil {
		t.Fatalf("RecordMetric failed: %v", err)
	}
}
