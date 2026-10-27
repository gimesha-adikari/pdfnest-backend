package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"pdfnest-backend/internal/studiosource"
)

var (
	ErrStudioPDFTooLarge    = errors.New("Studio PDF source exceeds admission limit")
	ErrStudioPDFChanged     = errors.New("Studio source changed during acquisition")
	ErrStudioPDFUnavailable = errors.New("Studio source storage unavailable")
	studioPDFSlots          = make(chan struct{}, 4)
)

const studioStorageTimeout = 2 * time.Minute

type studioContextReader struct {
	ctx    context.Context
	source io.Reader
}

func (r studioContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(b)
}

// StageStudioPDF reads an uploaded raw PDF into a private bounded snapshot.
// Presigned uploads are raw document bytes, not backend encrypted envelopes.
// No generic ResolveObject/DownloadToTemp fallback or unbounded ReadAll is used.
// A HEAD can reject early, but never replaces the bounded actual-byte read.
func StageStudioPDF(ctx context.Context, key string) (path string, cleanup func(), err error) {
	clean, e := sanitizeObjectKey(key)
	if e != nil || clean != key {
		return "", nil, ErrStudioPDFUnavailable
	}
	select {
	case studioPDFSlots <- struct{}{}:
	default:
		return "", nil, ErrStudioPDFUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, studioStorageTimeout)
	var snapshotPath string
	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			if snapshotPath != "" {
				_ = os.Remove(snapshotPath)
			}
			<-studioPDFSlots
		})
	}
	defer func() {
		if err != nil {
			release()
		}
	}()
	var source io.ReadCloser
	var expected int64
	var unchanged func() error
	if !RemoteStorageEnabled() {
		root, openErr := os.OpenRoot(GetLocalStorageDir())
		if openErr != nil {
			return "", nil, ErrStudioPDFUnavailable
		}
		defer root.Close()
		file, openErr := root.Open(key)
		if openErr != nil {
			if errors.Is(openErr, os.ErrNotExist) {
				return "", nil, ErrObjectNotFound
			}
			return "", nil, ErrStudioPDFUnavailable
		}
		source = file
		info, statErr := file.Stat()
		if statErr != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			return "", nil, ErrStudioPDFUnavailable
		}
		expected = info.Size()
		unchanged = func() error {
			after, e := root.Stat(key)
			if e != nil || !os.SameFile(info, after) || after.Size() != info.Size() || after.ModTime() != info.ModTime() {
				return ErrStudioPDFChanged
			}
			return nil
		}
	} else {
		store, storeErr := Default()
		if storeErr != nil || store == nil {
			return "", nil, ErrStudioPDFUnavailable
		}
		info, statErr := store.client.StatObject(ctx, store.bucket, key, minio.StatObjectOptions{})
		if statErr != nil {
			return "", nil, studioObjectError(statErr)
		}
		if info.Size > studiosource.MaxPDFBytes {
			return "", nil, ErrStudioPDFTooLarge
		}
		if info.ETag == "" || info.Size < 0 {
			return "", nil, ErrStudioPDFUnavailable
		}
		options := minio.GetObjectOptions{}
		if e := options.SetMatchETag(info.ETag); e != nil {
			return "", nil, ErrStudioPDFUnavailable
		}
		object, getErr := store.client.GetObject(ctx, store.bucket, key, options)
		if getErr != nil {
			return "", nil, studioObjectError(getErr)
		}
		source = object
		expected = info.Size
		unchanged = func() error {
			actual, e := object.Stat()
			if e != nil {
				return studioObjectError(e)
			}
			if actual.ETag != info.ETag || actual.Size != info.Size {
				return ErrStudioPDFChanged
			}
			return nil
		}
	}
	defer source.Close()
	stop := context.AfterFunc(ctx, func() { _ = source.Close() })
	defer stop()
	if expected > studiosource.MaxPDFBytes {
		return "", nil, ErrStudioPDFTooLarge
	}
	path, err = stageStudioPDF(ctx, source, studiosource.MaxPDFBytes, expected)
	if err != nil {
		return "", nil, studioObjectError(err)
	}
	snapshotPath = path
	if err = unchanged(); err != nil {
		return "", nil, err
	}
	return path, release, nil
}

