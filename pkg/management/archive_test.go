package management_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/rachmanzz/subzero/pkg/management"
)

func createTestTarGz(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		data := []byte(content)
		hdr := &tar.Header{
			Name: name,
			Mode: 0644,
			Size: int64(len(data)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func TestUnpackTarGz_Success(t *testing.T) {
	files := map[string]string{
		"main.go": `package main
func main() {}`,
		"helper/util.go": `package main
func helper() {}`,
		"go.mod": `module testmod
go 1.22`,
	}

	archiveBytes, err := createTestTarGz(files)
	if err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	destDir, err := os.MkdirTemp("", "archive-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(destDir)

	err = management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), destDir, management.DefaultArchiveLimits())
	if err != nil {
		t.Fatalf("SafeUnpackTarGz failed: %v", err)
	}

	// Verify files exist
	for name := range files {
		target := filepath.Join(destDir, filepath.FromSlash(name))
		if _, err := os.Stat(target); err != nil {
			t.Errorf("expected file %s to exist: %v", target, err)
		}
	}
}

func TestUnpackTarGz_ZipSlipPathTraversal(t *testing.T) {
	files := map[string]string{
		"../../evil.go": `package main`,
	}

	archiveBytes, err := createTestTarGz(files)
	if err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	destDir, err := os.MkdirTemp("", "archive-slip-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(destDir)

	err = management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), destDir, management.DefaultArchiveLimits())
	if err == nil {
		t.Fatal("expected zip slip error, got nil")
	}

	if !errors.Is(err, management.ErrZipSlipAttempt) {
		t.Fatalf("expected ErrZipSlipAttempt, got: %v", err)
	}
}

func TestUnpackTarGz_DisallowedExtension(t *testing.T) {
	files := map[string]string{
		"malware.exe": "binary content",
	}

	archiveBytes, err := createTestTarGz(files)
	if err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	destDir, err := os.MkdirTemp("", "archive-ext-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(destDir)

	err = management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), destDir, management.DefaultArchiveLimits())
	if err == nil {
		t.Fatal("expected disallowed extension error, got nil")
	}

	if !errors.Is(err, management.ErrInvalidFileType) {
		t.Fatalf("expected ErrInvalidFileType, got: %v", err)
	}
}

func TestUnpackTarGz_MaxFileCount(t *testing.T) {
	files := map[string]string{
		"f1.go": "package main",
		"f2.go": "package main",
	}

	archiveBytes, err := createTestTarGz(files)
	if err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	destDir, err := os.MkdirTemp("", "archive-maxfiles-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(destDir)

	limits := management.ArchiveLimits{
		MaxFileCount:         1,
		MaxUncompressedBytes: 1024 * 1024,
	}

	err = management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), destDir, limits)
	if err == nil {
		t.Fatal("expected file count exceeded error, got nil")
	}

	if !errors.Is(err, management.ErrTooManyFiles) {
		t.Fatalf("expected ErrTooManyFiles, got: %v", err)
	}
}

func TestUnpackTarGz_MaxBytesDecompressionBomb(t *testing.T) {
	files := map[string]string{
		"large.go": "package main\n" + string(bytes.Repeat([]byte("/"), 2000)),
	}

	archiveBytes, err := createTestTarGz(files)
	if err != nil {
		t.Fatalf("failed to create tar.gz: %v", err)
	}

	destDir, err := os.MkdirTemp("", "archive-maxbytes-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(destDir)

	limits := management.ArchiveLimits{
		MaxFileCount:         10,
		MaxUncompressedBytes: 500,
	}

	err = management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), destDir, limits)
	if err == nil {
		t.Fatal("expected decompression bomb size error, got nil")
	}

	if !errors.Is(err, management.ErrArchiveTooLarge) {
		t.Fatalf("expected ErrArchiveTooLarge, got: %v", err)
	}
}

