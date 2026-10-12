package protocol

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
)

// SignatureSize defines the exact size in bytes of the HMAC-SHA256 integrity hash.
const SignatureSize = 32

var (
	// ErrInvalidSignature is returned when the HMAC signature verification fails.
	ErrInvalidSignature = errors.New("protocol: invalid HMAC integrity signature")
	// ErrEmptySecretKey is returned when the secret key provided is empty.
	ErrEmptySecretKey = errors.New("protocol: secret key cannot be empty")
)

// Sign computes the HMAC-SHA256 signature for the given payload using secretKey.
func Sign(payload, secretKey []byte) []byte {
	mac := hmac.New(sha256.New, secretKey)
	mac.Write(payload)
	return mac.Sum(nil)
}

// Verify checks whether the signature matches the payload HMAC-SHA256 using constant-time comparison.
func Verify(payload, signature, secretKey []byte) bool {
	if len(signature) != SignatureSize {
		return false
	}
	expected := Sign(payload, secretKey)
	return hmac.Equal(signature, expected)
}
