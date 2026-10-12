package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Codec defines an interface for serializing and deserializing message payloads.
type Codec interface {
	Marshal(v any) ([]byte, error)
	Unmarshal(data []byte, v any) error
}

// JSONCodec provides standard JSON serialization.
type JSONCodec struct{}

// DefaultCodec is the default serialization mechanism used across Subzero components.
var DefaultCodec Codec = &JSONCodec{}

// Marshal encodes v to JSON bytes.
func (c *JSONCodec) Marshal(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("codec: marshal failed: %w", err)
	}
	return data, nil
}

// Unmarshal decodes JSON bytes into v.
func (c *JSONCodec) Unmarshal(data []byte, v any) error {
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("codec: unmarshal failed: %w", err)
	}
	return nil
}

// EncodeTask serializes a TaskMessage into byte payload. Note: wire framing and HMAC-SHA256 signatures are applied during transmission via WriteFrame.
func EncodeTask(task *TaskMessage, secretKey []byte) ([]byte, error) {
	data, err := DefaultCodec.Marshal(task)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// EncodeFramedTask serializes and applies 36-byte framing and HMAC-SHA256 signature to a TaskMessage.
func EncodeFramedTask(task *TaskMessage, secretKey []byte) ([]byte, error) {
	data, err := DefaultCodec.Marshal(task)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := WriteFrame(&buf, data, secretKey); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// DecodeTask decodes a raw payload into TaskMessage.
func DecodeTask(data []byte) (*TaskMessage, error) {
	var task TaskMessage
	if err := DefaultCodec.Unmarshal(data, &task); err != nil {
		return nil, err
	}
	return &task, nil
}

// EncodeResult encodes a ResultMessage.
func EncodeResult(result *ResultMessage) ([]byte, error) {
	data, err := DefaultCodec.Marshal(result)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// DecodeResult decodes raw bytes into ResultMessage.
func DecodeResult(data []byte) (*ResultMessage, error) {
	var res ResultMessage
	if err := DefaultCodec.Unmarshal(data, &res); err != nil {
		return nil, err
	}
	return &res, nil
}
