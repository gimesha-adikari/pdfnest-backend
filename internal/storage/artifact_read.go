package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/minio/minio-go/v7"
)

// Additive strict readback contract for the dormant OCR evidence adapter.
// Existing ResolveObject/DownloadToTemp callers retain their behavior.
type ArtifactReadKind string

const (
	ArtifactMissing     ArtifactReadKind = "missing"
	ArtifactInvalidKey  ArtifactReadKind = "invalid_key"
	ArtifactDenied      ArtifactReadKind = "denied"
	ArtifactUnavailable ArtifactReadKind = "uncertain_or_unavailable"
	ArtifactIntegrity   ArtifactReadKind = "integrity"
	ArtifactTooLarge    ArtifactReadKind = "too_large"
	ArtifactTemporary   ArtifactReadKind = "temporary_file"
)

type ArtifactReadError struct{ Kind ArtifactReadKind }

func (e *ArtifactReadError) Error() string { return "artifact read: " + string(e.Kind) }

// Path is a private, bounded logical-byte snapshot, never the persistent local
// object. Cleanup reports failures and removes only this readback's temp file.
type ArtifactFile struct {
	Path    string
	Cleanup func() error
}

func strictArtifactKey(key string) bool {
	clean, err := sanitizeObjectKey(key)
	return err == nil && clean == key && !strings.ContainsAny(key, "\\:%\x00\r\n")
}
func readError(kind ArtifactReadKind) error { return &ArtifactReadError{Kind: kind} }
func classifyArtifactRead(err error) error {
	if os.IsNotExist(err) {
		return readError(ArtifactMissing)
	}
	if os.IsPermission(err) {
		return readError(ArtifactDenied)
	}
	response := minio.ToErrorResponse(err)
	// A generic HTTP 404 can mean an absent bucket/proxy route, not this object.
	if response.StatusCode == 404 && (response.Code == "NoSuchKey" || response.Code == "NoSuchObject") {
		return readError(ArtifactMissing)
	}
	if response.StatusCode == 401 || response.StatusCode == 403 {
		return readError(ArtifactDenied)
	}
	return readError(ArtifactUnavailable)
}

// ReadArtifact uses the existing selector but never uses raw-path fallbacks or
// converts unknown remote failures into confirmed absence. No deletion API is
// part of this contract. An error is never permission to delete the output.
func ReadArtifact(ctx context.Context, key string, maxBytes int64) (*ArtifactFile, error) {
	if !strictArtifactKey(key) {
		return nil, readError(ArtifactInvalidKey)
	}
	if maxBytes <= 0 || maxBytes > 250*1024*1024 {
		return nil, readError(ArtifactTooLarge)
	}
	if RemoteStorageEnabled() {
		store, err := Default()
		if err != nil || store == nil {
			return nil, readError(ArtifactUnavailable)
		}
		return store.ReadArtifact(ctx, key, maxBytes)
	}
	root, err := os.OpenRoot(GetLocalStorageDir())
	if err != nil {
		return nil, classifyArtifactRead(err)
	}
	defer root.Close()
	file, err := root.Open(key)
	if err != nil {
		return nil, classifyArtifactRead(err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, readError(ArtifactUnavailable)
	}
	return snapshotArtifact(ctx, file, maxBytes, false)
}

// ReadArtifact reads/decrypts through the actual MinIO client and existing GCM
// implementation. Limits cover logical bytes plus the 12-byte nonce/16-byte tag.
// This is not an immutable-object or conditional-write primitive.
func (s *Store) ReadArtifact(ctx context.Context, key string, maxBytes int64) (*ArtifactFile, error) {
	if !strictArtifactKey(key) {
		return nil, readError(ArtifactInvalidKey)
	}
	if maxBytes <= 0 || maxBytes > 250*1024*1024 {
		return nil, readError(ArtifactTooLarge)
	}
	if s == nil || s.client == nil {
		return nil, readError(ArtifactUnavailable)
	}
	object, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, classifyArtifactRead(err)
	}
	return snapshotArtifact(ctx, object, maxBytes, true)
}

type contextArtifactReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextArtifactReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
func snapshotArtifact(ctx context.Context, source io.ReadCloser, maxBytes int64, decrypt bool) (result *ArtifactFile, err error) {
	var temp *os.File
	defer func() {
		if closeErr := source.Close(); closeErr != nil && err == nil {
			err = classifyArtifactRead(closeErr)
		}
		if temp != nil {
			if closeErr := temp.Close(); closeErr != nil && err == nil {
				err = readError(ArtifactTemporary)
			}
			if err != nil {
				if removeErr := os.Remove(temp.Name()); removeErr != nil && !os.IsNotExist(removeErr) {
					err = errors.Join(err, readError(ArtifactTemporary))
				}
				result = nil
			}
		}
	}()
	if ctx.Err() != nil {
		return nil, readError(ArtifactUnavailable)
	}
	temp, err = os.CreateTemp("", "ocr-artifact-read-*.pdf")
	if err != nil {
		return nil, readError(ArtifactTemporary)
	}
	limit := maxBytes
	if decrypt {
		limit += 28
	}
	size, copyErr := io.Copy(temp, io.LimitReader(contextArtifactReader{ctx, source}, limit+1))
	if copyErr != nil {
		return nil, classifyArtifactRead(copyErr)
	}
	if size > limit {
		return nil, readError(ArtifactTooLarge)
	}
	if decrypt {
		// AES-GCM requires the complete bounded ciphertext. Local readback streams;
		// this remote path retains the existing whole-message GCM memory limitation.
		data, readErr := os.ReadFile(temp.Name())
		if readErr != nil {
			return nil, readError(ArtifactTemporary)
		}
		logical, decryptErr := decryptData(data)
		if decryptErr != nil {
			return nil, readError(ArtifactIntegrity)
		}
		if int64(len(logical)) > maxBytes {
			return nil, readError(ArtifactTooLarge)
		}
		if truncateErr := temp.Truncate(0); truncateErr != nil {
			return nil, readError(ArtifactTemporary)
		}
		if _, seekErr := temp.Seek(0, io.SeekStart); seekErr != nil {
			return nil, readError(ArtifactTemporary)
		}
		if _, writeErr := temp.Write(logical); writeErr != nil {
			return nil, readError(ArtifactTemporary)
		}
	}
	path := temp.Name()
	return &ArtifactFile{Path: path, Cleanup: func() error {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return readError(ArtifactTemporary)
		}
		return nil
	}}, nil
}