// At most limit+1 bytes are acquired; the extra byte detects a lying/growing
// source. Partial files are removed on every failure, including cancellation.
func stageStudioPDF(ctx context.Context, source io.Reader, limit, expected int64) (path string, err error) {
	file, err := os.CreateTemp("", "platen-studio-source-*.pdf")
	if err != nil {
		return "", err
	}
	path = file.Name()
	defer func() {
		_ = file.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	n, err := io.CopyBuffer(file, io.LimitReader(studioContextReader{ctx, source}, limit+1), make([]byte, 32<<10))
	if err != nil {
		return path, err
	}
	if err = ctx.Err(); err != nil {
		return path, err
	}
	if n > limit {
		return path, ErrStudioPDFTooLarge
	}
	if n != expected {
		return path, ErrStudioPDFChanged
	}
	if err = file.Close(); err != nil {
		return path, err
	}
	return path, nil
}

func studioObjectError(err error) error {
	switch {
	case errors.Is(err, ErrStudioPDFTooLarge), errors.Is(err, ErrStudioPDFChanged):
		return err
	case errors.Is(err, os.ErrNotExist):
		return ErrObjectNotFound
	}
	switch minio.ToErrorResponse(err).Code {
	case "NoSuchKey", "NoSuchObject", "NotFound":
		return ErrObjectNotFound
	case "PreconditionFailed":
		return ErrStudioPDFChanged
	default:
		return ErrStudioPDFUnavailable
	}
}

// SaveStudioPDF publishes only a fresh server-owned Studio source key. It
// preserves the existing storage encryption contract with a bounded plaintext
// read and a context deadline; other storage writers retain their semantics.
func SaveStudioPDF(ctx context.Context, key, path string) error {
	clean, err := sanitizeObjectKey(key)
	if err != nil || clean != key {
		return ErrStudioPDFUnavailable
	}
	if !strings.HasPrefix(key, "studio/sources/") && !strings.HasPrefix(key, "studio/merge-sources/") {
		return ErrStudioPDFUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, studioStorageTimeout)
	defer cancel()
	file, err := os.Open(path)
	if err != nil {
		return ErrStudioPDFUnavailable
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return ErrStudioPDFUnavailable
	}
	if info.Size() > studiosource.MaxPDFBytes {
		return ErrStudioPDFTooLarge
	}
	if !RemoteStorageEnabled() {
		reader := &studioPublishReader{source: io.LimitReader(studioContextReader{ctx, file}, studiosource.MaxPDFBytes+1)}
		_, _, err := SaveLocalStream(ctx, key, reader)
		return err
	}
	data, err := io.ReadAll(io.LimitReader(studioContextReader{ctx, file}, studiosource.MaxPDFBytes+1))
	if err != nil || ctx.Err() != nil {
		return ErrStudioPDFUnavailable
	}
	if int64(len(data)) > studiosource.MaxPDFBytes {
		return ErrStudioPDFTooLarge
	}
	encrypted, err := encryptData(data)
	if err != nil {
		return ErrStudioPDFUnavailable
	}
	store, err := Default()
	if err != nil || store == nil {
		return ErrStudioPDFUnavailable
	}
	_, err = store.client.PutObject(ctx, store.bucket, key, bytes.NewReader(encrypted), int64(len(encrypted)), minio.PutObjectOptions{ContentType: "application/pdf"})
	if err != nil {
		return ErrStudioPDFUnavailable
	}
	return nil
}

// Report oversize as a copy error, before SaveLocalStream's atomic publication.
type studioPublishReader struct {
	source io.Reader
	count  int64
}

func (r *studioPublishReader) Read(b []byte) (int, error) {
	n, err := r.source.Read(b)
	r.count += int64(n)
	if r.count > studiosource.MaxPDFBytes {
		return 0, ErrStudioPDFTooLarge
	}
	return n, err
}
