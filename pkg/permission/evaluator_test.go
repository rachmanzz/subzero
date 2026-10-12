package permission

import (
	"errors"
	"testing"
)

func TestEvaluate_NilPolicy(t *testing.T) {
	_, err := Evaluate(nil, &ModuleRequest{})
	if !errors.Is(err, ErrNilPolicy) {
		t.Fatalf("expected ErrNilPolicy, got %v", err)
	}
}

func TestEvaluate_NilRequest(t *testing.T) {
	policy := DefaultWorkerPolicy()
	eff, err := Evaluate(&policy, nil)
	if err != nil {
		t.Fatalf("expected no error on nil request, got %v", err)
	}
	if len(eff.HostCalls) != 0 {
		t.Errorf("expected 0 host calls, got %d", len(eff.HostCalls))
	}
	if eff.Files.MaxFileSizeBytes != policy.Files.MaxFileSizeBytes {
		t.Errorf("expected default max file size %d, got %d", policy.Files.MaxFileSizeBytes, eff.Files.MaxFileSizeBytes)
	}
}

func TestEvaluate_ValidRequest(t *testing.T) {
	policy := DefaultWorkerPolicy()
	req := &ModuleRequest{
		HostCalls: []string{"storage:get_object", "logger:debug"},
		Filesystem: RequestFilesystem{
			ReadOnly:  []string{"/tmp/subzero-tenants/tenant-a"},
			ReadWrite: []string{"/tmp/subzero-workspace/scratch"},
		},
		S3: RequestS3{
			AllowedBuckets:  []string{"my-bucket"},
			AllowedPrefixes: []string{"s3://my-bucket/statements/"},
			Operations:      []string{"read"},
		},
		Files: RequestFiles{
			AllowedExtensions: []string{".csv", "json"},
			MaxFileSizeMB:     50,
		},
	}

	eff, err := Evaluate(&policy, req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !eff.HostCalls["storage:get_object"] || !eff.HostCalls["logger:debug"] {
		t.Errorf("expected host calls to be granted")
	}
	if len(eff.Filesystem.ReadOnly) != 1 || eff.Filesystem.ReadOnly[0] != "/tmp/subzero-tenants/tenant-a" {
		t.Errorf("unexpected read-only path: %v", eff.Filesystem.ReadOnly)
	}
	if len(eff.Filesystem.ReadWrite) != 1 || eff.Filesystem.ReadWrite[0] != "/tmp/subzero-workspace/scratch" {
		t.Errorf("unexpected read-write path: %v", eff.Filesystem.ReadWrite)
	}
	if len(eff.Files.AllowedExtensions) != 2 || eff.Files.AllowedExtensions[0] != ".csv" || eff.Files.AllowedExtensions[1] != ".json" {
		t.Errorf("unexpected allowed extensions: %v", eff.Files.AllowedExtensions)
	}
	if eff.Files.MaxFileSizeBytes != 50*1024*1024 {
		t.Errorf("expected max file size 50MB, got %d", eff.Files.MaxFileSizeBytes)
	}
}

func TestEvaluate_UnauthorizedHostCall(t *testing.T) {
	policy := DefaultWorkerPolicy()
	req := &ModuleRequest{
		HostCalls: []string{"os:exec_command"}, // Not allowed
	}
	_, err := Evaluate(&policy, req)
	if !errors.Is(err, ErrUnauthorizedHostCall) {
		t.Fatalf("expected ErrUnauthorizedHostCall, got %v", err)
	}
}

func TestEvaluate_UnauthorizedFilesystemPath(t *testing.T) {
	policy := DefaultWorkerPolicy()
	req := &ModuleRequest{
		Filesystem: RequestFilesystem{
			ReadOnly: []string{"/var/secret/keys"}, // Outside allowed roots
		},
	}
	_, err := Evaluate(&policy, req)
	if !errors.Is(err, ErrUnauthorizedPath) {
		t.Fatalf("expected ErrUnauthorizedPath, got %v", err)
	}
}

func TestEvaluate_SensitiveSystemPath(t *testing.T) {
	// Even if worker roots are permissive "/", sensitive paths must still be rejected
	policy := PermissiveWorkerPolicy()
	req := &ModuleRequest{
		Filesystem: RequestFilesystem{
			ReadOnly: []string{"/etc/passwd"},
		},
	}
	_, err := Evaluate(&policy, req)
	if !errors.Is(err, ErrSensitivePath) {
		t.Fatalf("expected ErrSensitivePath, got %v", err)
	}
}

func TestEvaluate_PathTraversal(t *testing.T) {
	policy := DefaultWorkerPolicy()
	req := &ModuleRequest{
		Filesystem: RequestFilesystem{
			ReadOnly: []string{"/tmp/subzero-tenants/../../etc/passwd"},
		},
	}
	_, err := Evaluate(&policy, req)
	if !errors.Is(err, ErrSensitivePath) && !errors.Is(err, ErrPathTraversal) {
		t.Fatalf("expected traversal or sensitive path rejection, got %v", err)
	}

	// Relative path
	req2 := &ModuleRequest{
		Filesystem: RequestFilesystem{
			ReadOnly: []string{"relative/path"},
		},
	}
	_, err2 := Evaluate(&policy, req2)
	if !errors.Is(err2, ErrPathTraversal) {
		t.Fatalf("expected ErrPathTraversal on relative path, got %v", err2)
	}
}

func TestEvaluate_UnauthorizedS3Prefix(t *testing.T) {
	policy := DefaultWorkerPolicy()
	policy.S3.AllowedPrefixes = []string{"s3://tenant-a-bucket/incoming/"}

	req := &ModuleRequest{
		S3: RequestS3{
			AllowedPrefixes: []string{"s3://tenant-b-bucket/secret/"},
		},
	}
	_, err := Evaluate(&policy, req)
	if !errors.Is(err, ErrUnauthorizedS3Prefix) {
		t.Fatalf("expected ErrUnauthorizedS3Prefix, got %v", err)
	}
}

func TestEvaluate_UnauthorizedFileExtension(t *testing.T) {
	policy := DefaultWorkerPolicy()
	req := &ModuleRequest{
		Files: RequestFiles{
			AllowedExtensions: []string{".exe", ".sh"},
		},
	}
	_, err := Evaluate(&policy, req)
	if !errors.Is(err, ErrUnauthorizedExtension) {
		t.Fatalf("expected ErrUnauthorizedExtension, got %v", err)
	}
}

func TestEvaluate_FileSizeTooLarge(t *testing.T) {
	policy := DefaultWorkerPolicy() // Max 100 MB
	req := &ModuleRequest{
		Files: RequestFiles{
			MaxFileSizeMB: 500, // 500 MB exceeds 100 MB
		},
	}
	_, err := Evaluate(&policy, req)
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("expected ErrFileTooLarge, got %v", err)
	}
}
