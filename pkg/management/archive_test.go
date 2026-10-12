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
