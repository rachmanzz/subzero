//go:build wasip1

package sdk

import (
	"fmt"
	"unsafe"
)

//go:wasmimport subzero_host host_storage_get
func hostStorageGet(keyPtr, keyLen, outPtr, outMaxLen, outReadPtr uint32) uint32

//go:wasmimport subzero_host host_storage_put
func hostStoragePut(keyPtr, keyLen, valPtr, valLen uint32) uint32

//go:wasmimport subzero_host host_log
func hostLog(level uint32, msgPtr, msgLen uint32) uint32

//go:wasmimport subzero_host host_metrics_record
func hostMetricsRecord(namePtr, nameLen uint32, val float64) uint32

func callHostStorageGet(key string) ([]byte, error) {
	keyBytes := []byte(key)
	if len(keyBytes) == 0 {
		return nil, fmt.Errorf("empty key")
	}
	keyPtr := uint32(uintptr(unsafe.Pointer(&keyBytes[0])))
	keyLen := uint32(len(keyBytes))

	// Initial buffer: 64 KB
	buf := make([]byte, 65536)
	bufPtr := uint32(uintptr(unsafe.Pointer(&buf[0])))
	var bytesRead uint32
	readPtr := uint32(uintptr(unsafe.Pointer(&bytesRead)))

	errCode := hostStorageGet(keyPtr, keyLen, bufPtr, uint32(len(buf)), readPtr)
	if errCode == 1 {
		return nil, ErrPermissionDenied
	}
	if errCode == 2 {
		return nil, ErrKeyNotFound
	}
	if errCode == 3 {
		// Buffer was too small; bytesRead indicates needed size
		buf = make([]byte, bytesRead)
		bufPtr = uint32(uintptr(unsafe.Pointer(&buf[0])))
		errCode = hostStorageGet(keyPtr, keyLen, bufPtr, uint32(len(buf)), readPtr)
		if errCode != 0 {
			return nil, fmt.Errorf("hostcall storage_get retry failed with code %d", errCode)
		}
	}
	if errCode != 0 {
		return nil, ErrInternalError
	}
	return buf[:bytesRead], nil
}

func callHostStoragePut(key string, data []byte) error {
	keyBytes := []byte(key)
	if len(keyBytes) == 0 {
		return fmt.Errorf("empty key")
	}
	keyPtr := uint32(uintptr(unsafe.Pointer(&keyBytes[0])))
	keyLen := uint32(len(keyBytes))

	var valPtr uint32
	if len(data) > 0 {
		valPtr = uint32(uintptr(unsafe.Pointer(&data[0])))
	}
	valLen := uint32(len(data))

	errCode := hostStoragePut(keyPtr, keyLen, valPtr, valLen)
	if errCode == 1 {
		return ErrPermissionDenied
	}
	if errCode != 0 {
		return ErrInternalError
	}
	return nil
}

func callHostLog(level LogLevel, msg string) error {
	msgBytes := []byte(msg)
	var msgPtr uint32
	if len(msgBytes) > 0 {
		msgPtr = uint32(uintptr(unsafe.Pointer(&msgBytes[0])))
	}
	msgLen := uint32(len(msgBytes))

	errCode := hostLog(uint32(level), msgPtr, msgLen)
	if errCode == 1 {
		return ErrPermissionDenied
	}
	if errCode != 0 {
		return ErrInternalError
	}
	return nil
}

func callHostMetricsRecord(name string, val float64) error {
	nameBytes := []byte(name)
	if len(nameBytes) == 0 {
		return fmt.Errorf("empty metric name")
	}
	namePtr := uint32(uintptr(unsafe.Pointer(&nameBytes[0])))
	nameLen := uint32(len(nameBytes))

	errCode := hostMetricsRecord(namePtr, nameLen, val)
	if errCode == 1 {
		return ErrPermissionDenied
	}
	if errCode != 0 {
		return ErrInternalError
	}
	return nil
}
