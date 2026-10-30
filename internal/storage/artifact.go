package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// The legacy AES-GCM envelope needs a contiguous buffer. This is a buffer
// budget, not an output-size policy: plaintext artifacts stream at any admitted
// size, with I/O limited by the recorded length and an independent deadline.
const MaxBufferedArtifactBytes int64 = 250 << 20
const artifactStorageTimeout = 30 * time.Second

var ErrArtifactIntegrity = errors.New("artifact content does not match its recorded identity")
var ErrArtifactCapacity = errors.New("encrypted artifact exceeds bounded verification capacity")

// The existing AES-GCM envelope needs the full bounded object. Serialize these
// buffers per process (at most two 250 MiB buffers during upload). Plain local
// verification and source staging use only a streaming copy buffer.
var artifactBuffers = make(chan struct{}, 1)

func CanonicalObjectKey(key string) bool {
	clean, err := sanitizeObjectKey(key)
	return err == nil && clean == key && !strings.Contains(key, "\\")
}

func artifactIdentity(digest string, size int64) bool {
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == sha256.Size && digest == strings.ToLower(digest) && size >= 0 && size < math.MaxInt64-29
}

type artifactReader struct {
	ctx context.Context
	r   io.Reader
}

func (r artifactReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// StageArtifactSource captures once, before recording identity. Uploading this
// private snapshot rather than reopening the caller's source closes hash/write
// mutation races. This is disposable generated output, not durable raw input.
func StageArtifactSource(ctx context.Context, source string) (path, digest string, size int64, cleanup func(), err error) {
	ctx, cancel := context.WithTimeout(ctx, artifactStorageTimeout)
	defer cancel()
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() >= math.MaxInt64-29 {
		return "", "", 0, nil, fmt.Errorf("artifact source unavailable")
	}
	f, err := os.Open(source)
	if err != nil {
		return "", "", 0, nil, err
	}
	defer f.Close()
	tmp, err := os.CreateTemp("", "task-artifact-snapshot-*")
	if err != nil {
		return "", "", 0, nil, err
	}
	cleanup = func() { _ = os.Remove(tmp.Name()) }
	h := sha256.New()
	size, err = io.Copy(io.MultiWriter(tmp, h), io.LimitReader(artifactReader{ctx, f}, info.Size()+1))
	if err == nil && size != info.Size() {
		err = ErrArtifactIntegrity
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return "", "", 0, nil, err
	}
	return tmp.Name(), hex.EncodeToString(h.Sum(nil)), size, cleanup, nil
}

// VerifyArtifact hashes actual application bytes from the canonical store.
// No ETag, metadata checksum, cwd fallback or existence check is charge proof.
func VerifyArtifact(ctx context.Context, key, digest string, size int64) error {
	return readVerifiedArtifact(ctx, key, digest, size, io.Discard)
}

func ResolveVerifiedArtifact(ctx context.Context, key, digest string, size int64) (string, func(), error) {
	tmp, err := os.CreateTemp("", "task-artifact-recovery-*")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(tmp.Name()) }
	err = readVerifiedArtifact(ctx, key, digest, size, tmp)
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return "", nil, err
	}
	return tmp.Name(), cleanup, nil
}

