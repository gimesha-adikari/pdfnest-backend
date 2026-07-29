package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

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

type PresignFile struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	Type string `json:"type"`
	Key  string `json:"key"`
}

type PresignRequest struct {
	Purpose string        `json:"purpose"`
	Prefix  string        `json:"prefix"`
	Files   []PresignFile `json:"files"`
}

type PresignItem struct {
	Name      string            `json:"name"`
	Size      int64             `json:"size"`
	Type      string            `json:"type"`
	Key       string            `json:"key"`
	UploadURL string            `json:"uploadUrl"`
	Method    string            `json:"method"`
	Headers   map[string]string `json:"headers,omitempty"`
}

type PresignResponse struct {
	Files []PresignItem `json:"files"`
}

func sanitizeObjectKey(raw string) (string, error) {
	key := strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/"))
	if key == "" {
		return "", fmt.Errorf("empty object key")
	}

	key = path.Clean("/" + key)
	key = strings.TrimPrefix(key, "/")

	if key == "" || key == "." || strings.Contains(key, "..") {
		return "", fmt.Errorf("invalid object key")
	}

	return key, nil
}

func (s *Store) PresignPutObject(
	ctx context.Context,
	key string,
	_ string, // contentType no longer used
	expires time.Duration,
) (string, error) {
	key, err := sanitizeObjectKey(key)
	if err != nil {
		return "", err
	}

	if expires <= 0 {
		expires = 15 * time.Minute
	}

	u, err := s.client.PresignedPutObject(
		ctx,
		s.bucket,
		key,
		expires,
	)
	if err != nil {
		return "", fmt.Errorf("failed to presign put object: %w", err)
	}

	return u.String(), nil
}

func (s *Store) PresignUploads(ctx context.Context, req PresignRequest, expires time.Duration) (*PresignResponse, error) {
	if len(req.Files) == 0 {
		return nil, fmt.Errorf("no files provided")
	}

	prefix, err := sanitizeObjectKey(req.Prefix)
	if err != nil {
		return nil, fmt.Errorf("invalid prefix: %w", err)
	}

	if expires <= 0 {
		expires = 15 * time.Minute
	}

	out := make([]PresignItem, 0, len(req.Files))

	for _, file := range req.Files {
		key := strings.TrimSpace(file.Key)
		if key == "" {
			key = BuildKey(prefix, filepath.Ext(file.Name))
		}

		key, err := sanitizeObjectKey(key)
		if err != nil {
			return nil, fmt.Errorf("invalid key for %q: %w", file.Name, err)
		}

		if key != prefix && !strings.HasPrefix(key, prefix+"/") {
			return nil, fmt.Errorf("object key %q is outside prefix %q", key, prefix)
		}

		uploadURL, err := s.PresignPutObject(ctx, key, file.Type, expires)
		if err != nil {
			return nil, fmt.Errorf("failed to presign upload for %q: %w", file.Name, err)
		}

		headers := map[string]string{}

		out = append(out, PresignItem{
			Name:      file.Name,
			Size:      file.Size,
			Type:      file.Type,
			Key:       key,
			UploadURL: uploadURL,
			Method:    "PUT",
			Headers:   headers,
		})
	}

	return &PresignResponse{Files: out}, nil
}
