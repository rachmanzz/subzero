package management

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	ErrModuleNotFound  = errors.New("management: wasm module not found")
	ErrInvalidModuleID = errors.New("management: invalid module ID or path traversal attempt")
)

// Store defines the pluggable contract for persisting and retrieving Wasm modules.
type Store interface {
	Save(ctx context.Context, module *WasmModule, wasmBytes []byte, sourceBytes []byte) error
	List(ctx context.Context, tenantID string) ([]*WasmModule, error)
	Get(ctx context.Context, moduleID string) (*WasmModule, error)
	GetWasmBytes(ctx context.Context, moduleID string) ([]byte, error)
	Delete(ctx context.Context, moduleID string) error
}

// FileStore is the default filesystem-backed module storage.
type FileStore struct {
	baseDir string
	mu      sync.RWMutex
}

// NewFileStore creates a new filesystem store rooted at baseDir.
func NewFileStore(baseDir string) (*FileStore, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("management/store: failed to create base dir: %w", err)
	}
	return &FileStore{baseDir: baseDir}, nil
}

func (s *FileStore) moduleDir(moduleID string) (string, error) {
	trimmed := strings.TrimSpace(moduleID)
	if trimmed == "" {
		return "", ErrInvalidModuleID
	}
	if strings.Contains(trimmed, "..") || strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
		return "", fmt.Errorf("%w: %q", ErrInvalidModuleID, moduleID)
	}
	cleanBase := filepath.Clean(s.baseDir)
	target := filepath.Join(cleanBase, trimmed)
	cleanTarget := filepath.Clean(target)
	if !strings.HasPrefix(cleanTarget, cleanBase+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrInvalidModuleID, moduleID)
	}
	return cleanTarget, nil
}

func (s *FileStore) Save(ctx context.Context, module *WasmModule, wasmBytes []byte, sourceBytes []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir, err := s.moduleDir(module.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("management/store: create dir failed: %w", err)
	}

	wasmPath := filepath.Join(dir, "module.wasm")
	if err := os.WriteFile(wasmPath, wasmBytes, 0644); err != nil {
		return fmt.Errorf("management/store: write wasm failed: %w", err)
	}
	module.WasmPath = wasmPath
	module.Size = int64(len(wasmBytes))

	if len(sourceBytes) > 0 {
		srcPath := filepath.Join(dir, "source.go")
		if err := os.WriteFile(srcPath, sourceBytes, 0644); err != nil {
			return fmt.Errorf("management/store: write source failed: %w", err)
		}
		module.SourcePath = srcPath
	}

	metaPath := filepath.Join(dir, "meta.json")
	metaBytes, err := json.MarshalIndent(module, "", "  ")
	if err != nil {
		return fmt.Errorf("management/store: marshal meta failed: %w", err)
	}

	return os.WriteFile(metaPath, metaBytes, 0644)
}

func (s *FileStore) List(ctx context.Context, tenantID string) ([]*WasmModule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return nil, fmt.Errorf("management/store: read base dir failed: %w", err)
	}

	var modules []*WasmModule
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metaPath := filepath.Join(s.baseDir, entry.Name(), "meta.json")
		data, err := os.ReadFile(metaPath)
		if err != nil {
			continue
		}

		var mod WasmModule
		if err := json.Unmarshal(data, &mod); err != nil {
			continue
		}

		if tenantID == "" || mod.TenantID == tenantID {
			modules = append(modules, &mod)
		}
	}

	return modules, nil
}

func (s *FileStore) Get(ctx context.Context, moduleID string) (*WasmModule, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir, err := s.moduleDir(moduleID)
	if err != nil {
		return nil, err
	}

	metaPath := filepath.Join(dir, "meta.json")
	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrModuleNotFound
		}
		return nil, err
	}

	var mod WasmModule
	if err := json.Unmarshal(data, &mod); err != nil {
		return nil, fmt.Errorf("management/store: unmarshal meta failed: %w", err)
	}

	return &mod, nil
}

func (s *FileStore) GetWasmBytes(ctx context.Context, moduleID string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	dir, err := s.moduleDir(moduleID)
	if err != nil {
		return nil, err
	}

	wasmPath := filepath.Join(dir, "module.wasm")
	bytes, err := os.ReadFile(wasmPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrModuleNotFound
		}
		return nil, err
	}

	return bytes, nil
}

func (s *FileStore) Delete(ctx context.Context, moduleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	dir, err := s.moduleDir(moduleID)
	if err != nil {
		return err
	}

	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("management/store: delete module %q failed: %w", moduleID, err)
	}
	return nil
}
