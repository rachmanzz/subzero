package sdk

import (
	"errors"
	"fmt"
)

var (
	// ErrPermissionDenied is returned when the worker policy denies the requested host call.
	ErrPermissionDenied = errors.New("hostcall: permission denied by worker policy")

	// ErrKeyNotFound is returned when a requested object is not found in host storage.
	ErrKeyNotFound = errors.New("hostcall: key not found in storage")

	// ErrBufferTooSmall is returned when the allocated guest buffer is insufficient.
	ErrBufferTooSmall = errors.New("hostcall: buffer too small")

	// ErrInternalError is returned when an unspecified host-side error occurs.
	ErrInternalError = errors.New("hostcall: internal host error")
)

// LogLevel represents the logging severity level for host_log.
type LogLevel int

const (
	LogLevelDebug LogLevel = 0
	LogLevelInfo  LogLevel = 1
	LogLevelWarn  LogLevel = 2
	LogLevelError LogLevel = 3
)

// GetStorageBlob retrieves a file or blob from the host storage provider via host call.
func GetStorageBlob(key string) ([]byte, error) {
	if key == "" {
		return nil, fmt.Errorf("storage key cannot be empty")
	}
	return callHostStorageGet(key)
}

// PutStorageBlob writes a file or blob directly to the host storage provider via host call.
func PutStorageBlob(key string, data []byte) error {
	if key == "" {
		return fmt.Errorf("storage key cannot be empty")
	}
	return callHostStoragePut(key, data)
}

// Log streams a log message directly to the host supervisor logger via host call.
func Log(level LogLevel, msg string) error {
	return callHostLog(level, msg)
}

// RecordMetric emits a metric counter/gauge to the host supervisor via host call.
func RecordMetric(name string, val float64) error {
	if name == "" {
		return fmt.Errorf("metric name cannot be empty")
	}
	return callHostMetricsRecord(name, val)
}
