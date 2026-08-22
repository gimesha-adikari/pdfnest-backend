package acquisition

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/storage"
)

// ExtractZipArchive securely extracts a staged ZIP archive into the sandbox workspace,
// enforcing strict Zip Slip, Zip Bomb decompression ratio, and volume limits.
func ExtractZipArchive(
	ctx context.Context,
	zipFilePath string,
	sandbox *Sandbox,
	limits AcquisitionLimits,
) (*AcquisitionResult, error) {
	if sandbox == nil || sandbox.IsClosed() {
		return nil, ErrSandboxClosed
	}

	// 0. Resolve canonical storage key or staged path to an absolute local file
	resolvedPath, cleanup, err := storage.ResolveArchive(ctx, zipFilePath)
	if err != nil {
		return nil, fmt.Errorf("resolve zip archive: %w", err)
	}
	defer cleanup()

	// 1. Calculate source artifact SHA-256
	artifactSha, err := engine.HashFileSHA256(resolvedPath)
	if err != nil {
		return nil, fmt.Errorf("hash zip archive: %w", err)
	}

	timeout := limits.ExtractTimeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	extractCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	maxBytes := limits.MaxExtractedBytes
	if maxBytes <= 0 {
		maxBytes = 250 * 1024 * 1024
	}

	maxFiles := limits.MaxFileCount
	if maxFiles <= 0 {
		maxFiles = 25000
	}

	maxRatio := limits.MaxDecompressionRatio
	if maxRatio <= 0 {
		maxRatio = 10.0
	}

	startTime := time.Now()

	// 2. Open ZIP reader
	r, err := zip.OpenReader(resolvedPath)
	if err != nil {
		_ = sandbox.Cleanup()
		return nil, fmt.Errorf("open zip archive: %w", err)
	}
	defer r.Close()

	var totalCompressedBytes int64
	var totalUncompressedBytes int64
	var fileCount int

	for _, f := range r.File {
		select {
		case <-extractCtx.Done():
			_ = sandbox.Cleanup()
			return nil, extractCtx.Err()
		default:
		}

		// 3. Zip Slip & path traversal validation
		rawName := f.Name
		if strings.Contains(rawName, "\x00") || strings.Contains(rawName, ":") {
			_ = sandbox.Cleanup()
			return nil, engine.ErrZipSlipDetected
		}

		// Normalize separators cross-platform (convert Windows backslashes to forward slashes before cleaning)
		slashName := strings.ReplaceAll(rawName, "\\", "/")
		if strings.HasPrefix(slashName, "/") {
			_ = sandbox.Cleanup()
			return nil, engine.ErrZipSlipDetected
		}

		cleanName := filepath.Clean(slashName)
		if cleanName == "." || cleanName == ".." || strings.HasPrefix(cleanName, "../") || strings.Contains(cleanName, "/../") {
			_ = sandbox.Cleanup()
			return nil, engine.ErrZipSlipDetected
		}

		targetPath, err := sandbox.ResolvePath(cleanName)
		if err != nil {
			_ = sandbox.Cleanup()
			return nil, fmt.Errorf("%w: %v", engine.ErrZipSlipDetected, err)
		}

		// 4. Hostile Symlink Rejection
		if (f.Mode()&fs.ModeSymlink) != 0 || (f.FileInfo().Mode()&fs.ModeSymlink) != 0 {
			_ = sandbox.Cleanup()
			return nil, ErrArchiveSymlinkRejected
		}

		// Directory entry
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(targetPath, 0755); err != nil {
				_ = sandbox.Cleanup()
				return nil, fmt.Errorf("create archive directory %s: %w", cleanName, err)
			}
			continue
		}

		// 5. File limit and decompression ratio monitoring (Zip Bomb Protection)
		fileCount++
		if fileCount > maxFiles {
			_ = sandbox.Cleanup()
			return nil, ErrMaxFileCountExceeded
		}

		compSize := int64(f.CompressedSize64)
		uncompSize := int64(f.UncompressedSize64)

		totalCompressedBytes += compSize
		totalUncompressedBytes += uncompSize

		if totalUncompressedBytes > maxBytes {
			_ = sandbox.Cleanup()
			return nil, ErrMaxExtractedSizeExceeded
		}

		// Check decompression ratio once uncompressed content exceeds 10 KB
		effectiveComp := totalCompressedBytes
		if effectiveComp == 0 {
			effectiveComp = 1
		}
		if totalUncompressedBytes > 10*1024 {
			ratio := float64(totalUncompressedBytes) / float64(effectiveComp)
			if ratio > maxRatio {
				_ = sandbox.Cleanup()
				return nil, engine.ErrDecompressionRatioExceeded
			}
		}

		// 6. Safe streaming file extraction
		if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
			_ = sandbox.Cleanup()
			return nil, fmt.Errorf("create parent directory for %s: %w", cleanName, err)
		}

		rc, err := f.Open()
		if err != nil {
			_ = sandbox.Cleanup()
			return nil, fmt.Errorf("open archive entry %s: %w", cleanName, err)
		}

		perm := f.Mode().Perm()
		if perm == 0 {
			perm = 0644
		}
		// Strip SUID/SGID bits for security
		perm = perm & 0777

		destFile, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
		if err != nil {
			rc.Close()
			_ = sandbox.Cleanup()
			return nil, fmt.Errorf("create extracted file %s: %w", cleanName, err)
		}

		// Limit reader prevents stream from exceeding remaining bytes
		remainingQuota := maxBytes - totalUncompressedBytes + uncompSize + 1
		limitReader := io.LimitReader(rc, remainingQuota)

		written, err := io.Copy(destFile, limitReader)
		destFile.Close()
		rc.Close()

		if err != nil {
			_ = sandbox.Cleanup()
			return nil, fmt.Errorf("write extracted file %s: %w", cleanName, err)
		}

		if written > remainingQuota-1 {
			_ = sandbox.Cleanup()
			return nil, ErrMaxExtractedSizeExceeded
		}
	}

	repoName := filepath.Base(zipFilePath)
	repoName = strings.TrimSuffix(repoName, filepath.Ext(repoName))
	duration := time.Since(startTime).Milliseconds()

	return &AcquisitionResult{
		SandboxPath:           sandbox.RootPath,
		SourceType:            engine.SourceTypeZip,
		RepositoryName:        repoName,
		ArchiveSha256:         artifactSha,
		TotalFiles:            fileCount,
		TotalBytes:            totalUncompressedBytes,
		AcquisitionDurationMs: duration,
	}, nil
}
