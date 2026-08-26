package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/minio/minio-go/v7"
)

var (
	ErrObjectNotFound = errors.New("storage: object not found")
)

// GetLocalStorageDir returns the absolute directory for local file persistence.
// It is guaranteed to be absolute and independent of caller working directory.
func GetLocalStorageDir() string {
	dir := strings.TrimSpace(os.Getenv("ANALYZER_STORAGE_DIR"))
	if dir == "" {
		dir = strings.TrimSpace(os.Getenv("LOCAL_STORAGE_DIR"))
	}
	if dir == "" {
		// Default to a deterministic directory in /tmp/platen_storage or relative to binary
		dir = filepath.Join(os.TempDir(), "platen_storage")
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	_ = os.MkdirAll(absDir, 0755)
	return absDir
}

// SaveLocalStream writes a stream directly to the local storage directory under the given object key.
// It streams into the file, calculating SHA-256 on the fly without holding the entire payload in memory.
func SaveLocalStream(ctx context.Context, key string, r io.Reader) (int64, string, error) {
	sanitizedKey, err := sanitizeObjectKey(key)
	if err != nil {
		return 0, "", fmt.Errorf("invalid storage key: %w", err)
	}

	targetPath := filepath.Join(GetLocalStorageDir(), filepath.FromSlash(sanitizedKey))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0755); err != nil {
		return 0, "", fmt.Errorf("failed to create storage directory: %w", err)
	}

	// Create temporary staging file first for atomic write
	tmpFile, err := os.CreateTemp(filepath.Dir(targetPath), ".upload-*")
	if err != nil {
		return 0, "", fmt.Errorf("failed to create staging file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath) // cleaned up if rename didn't happen
	}()

	hasher := sha256.New()
	multiWriter := io.MultiWriter(tmpFile, hasher)

	written, err := io.Copy(multiWriter, r)
	if err != nil {
		return 0, "", fmt.Errorf("failed to write upload stream: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return 0, "", fmt.Errorf("failed to sync staging file: %w", err)
	}
	_ = tmpFile.Close()

	// Atomic rename to final target path
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return 0, "", fmt.Errorf("failed to commit storage file: %w", err)
	}

	sha256Hex := hex.EncodeToString(hasher.Sum(nil))
	return written, sha256Hex, nil
}

// DeleteLocalObject removes a canonical object from the local development
// store. It is used only to roll back a failed database registration after a
// source file was safely persisted.
func DeleteLocalObject(key string) error {
	sanitizedKey, err := sanitizeObjectKey(key)
	if err != nil {
		return fmt.Errorf("invalid storage key: %w", err)
	}
	path := filepath.Join(GetLocalStorageDir(), filepath.FromSlash(sanitizedKey))
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ObjectExists verifies whether a canonical storage key exists in local storage or R2.
func ObjectExists(ctx context.Context, key string) bool {
	sanitizedKey, err := sanitizeObjectKey(key)
	if err != nil {
		return false
	}

	// 1. Check local storage directory
	localPath := filepath.Join(GetLocalStorageDir(), filepath.FromSlash(sanitizedKey))
	if fi, err := os.Stat(localPath); err == nil && !fi.IsDir() {
		return true
	}

	// 2. Check direct filesystem path (e.g. for test fixtures)
	if fi, err := os.Stat(key); err == nil && !fi.IsDir() {
		return true
	}
	if abs, err := filepath.Abs(key); err == nil {
		if fi, err := os.Stat(abs); err == nil && !fi.IsDir() {
			return true
		}
	}

	// 3. Check R2 if configured
	store, err := Default()
	if err == nil && store != nil {
		_, statErr := store.client.StatObject(ctx, store.bucket, sanitizedKey, minio.StatObjectOptions{})
		if statErr == nil {
			return true
		}
	}

	return false
}

// ResolveObject resolves a storage key to an absolute local filesystem path.
// If the object is stored in R2, it is downloaded to a secure temporary file.
// The returned cleanup removes only that temporary download, never a local
// storage object. Callers therefore use the same safe resolution path for
// source assets and downloadable outputs.
func ResolveObject(ctx context.Context, key, prefix, suffix string) (string, func(), error) {
	sanitizedKey, err := sanitizeObjectKey(key)
	if err != nil {
		// Fallback check if it is a raw filepath
		if fi, statErr := os.Stat(key); statErr == nil && !fi.IsDir() {
			abs, _ := filepath.Abs(key)
			return abs, func() {}, nil
		}
		return "", nil, fmt.Errorf("invalid storage key: %w", err)
	}

	// 1. Check local storage directory
	localPath := filepath.Join(GetLocalStorageDir(), filepath.FromSlash(sanitizedKey))
	if fi, err := os.Stat(localPath); err == nil && !fi.IsDir() {
		return localPath, func() {}, nil
	}

	// 2. Check if key matches an existing filesystem path directly (e.g., test-corpus/)
	if fi, err := os.Stat(key); err == nil && !fi.IsDir() {
		abs, err := filepath.Abs(key)
		if err == nil {
			return abs, func() {}, nil
		}
	}

	// 3. Check R2 if configured
	store, err := Default()
	if err == nil && store != nil {
		tmpPath, dlErr := store.DownloadToTemp(sanitizedKey, prefix, suffix)
		if dlErr == nil {
			cleanup := func() {
				_ = os.Remove(tmpPath)
			}
			return tmpPath, cleanup, nil
		}
	}

	return "", nil, fmt.Errorf("%w: object key '%s' could not be found locally or in remote storage", ErrObjectNotFound, key)
}

// ResolveArchive is retained for archive consumers. New callers should use
// ResolveObject with an accurate temporary-file suffix.
func ResolveArchive(ctx context.Context, key string) (string, func(), error) {
	return ResolveObject(ctx, key, "pdfnest-archive", ".zip")
}
