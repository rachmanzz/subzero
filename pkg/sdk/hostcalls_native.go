//go:build !wasip1

package sdk

import (
	"fmt"
	"os"
	"sync"
)

var (
	nativeMockStorage = struct {
		sync.RWMutex
		data map[string][]byte
	}{data: make(map[string][]byte)}

	nativeMockMetrics = struct {
		sync.RWMutex
		metrics map[string]float64
	}{metrics: make(map[string]float64)}
)

func callHostStorageGet(key string) ([]byte, error) {
	nativeMockStorage.RLock()
	defer nativeMockStorage.RUnlock()
	val, ok := nativeMockStorage.data[key]
	if !ok {
		return nil, ErrKeyNotFound
	}
	cpy := make([]byte, len(val))
	copy(cpy, val)
	return cpy, nil
}

func callHostStoragePut(key string, data []byte) error {
	nativeMockStorage.Lock()
	defer nativeMockStorage.Unlock()
	cpy := make([]byte, len(data))
	copy(cpy, data)
	nativeMockStorage.data[key] = cpy
	return nil
}

func callHostLog(level LogLevel, msg string) error {
	levelStr := "DEBUG"
	switch level {
	case LogLevelInfo:
		levelStr = "INFO"
	case LogLevelWarn:
		levelStr = "WARN"
	case LogLevelError:
		levelStr = "ERROR"
	}
	fmt.Fprintf(os.Stderr, "[Native Mock Host Log | %s] %s\n", levelStr, msg)
	return nil
}

func callHostMetricsRecord(name string, val float64) error {
	nativeMockMetrics.Lock()
	defer nativeMockMetrics.Unlock()
	nativeMockMetrics.metrics[name] = val
	return nil
}
