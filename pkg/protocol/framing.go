package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	// HeaderSize is 4 bytes (uint32 length) + 32 bytes (HMAC-SHA256 signature).
	HeaderSize = 4 + SignatureSize

	// DefaultMaxPayloadSize sets a reasonable upper limit for framed messages (16MB)
	// to prevent malicious or malformed length fields from causing allocation bloat.
	DefaultMaxPayloadSize = 16 * 1024 * 1024
)

var (
	// ErrPayloadTooLarge is returned when payload length exceeds the maximum allowed size.
	ErrPayloadTooLarge = errors.New("protocol: payload size exceeds maximum limit")
	// ErrIncompleteFrame is returned when the frame stream ends unexpectedly.
	ErrIncompleteFrame = errors.New("protocol: incomplete frame read")
)

// WriteFrame writes a wire-framed message to w.
// Format: [4 Bytes Uint32 Length] [32 Bytes HMAC-SHA256] [Payload bytes]
func WriteFrame(w io.Writer, payload []byte, secretKey []byte) error {
	payloadLen := len(payload)
	if payloadLen > DefaultMaxPayloadSize {
		return ErrPayloadTooLarge
	}

	sig := Sign(payload, secretKey)

	// Buffer header: 4 bytes length + 32 bytes signature
	header := make([]byte, HeaderSize)
	binary.BigEndian.PutUint32(header[0:4], uint32(payloadLen))
	copy(header[4:HeaderSize], sig)

	if _, err := w.Write(header); err != nil {
		return fmt.Errorf("protocol: write header failed: %w", err)
	}

	if payloadLen > 0 {
		if _, err := w.Write(payload); err != nil {
			return fmt.Errorf("protocol: write payload failed: %w", err)
		}
	}

	return nil
}

// ReadFrame reads a wire-framed message from r, verifies its HMAC integrity signature,
// and returns the authenticated payload.
func ReadFrame(r io.Reader, secretKey []byte, maxPayloadSize uint32) ([]byte, error) {
	if maxPayloadSize == 0 {
		maxPayloadSize = DefaultMaxPayloadSize
	}

	header := make([]byte, HeaderSize)
	if _, err := io.ReadFull(r, header); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("protocol: read header failed: %w", err)
	}

	length := binary.BigEndian.Uint32(header[0:4])
	if length > maxPayloadSize {
		return nil, fmt.Errorf("%w: requested %d bytes, max allowed %d", ErrPayloadTooLarge, length, maxPayloadSize)
	}

	signature := header[4:HeaderSize]

	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrIncompleteFrame, err)
		}
	}

	if !Verify(payload, signature, secretKey) {
		return nil, ErrInvalidSignature
	}

	return payload, nil
}
