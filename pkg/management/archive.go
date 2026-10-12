package management

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var (
	ErrZipSlipAttempt       = errors.New("security: zip-slip path traversal attempt detected")
	ErrArchiveTooLarge      = errors.New("security: unpacked archive size exceeds maximum limit")
	ErrTooManyFiles         = errors.New("security: archive contains too many files")
	ErrMissingMainEntrypoint = errors.New("structure: archive must contain a main package with main() entrypoint")
	ErrInvalidFileType      = errors.New("structure: forbidden file type in archive (only .go, go.mod, go.sum allowed)")
)

// ArchiveLimits configures safety thresholds against decompression bombs.
type ArchiveLimits struct {
	MaxUncompressedBytes int64 // Default: 25 MB
	MaxFileCount         int   // Default: 50 files
}

// DefaultArchiveLimits returns sensible default constraints.
func DefaultArchiveLimits() ArchiveLimits {
	return ArchiveLimits{
		MaxUncompressedBytes: 25 * 1024 * 1024, // 25 MB
		MaxFileCount:         50,
	}
}

// SafeUnpackTarGz safely unpacks a .tar.gz archive into destDir with Zip-Slip and bomb protection.
func SafeUnpackTarGz(r io.Reader, destDir string, limits ArchiveLimits) error {
	if limits.MaxUncompressedBytes == 0 {
		limits = DefaultArchiveLimits()
	}

	gzReader, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("archive: invalid gzip stream: %w", err)
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)

	destClean := filepath.Clean(destDir)
	var totalBytes int64
	var fileCount int

	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("archive: corrupt tar stream: %w", err)
		}

		fileCount++
		if fileCount > limits.MaxFileCount {
			return fmt.Errorf("%w: maximum allowed is %d", ErrTooManyFiles, limits.MaxFileCount)
		}

		// Prevent Zip-Slip: Convert Windows backslashes and check traversal
		normalizedName := strings.ReplaceAll(header.Name, "\\", "/")
		if strings.HasPrefix(normalizedName, "/") || strings.Contains(normalizedName, ":") {
			return fmt.Errorf("%w: %s", ErrZipSlipAttempt, header.Name)
		}
		for _, part := range strings.Split(normalizedName, "/") {
			if part == ".." {
				return fmt.Errorf("%w: %s", ErrZipSlipAttempt, header.Name)
			}
		}

		target := filepath.Join(destDir, filepath.FromSlash(normalizedName))
		targetClean := filepath.Clean(target)
		if !strings.HasPrefix(targetClean, destClean+string(filepath.Separator)) && targetClean != destClean {
			return fmt.Errorf("%w: %s", ErrZipSlipAttempt, header.Name)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(targetClean, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			// Whitelist allowed file extensions (.go, go.mod, go.sum, config.zero, .md, .txt, .json)
			ext := strings.ToLower(filepath.Ext(targetClean))
			base := filepath.Base(targetClean)
			if ext != ".go" && base != "go.mod" && base != "go.sum" && ext != ".md" && ext != ".txt" && ext != ".json" && base != "config.zero" && ext != ".zero" {
				return fmt.Errorf("%w: %s (only source code and configuration files permitted)", ErrInvalidFileType, base)
			}

			if err := os.MkdirAll(filepath.Dir(targetClean), 0755); err != nil {
				return err
			}

			outFile, err := os.OpenFile(targetClean, os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0644)
			if err != nil {
				return err
			}

			// Copy with byte count tracking
			written, err := io.Copy(outFile, io.LimitReader(tarReader, limits.MaxUncompressedBytes-totalBytes+1))
			_ = outFile.Close()
			if err != nil {
				return err
			}

			totalBytes += written
			if totalBytes > limits.MaxUncompressedBytes {
				return fmt.Errorf("%w: exceeded %d bytes", ErrArchiveTooLarge, limits.MaxUncompressedBytes)
			}
		default:
			// Ignore unsupported/dangerous header types like symlinks, pipes, device files
			continue
		}
	}

	return nil
}

// PackTarGz walks sourceDir and packs all source files (.go, go.mod, go.sum, .md, .txt, .json) into a .tar.gz byte buffer.
func PackTarGz(sourceDir string) ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	cleanSource := filepath.Clean(sourceDir)

	err := filepath.Walk(cleanSource, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(cleanSource, path)
		if err != nil {
			return err
		}
		slashPath := filepath.ToSlash(relPath)

		ext := strings.ToLower(filepath.Ext(path))
		base := filepath.Base(path)
		if ext != ".go" && base != "go.mod" && base != "go.sum" && ext != ".md" && ext != ".txt" && ext != ".json" && base != "config.zero" && ext != ".zero" {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("archive: read file %s: %w", relPath, err)
		}

		hdr := &tar.Header{
			Name:    slashPath,
			Mode:    0644,
			Size:    int64(len(data)),
			ModTime: info.ModTime(),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("archive: write header %s: %w", relPath, err)
		}
		if _, err := tw.Write(data); err != nil {
			return fmt.Errorf("archive: write body %s: %w", relPath, err)
		}
		return nil
	})

	if err != nil {
		_ = tw.Close()
		_ = gw.Close()
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
