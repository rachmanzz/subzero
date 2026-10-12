package permission

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

var (
	ErrNilPolicy              = errors.New("permission: worker policy cannot be nil")
	ErrUnauthorizedHostCall   = errors.New("permission: requested host call not permitted by worker policy")
	ErrUnauthorizedPath       = errors.New("permission: requested filesystem path not permitted by worker policy")
	ErrPathTraversal          = errors.New("permission: path traversal or relative path forbidden")
	ErrSensitivePath          = errors.New("permission: access to sensitive system directory forbidden")
	ErrUnauthorizedS3Bucket   = errors.New("permission: requested S3 bucket not permitted by worker policy")
	ErrUnauthorizedS3Prefix   = errors.New("permission: requested S3 prefix not permitted by worker policy")
	ErrUnauthorizedOperation  = errors.New("permission: requested storage operation not permitted by worker policy")
	ErrUnauthorizedExtension  = errors.New("permission: requested file extension not permitted by worker policy")
	ErrFileTooLarge           = errors.New("permission: requested file size exceeds worker policy limit")
)

// Sensitive root directories that can never be mounted into a guest sandbox.
var sensitiveDirectories = []string{
	"/etc", "/root", "/boot", "/sys", "/proc", "/dev", "/usr", "/bin", "/sbin", "/lib", "/lib64",
}

// WorkerPolicy defines the absolute capabilities granted to a worker process by the host.
type WorkerPolicy struct {
	AllowedHostCalls []string         `json:"allowed_host_calls" yaml:"allowed_host_calls"`
	Filesystem       PolicyFilesystem `json:"filesystem" yaml:"filesystem"`
	S3               PolicyS3         `json:"s3" yaml:"s3"`
	Files            PolicyFiles      `json:"files" yaml:"files"`
}

type PolicyFilesystem struct {
	AllowedReadOnlyRoots  []string `json:"allowed_read_only_roots" yaml:"allowed_read_only_roots"`
	AllowedReadWriteRoots []string `json:"allowed_read_write_roots" yaml:"allowed_read_write_roots"`
}

type PolicyS3 struct {
	AllowedBuckets    []string `json:"allowed_buckets" yaml:"allowed_buckets"`
	AllowedPrefixes   []string `json:"allowed_prefixes" yaml:"allowed_prefixes"`
	AllowedOperations []string `json:"allowed_operations" yaml:"allowed_operations"` // "read", "write"
}

type PolicyFiles struct {
	AllowedExtensions []string `json:"allowed_extensions" yaml:"allowed_extensions"`
	MaxFileSizeBytes  int64    `json:"max_file_size_bytes" yaml:"max_file_size_bytes"`
}

// ModuleRequest defines the permissions requested by a tenant module in config.zero.
type ModuleRequest struct {
	HostCalls  []string          `json:"host_calls,omitempty" yaml:"host_calls"`
	Filesystem RequestFilesystem `json:"filesystem,omitempty" yaml:"filesystem"`
	S3         RequestS3         `json:"s3,omitempty" yaml:"s3"`
	Files      RequestFiles      `json:"files,omitempty" yaml:"files"`
}

type RequestFilesystem struct {
	ReadOnly  []string `json:"read_only,omitempty" yaml:"read_only"`
	ReadWrite []string `json:"read_write,omitempty" yaml:"read_write"`
}

type RequestS3 struct {
	AllowedBuckets  []string `json:"allowed_buckets,omitempty" yaml:"allowed_buckets"`
	AllowedPrefixes []string `json:"allowed_prefixes,omitempty" yaml:"allowed_prefixes"`
	Operations      []string `json:"operations,omitempty" yaml:"operations"`
}

type RequestFiles struct {
	AllowedExtensions []string `json:"allowed_extensions,omitempty" yaml:"allowed_extensions"`
	MaxFileSizeMB     int64    `json:"max_file_size_mb,omitempty" yaml:"max_file_size_mb"`
}

// EffectivePermissions represents the evaluated permissions granted to the Wasm sandbox.
type EffectivePermissions struct {
	HostCalls  map[string]bool     `json:"host_calls"`
	Filesystem EffectiveFilesystem `json:"filesystem"`
	S3         EffectiveS3         `json:"s3"`
	Files      EffectiveFiles      `json:"files"`
}

type EffectiveFilesystem struct {
	ReadOnly  []string `json:"read_only"`
	ReadWrite []string `json:"read_write"`
}

type EffectiveS3 struct {
	AllowedBuckets  []string        `json:"allowed_buckets"`
	AllowedPrefixes []string        `json:"allowed_prefixes"`
	Operations      map[string]bool `json:"operations"`
}

type EffectiveFiles struct {
	AllowedExtensions []string `json:"allowed_extensions"`
	MaxFileSizeBytes  int64    `json:"max_file_size_bytes"`
}

