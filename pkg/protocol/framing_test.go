package protocol

import (
	"bytes"
	"io"
	"testing"
)

func TestFramingRoundTrip(t *testing.T) {
	secretKey := []byte("super-secret-subzero-key-32b-ok!")
	original := []byte("hello, subzero distributed runtime!")

	var buf bytes.Buffer
	err := WriteFrame(&buf, original, secretKey)
	if err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}

	read, err := ReadFrame(&buf, secretKey, 0)
	if err != nil {
		t.Fatalf("ReadFrame failed: %v", err)
	}

	if !bytes.Equal(original, read) {
		t.Errorf("payload mismatch: expected %q, got %q", string(original), string(read))
	}
}

func TestFramingTamperedPayload(t *testing.T) {
	secretKey := []byte("super-secret-subzero-key-32b-ok!")
	original := []byte("untampered data")

	var buf bytes.Buffer
	if err := WriteFrame(&buf, original, secretKey); err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}

	// Tamper with the payload (modify the last byte)
	raw := buf.Bytes()
	raw[len(raw)-1] ^= 0xFF

	r := bytes.NewReader(raw)
	_, err := ReadFrame(r, secretKey, 0)
	if err != ErrInvalidSignature {
		t.Fatalf("expected ErrInvalidSignature, got %v", err)
	}
}

func TestFramingWrongSecretKey(t *testing.T) {
	keyA := []byte("key-alice-1234567890123456789012")
	keyB := []byte("key-bob---1234567890123456789012")
	payload := []byte("important payload")

	var buf bytes.Buffer
	if err := WriteFrame(&buf, payload, keyA); err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}

	_, err := ReadFrame(&buf, keyB, 0)
	if err != ErrInvalidSignature {
		t.Fatalf("expected ErrInvalidSignature on key mismatch, got %v", err)
	}
}

func TestFramingEmptyPayload(t *testing.T) {
	secretKey := []byte("secret-key")
	var buf bytes.Buffer

	if err := WriteFrame(&buf, []byte{}, secretKey); err != nil {
		t.Fatalf("WriteFrame failed on empty payload: %v", err)
	}

	read, err := ReadFrame(&buf, secretKey, 0)
	if err != nil {
		t.Fatalf("ReadFrame failed: %v", err)
	}
	if len(read) != 0 {
		t.Fatalf("expected empty payload, got %d bytes", len(read))
	}
}

func TestFramingPayloadExceedsMaxLimit(t *testing.T) {
	secretKey := []byte("secret-key")
	payload := make([]byte, 1024)

	var buf bytes.Buffer
	if err := WriteFrame(&buf, payload, secretKey); err != nil {
		t.Fatalf("WriteFrame failed: %v", err)
	}

	// Read with limit lower than payload
	_, err := ReadFrame(&buf, secretKey, 512)
	if err == nil {
		t.Fatal("expected error for payload exceeding max limit, got nil")
	}
}

func TestFramingEOFHandling(t *testing.T) {
	secretKey := []byte("secret-key")
	var buf bytes.Buffer

	_, err := ReadFrame(&buf, secretKey, 0)
	if err != io.EOF {
		t.Fatalf("expected io.EOF on empty reader, got %v", err)
	}
}

func TestTaskCodecRoundTrip(t *testing.T) {
	task := &TaskMessage{
		TaskID:       "task-1234-uuid",
		TenantID:     "tenant-alpha",
		ActionTarget: "excel-parser-wasm",
		SourceURI:    "s3://documents-bucket/tenant-alpha/october-data.xlsx",
		CustomConfig: map[string]string{
			"header_row": "1",
			"sheet_name": "Sheet1",
		},
	}

	data, err := EncodeTask(task, []byte("key"))
	if err != nil {
		t.Fatalf("EncodeTask failed: %v", err)
	}

	decoded, err := DecodeTask(data)
	if err != nil {
		t.Fatalf("DecodeTask failed: %v", err)
	}

	if decoded.TaskID != task.TaskID || decoded.TenantID != task.TenantID || decoded.SourceURI != task.SourceURI {
		t.Fatalf("decoded task does not match original: %+v vs %+v", decoded, task)
	}
}
