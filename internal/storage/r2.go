package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

type Store struct {
	client *minio.Client
	bucket string
}

var (
	defaultStore *Store
	defaultErr   error
	once         sync.Once
)

func Default() (*Store, error) {
	once.Do(func() {
		defaultStore, defaultErr = newFromEnv()
	})
	return defaultStore, defaultErr
}

func newFromEnv() (*Store, error) {
	bucket := strings.TrimSpace(os.Getenv("R2_BUCKET"))
	accessKey := strings.TrimSpace(os.Getenv("R2_ACCESS_KEY"))
	secretKey := strings.TrimSpace(os.Getenv("R2_SECRET_KEY"))
	endpointRaw := strings.TrimSpace(os.Getenv("R2_ENDPOINT"))

	if bucket == "" {
		return nil, fmt.Errorf("R2_BUCKET is missing")
	}
	if accessKey == "" {
		return nil, fmt.Errorf("R2_ACCESS_KEY is missing")
	}
	if secretKey == "" {
		return nil, fmt.Errorf("R2_SECRET_KEY is missing")
	}
	if endpointRaw == "" {
		return nil, fmt.Errorf("R2_ENDPOINT is missing")
	}

	endpoint, secure, err := parseEndpoint(endpointRaw)
	if err != nil {
		return nil, err
	}

	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("create r2 client: %w", err)
	}

	return &Store{
		client: client,
		bucket: bucket,
	}, nil
}

func parseEndpoint(raw string) (host string, secure bool, err error) {
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", false, err
		}
		if u.Host == "" {
			return "", false, fmt.Errorf("invalid R2_ENDPOINT: %s", raw)
		}
		return u.Host, u.Scheme == "https", nil
	}
	return raw, true, nil
}

func BuildKey(prefix, ext string) string {
	prefix = strings.Trim(prefix, "/ ")
	ext = strings.TrimSpace(ext)
	if ext == "" {
		ext = ".bin"
	}
	if !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return filepath.ToSlash(filepath.Join(prefix, uuid.NewString()+ext))
}

func (s *Store) UploadFile(path, key, contentType string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return err
	}

	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(path))
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err = s.client.PutObject(
		context.Background(),
		s.bucket,
		key,
		f,
		stat.Size(),
		minio.PutObjectOptions{ContentType: contentType},
	)
	if err != nil {
		return fmt.Errorf("upload to r2 failed: %w", err)
	}
	return nil
}

func (s *Store) DownloadToTemp(key, prefix, suffix string) (string, error) {
	if suffix == "" {
		suffix = ".bin"
	}
	if !strings.HasPrefix(suffix, ".") {
		suffix = "." + suffix
	}

	tmp, err := os.CreateTemp("", prefix+"-*"+suffix)
	if err != nil {
		return "", err
	}
	defer tmp.Close()

	obj, err := s.client.GetObject(context.Background(), s.bucket, key, minio.GetObjectOptions{})
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("download from r2 failed: %w", err)
	}
	defer obj.Close()

	if _, err := io.Copy(tmp, obj); err != nil {
		_ = os.Remove(tmp.Name())
		return "", fmt.Errorf("copy r2 object to temp failed: %w", err)
	}

	return tmp.Name(), nil
}

func (s *Store) UploadBytes(data []byte, key, contentType string) error {
	reader := bytes.NewReader(data)

	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err := s.client.PutObject(
		context.Background(),
		s.bucket,
		key,
		reader,
		int64(len(data)),
		minio.PutObjectOptions{
			ContentType: contentType,
		},
	)
	if err != nil {
		return fmt.Errorf("upload bytes to r2 failed: %w", err)
	}

	return nil
}