// DefaultWorkerPolicy returns a standard safe policy for general worker processes.
func DefaultWorkerPolicy() WorkerPolicy {
	return WorkerPolicy{
		AllowedHostCalls: []string{
			"storage:get_object",
			"storage:put_object",
			"logger:debug",
			"logger:info",
			"logger:error",
			"metrics:record",
		},
		Filesystem: PolicyFilesystem{
			AllowedReadOnlyRoots: []string{
				"/tmp/subzero-tenants",
				"/data/subzero/uploads",
			},
			AllowedReadWriteRoots: []string{
				"/tmp/subzero-workspace",
			},
		},
		S3: PolicyS3{
			AllowedBuckets:    []string{"*"},
			AllowedPrefixes:   []string{"s3://"},
			AllowedOperations: []string{"read", "write"},
		},
		Files: PolicyFiles{
			AllowedExtensions: []string{".csv", ".tsv", ".json", ".txt", ".xlsx", ".xls"},
			MaxFileSizeBytes:  100 * 1024 * 1024, // 100 MB
		},
	}
}

// PermissiveWorkerPolicy returns an unrestricted policy suitable for tests and development.
func PermissiveWorkerPolicy() WorkerPolicy {
	return WorkerPolicy{
		AllowedHostCalls: []string{"*"},
		Filesystem: PolicyFilesystem{
			AllowedReadOnlyRoots:  []string{"/"},
			AllowedReadWriteRoots: []string{"/tmp"},
		},
		S3: PolicyS3{
			AllowedBuckets:    []string{"*"},
			AllowedPrefixes:   []string{"*"},
			AllowedOperations: []string{"read", "write"},
		},
		Files: PolicyFiles{
			AllowedExtensions: []string{"*"},
			MaxFileSizeBytes:  1024 * 1024 * 1024, // 1 GB
		},
	}
}

// Evaluate evaluates a module's capability request against the worker process policy.
// It returns an error if any requested capability exceeds the policy (Privilege Escalation Check).
func Evaluate(policy *WorkerPolicy, req *ModuleRequest) (*EffectivePermissions, error) {
	if policy == nil {
		return nil, ErrNilPolicy
	}

	eff := &EffectivePermissions{
		HostCalls: make(map[string]bool),
		S3: EffectiveS3{
			Operations: make(map[string]bool),
		},
	}

	// 1. If req is nil, module has zero permissions requested (clean least-privilege)
	if req == nil {
		eff.Files.MaxFileSizeBytes = policy.Files.MaxFileSizeBytes
		return eff, nil
	}

	// 2. Evaluate Host Calls
	allowedCallsMap := make(map[string]bool)
	wildcardHostCalls := false
	for _, call := range policy.AllowedHostCalls {
		if call == "*" {
			wildcardHostCalls = true
		}
		allowedCallsMap[strings.ToLower(strings.TrimSpace(call))] = true
	}

	for _, reqCall := range req.HostCalls {
		cleanCall := strings.ToLower(strings.TrimSpace(reqCall))
		if cleanCall == "" {
			continue
		}
		if !wildcardHostCalls && !allowedCallsMap[cleanCall] {
			return nil, fmt.Errorf("%w: %q", ErrUnauthorizedHostCall, reqCall)
		}
		eff.HostCalls[cleanCall] = true
	}

	// 3. Evaluate Filesystem Read-Only Paths
	if len(req.Filesystem.ReadOnly) > 0 {
		for _, roPath := range req.Filesystem.ReadOnly {
			cleanPath, err := sanitizeAndValidatePath(roPath)
			if err != nil {
				return nil, err
			}
			if !IsPathUnderRoots(cleanPath, policy.Filesystem.AllowedReadOnlyRoots) {
				return nil, fmt.Errorf("%w (read-only): %q is not under allowed roots", ErrUnauthorizedPath, roPath)
			}
			eff.Filesystem.ReadOnly = append(eff.Filesystem.ReadOnly, cleanPath)
		}
	} else {
		// Inherit policy's allowed read-only roots ceiling
		eff.Filesystem.ReadOnly = policy.Filesystem.AllowedReadOnlyRoots
	}

	// 4. Evaluate Filesystem Read-Write Paths
	if len(req.Filesystem.ReadWrite) > 0 {
		for _, rwPath := range req.Filesystem.ReadWrite {
			cleanPath, err := sanitizeAndValidatePath(rwPath)
			if err != nil {
				return nil, err
			}
			if !IsPathUnderRoots(cleanPath, policy.Filesystem.AllowedReadWriteRoots) {
				return nil, fmt.Errorf("%w (read-write): %q is not under allowed roots", ErrUnauthorizedPath, rwPath)
			}
			eff.Filesystem.ReadWrite = append(eff.Filesystem.ReadWrite, cleanPath)
		}
	} else {
		// Inherit policy's allowed read-write roots ceiling
		eff.Filesystem.ReadWrite = policy.Filesystem.AllowedReadWriteRoots
	}

	// 5. Evaluate S3 Buckets
	wildcardBuckets := sliceContains(policy.S3.AllowedBuckets, "*")
	for _, reqBucket := range req.S3.AllowedBuckets {
		cleanBucket := strings.TrimSpace(reqBucket)
		if cleanBucket == "" {
			continue
		}
		if !wildcardBuckets && !sliceContains(policy.S3.AllowedBuckets, cleanBucket) {
			return nil, fmt.Errorf("%w: %q", ErrUnauthorizedS3Bucket, reqBucket)
		}
		eff.S3.AllowedBuckets = append(eff.S3.AllowedBuckets, cleanBucket)
	}

	// 6. Evaluate S3 Prefixes
	wildcardPrefixes := sliceContains(policy.S3.AllowedPrefixes, "*")
	for _, reqPrefix := range req.S3.AllowedPrefixes {
		cleanPrefix := strings.TrimSpace(reqPrefix)
		if cleanPrefix == "" {
			continue
		}
		if !wildcardPrefixes && !IsS3PrefixAllowed(cleanPrefix, policy.S3.AllowedPrefixes) {
			return nil, fmt.Errorf("%w: %q", ErrUnauthorizedS3Prefix, reqPrefix)
		}
		eff.S3.AllowedPrefixes = append(eff.S3.AllowedPrefixes, cleanPrefix)
	}

	// 7. Evaluate S3 Operations
	wildcardOps := sliceContains(policy.S3.AllowedOperations, "*")
	for _, op := range req.S3.Operations {
		cleanOp := strings.ToLower(strings.TrimSpace(op))
		if cleanOp == "" {
			continue
		}
		if !wildcardOps && !sliceContains(policy.S3.AllowedOperations, cleanOp) {
			return nil, fmt.Errorf("%w: %q", ErrUnauthorizedOperation, op)
		}
		eff.S3.Operations[cleanOp] = true
	}

	// 8. Evaluate File Extensions
	wildcardExts := sliceContains(policy.Files.AllowedExtensions, "*")
	for _, ext := range req.Files.AllowedExtensions {
		cleanExt := strings.ToLower(strings.TrimSpace(ext))
		if cleanExt == "" {
			continue
		}
		if !strings.HasPrefix(cleanExt, ".") {
			cleanExt = "." + cleanExt
		}
		if !wildcardExts && !sliceContains(policy.Files.AllowedExtensions, cleanExt) {
			return nil, fmt.Errorf("%w: %q", ErrUnauthorizedExtension, ext)
		}
		eff.Files.AllowedExtensions = append(eff.Files.AllowedExtensions, cleanExt)
	}
	if len(eff.Files.AllowedExtensions) == 0 && len(policy.Files.AllowedExtensions) > 0 {
		eff.Files.AllowedExtensions = policy.Files.AllowedExtensions
	}

	// 9. Evaluate File Size Limit
	policyMaxBytes := policy.Files.MaxFileSizeBytes
	if policyMaxBytes <= 0 {
		policyMaxBytes = 100 * 1024 * 1024
	}
	if req.Files.MaxFileSizeMB > 0 {
		reqBytes := req.Files.MaxFileSizeMB * 1024 * 1024
		if reqBytes > policyMaxBytes {
			return nil, fmt.Errorf("%w: requested %d MB exceeds policy limit of %d MB",
				ErrFileTooLarge, req.Files.MaxFileSizeMB, policyMaxBytes/(1024*1024))
		}
		eff.Files.MaxFileSizeBytes = reqBytes
	} else {
		eff.Files.MaxFileSizeBytes = policyMaxBytes
	}

	return eff, nil
}