func TestPackTarGz(t *testing.T) {
	srcDir, err := os.MkdirTemp("", "archive-pack-src-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(srcDir)

	_ = os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\nfunc main() {}"), 0644)
	_ = os.WriteFile(filepath.Join(srcDir, "go.mod"), []byte("module mymod\ngo 1.22"), 0644)
	_ = os.WriteFile(filepath.Join(srcDir, "ignored.bin"), []byte("binary payload"), 0644) // should be ignored

	tarGzBytes, err := management.PackTarGz(srcDir)
	if err != nil {
		t.Fatalf("PackTarGz failed: %v", err)
	}
	if len(tarGzBytes) == 0 {
		t.Fatal("expected non-empty archive bytes")
	}

	destDir, err := os.MkdirTemp("", "archive-pack-dest-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(destDir)

	err = management.SafeUnpackTarGz(bytes.NewReader(tarGzBytes), destDir, management.DefaultArchiveLimits())
	if err != nil {
		t.Fatalf("SafeUnpackTarGz failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(destDir, "main.go")); err != nil {
		t.Errorf("main.go not found in unpacked archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "go.mod")); err != nil {
		t.Errorf("go.mod not found in unpacked archive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(destDir, "ignored.bin")); !os.IsNotExist(err) {
		t.Errorf("ignored.bin should not have been packed")
	}
}

func TestUnpackTarGz_ZipSlipVariants(t *testing.T) {
	// Test variants of directory traversal: Windows backslashes, absolute paths, nested traversal
	variants := []struct {
		name      string
		entryName string
	}{
		{"Absolute path", "/etc/passwd.go"},
		{"Nested traversal", "foo/../../bar.go"},
		{"Deep nested traversal", "a/b/../../../etc/shadow.go"},
		{"Windows backslash traversal", `..\..\evil.go`},
		{"Windows absolute backslash", `C:\Windows\System32\cmd.go`},
	}

	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			archiveBytes, err := createTestTarGz(map[string]string{
				v.entryName: "package main\nfunc main() {}",
			})
			if err != nil {
				t.Fatalf("failed to create tar: %v", err)
			}

			destDir := t.TempDir()
			err = management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), destDir, management.DefaultArchiveLimits())
			if err == nil {
				t.Fatalf("expected Zip-Slip rejection for entry %q, got nil", v.entryName)
			}
			if !errors.Is(err, management.ErrZipSlipAttempt) {
				t.Logf("entry %q was rejected with: %v", v.entryName, err)
			}
		})
	}
}

func TestUnpackTarGz_SymlinkAndSpecialHeadersIgnored(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	// Add regular file
	regHdr := &tar.Header{
		Name:     "main.go",
		Mode:     0644,
		Size:     int64(len("package main\nfunc main() {}")),
		Typeflag: tar.TypeReg,
	}
	_ = tw.WriteHeader(regHdr)
	_, _ = tw.Write([]byte("package main\nfunc main() {}"))

	// Add symlink entry pointing outside
	symHdr := &tar.Header{
		Name:     "symlink_attack",
		Linkname: "/etc/passwd",
		Typeflag: tar.TypeSymlink,
	}
	_ = tw.WriteHeader(symHdr)

	// Add hardlink entry
	linkHdr := &tar.Header{
		Name:     "link_attack",
		Linkname: "main.go",
		Typeflag: tar.TypeLink,
	}
	_ = tw.WriteHeader(linkHdr)

	_ = tw.Close()
	_ = gw.Close()

	destDir := t.TempDir()
	err := management.SafeUnpackTarGz(bytes.NewReader(buf.Bytes()), destDir, management.DefaultArchiveLimits())
	if err != nil {
		t.Fatalf("SafeUnpackTarGz should succeed while ignoring symlinks/hardlinks, got: %v", err)
	}

	// Verify symlink was skipped and not created
	if _, err := os.Lstat(filepath.Join(destDir, "symlink_attack")); !os.IsNotExist(err) {
		t.Fatal("symlink_attack should have been ignored and not created")
	}
	if _, err := os.Lstat(filepath.Join(destDir, "link_attack")); !os.IsNotExist(err) {
		t.Fatal("link_attack should have been ignored and not created")
	}
	// Verify main.go was extracted
	if _, err := os.Stat(filepath.Join(destDir, "main.go")); err != nil {
		t.Fatalf("main.go should exist: %v", err)
	}
}

func TestUnpackTarGz_ExactByteLimitBoundary(t *testing.T) {
	content := "package main\nfunc main() {}"
	exactLen := int64(len(content))

	archiveBytes, err := createTestTarGz(map[string]string{
		"main.go": content,
	})
	if err != nil {
		t.Fatal(err)
	}

	destDir := t.TempDir()
	// When limit == exactLen, unpacking exactly exactLen bytes should succeed
	err = management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), destDir, management.ArchiveLimits{
		MaxFileCount:         10,
		MaxUncompressedBytes: exactLen,
	})
	if err != nil {
		t.Fatalf("expected unpack to succeed at exact limit boundary (%d bytes), got: %v", exactLen, err)
	}

	// When limit == exactLen - 1, unpacking should fail
	destDir2 := t.TempDir()
	err = management.SafeUnpackTarGz(bytes.NewReader(archiveBytes), destDir2, management.ArchiveLimits{
		MaxFileCount:         10,
		MaxUncompressedBytes: exactLen - 1,
	})
	if err == nil {
		t.Fatal("expected unpack to fail when size exceeds limit by 1 byte")
	}
	if !errors.Is(err, management.ErrArchiveTooLarge) {
		t.Fatalf("expected ErrArchiveTooLarge, got: %v", err)
	}
}