func readVerifiedArtifact(ctx context.Context, key, digest string, size int64, dst io.Writer) error {
	if !CanonicalObjectKey(key) || !artifactIdentity(digest, size) {
		return ErrArtifactIntegrity
	}
	ctx, cancel := context.WithTimeout(ctx, artifactStorageTimeout)
	defer cancel()
	var reader io.ReadCloser
	if !RemoteStorageEnabled() {
		root, err := os.OpenRoot(GetLocalStorageDir())
		if err != nil {
			return err
		}
		defer root.Close()
		// Reject symlink aliases, including parents. OpenRoot also confines any
		// concurrent path replacement to this canonical storage root.
		parts := strings.Split(key, "/")
		for i := range parts {
			info, err := root.Lstat(filepath.Join(parts[:i+1]...))
			if os.IsNotExist(err) {
				return ErrObjectNotFound
			}
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 || (i == len(parts)-1 && !info.Mode().IsRegular()) {
				return ErrArtifactIntegrity
			}
		}
		f, err := root.Open(key)
		if os.IsNotExist(err) {
			return ErrObjectNotFound
		}
		if err != nil {
			return err
		}
		reader = f
	} else {
		store, err := Default()
		if err != nil {
			return err
		}
		if store == nil {
			return errors.New("artifact storage unavailable")
		}
		reader, err = store.client.GetObject(ctx, store.bucket, key, minio.GetObjectOptions{})
		if err != nil {
			return artifactReadError(err)
		}
	}
	defer reader.Close()
	if RemoteStorageEnabled() && getEncryptionKey() != nil {
		if size > MaxBufferedArtifactBytes {
			return ErrArtifactCapacity
		}
		if err := acquireArtifactBuffer(ctx); err != nil {
			return err
		}
		defer func() { <-artifactBuffers }()
		// Existing envelope: 12-byte nonce plus 16-byte GCM tag. The extra
		// byte detects excess even when provider length metadata is dishonest.
		data := make([]byte, size+28+1)
		n, err := io.ReadFull(artifactReader{ctx, reader}, data)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return artifactReadError(err)
		}
		data = data[:n]
		// Legacy plaintext is safe here only with exact recorded length+SHA.
		if int64(n) != size || artifactDigest(data) != digest {
			if int64(n) != size+28 {
				return ErrArtifactIntegrity
			}
			data, err = decryptData(data)
			if err != nil || int64(len(data)) != size || artifactDigest(data) != digest {
				return ErrArtifactIntegrity
			}
		}
		_, err = io.Copy(dst, bytes.NewReader(data))
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(artifactReader{ctx, reader}, size+1))
	if err != nil {
		return artifactReadError(err)
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return ErrArtifactIntegrity
	}
	return ctx.Err()
}

func artifactDigest(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func artifactReadError(err error) error {
	if os.IsNotExist(err) || isMissingObjectError(err) {
		return ErrObjectNotFound
	}
	return err
}

func acquireArtifactBuffer(ctx context.Context) error {
	select {
	case artifactBuffers <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SaveVerifiedArtifact uses the private snapshot and preserves the existing
// optional AES-GCM format, but bounds source reads and remote I/O explicitly.
// The caller owns row serialization and must only write a proven missing key.
func SaveVerifiedArtifact(ctx context.Context, key, source, digest string, size int64, contentType string) error {
	if !CanonicalObjectKey(key) || !artifactIdentity(digest, size) {
		return ErrArtifactIntegrity
	}
	ctx, cancel := context.WithTimeout(ctx, artifactStorageTimeout)
	defer cancel()
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	defer file.Close()
	if !RemoteStorageEnabled() || getEncryptionKey() == nil {
		h := sha256.New()
		n, err := io.Copy(h, io.LimitReader(artifactReader{ctx, file}, size+1))
		if err != nil {
			return err
		}
		if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
			return ErrArtifactIntegrity
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if !RemoteStorageEnabled() {
			_, _, err := SaveLocalStream(ctx, key, io.LimitReader(artifactReader{ctx, file}, size))
			return err
		}
		store, err := Default()
		if err != nil {
			return err
		}
		if store == nil {
			return errors.New("artifact storage unavailable")
		}
		_, err = store.client.PutObject(ctx, store.bucket, key, io.LimitReader(artifactReader{ctx, file}, size), size, minio.PutObjectOptions{ContentType: contentType})
		return err
	}
	if size > MaxBufferedArtifactBytes {
		return ErrArtifactCapacity
	}
	if err := acquireArtifactBuffer(ctx); err != nil {
		return err
	}
	defer func() { <-artifactBuffers }()
	data := make([]byte, size+1)
	n, err := io.ReadFull(artifactReader{ctx, file}, data)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	data = data[:n]
	if int64(n) != size || artifactDigest(data) != digest {
		return ErrArtifactIntegrity
	}
	store, err := Default()
	if err != nil {
		return err
	}
	if store == nil {
		return errors.New("artifact storage unavailable")
	}
	encrypted, err := encryptData(data)
	if err != nil {
		return err
	}
	_, err = store.client.PutObject(ctx, store.bucket, key, bytes.NewReader(encrypted), int64(len(encrypted)), minio.PutObjectOptions{ContentType: contentType})
	return err
}