func sanitizeAndValidatePath(p string) (string, error) {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return "", errors.New("permission: path cannot be empty")
	}
	if !filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("%w: path must be absolute %q", ErrPathTraversal, trimmed)
	}

	for _, part := range strings.Split(filepath.ToSlash(trimmed), "/") {
		if part == ".." {
			return "", fmt.Errorf("%w: traversal detected in %q", ErrPathTraversal, trimmed)
		}
	}

	clean := filepath.Clean(trimmed)

	// Check against sensitive system directories
	for _, sens := range sensitiveDirectories {
		if clean == sens || strings.HasPrefix(clean, sens+"/") {
			return "", fmt.Errorf("%w: %q", ErrSensitivePath, clean)
		}
	}

	return clean, nil
}

// IsPathUnderRoots checks whether a path resides inside any of the allowed root directories.
func IsPathUnderRoots(path string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	for _, root := range roots {
		cleanRoot := filepath.Clean(strings.TrimSpace(root))
		if cleanRoot == "/" {
			return true
		}
		if path == cleanRoot || strings.HasPrefix(path, cleanRoot+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// IsS3PrefixAllowed checks whether an S3 target URI or prefix matches any of the allowed policy prefixes.
func IsS3PrefixAllowed(prefix string, allowedPrefixes []string) bool {
	for _, allowed := range allowedPrefixes {
		cleanAllowed := strings.TrimSpace(allowed)
		if cleanAllowed == "*" || cleanAllowed == "s3://" {
			return true
		}
		if strings.HasPrefix(prefix, cleanAllowed) {
			return true
		}
	}
	return false
}

func sliceContains(s []string, target string) bool {
	targetLower := strings.ToLower(strings.TrimSpace(target))
	for _, v := range s {
		if strings.ToLower(strings.TrimSpace(v)) == targetLower {
			return true
		}
	}
	return false
}
