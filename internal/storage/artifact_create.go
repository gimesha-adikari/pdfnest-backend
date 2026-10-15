package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
)

// Additive, dormant OCR-only contract. An error never authorizes output deletion.
type ArtifactCreateKind string

const (
	ArtifactCreateInvalid     ArtifactCreateKind = "invalid_key_or_source"
	ArtifactCreateExists      ArtifactCreateKind = "already_exists"
	ArtifactCreateConflict    ArtifactCreateKind = "conditional_conflict"
	ArtifactCreateDenied      ArtifactCreateKind = "denied"
	ArtifactCreateUnsupported ArtifactCreateKind = "exclusive_create_unsupported"
	ArtifactCreateNotApplied  ArtifactCreateKind = "not_applied"
	ArtifactCreateUncertain   ArtifactCreateKind = "uncertain_retain_intent"
)

type ArtifactCreateError struct{ Kind ArtifactCreateKind }

func (e *ArtifactCreateError) Error() string    { return "artifact create: " + string(e.Kind) }
func createError(kind ArtifactCreateKind) error { return &ArtifactCreateError{Kind: kind} }

// This structural check does not establish task authority. The private OCR
// adapter also checks its captured owner/task/claim/revision-bound intent.
func privateArtifactKey(key string) bool {
	parts := strings.Split(key, "/")
	if len(parts) != 4 || parts[0] != "ocr-finalization" || parts[1] != "outputs" || len(parts[2]) != 64 {
		return false
	}
	digest, err := hex.DecodeString(parts[2])
	if err != nil || hex.EncodeToString(digest) != parts[2] {
		return false
	}
	id := strings.TrimSuffix(parts[3], ".pdf")
	parsed, err := uuid.Parse(id)
	return err == nil && parsed.Version() == 4 && parsed.Variant() == uuid.RFC4122 && parsed.String() == id && parts[3] == id+".pdf"
}
func validCreateSource(key, digest string, size int64) bool {
	hash, err := hex.DecodeString(digest)
	return privateArtifactKey(key) && err == nil && len(hash) == 32 && hex.EncodeToString(hash) == digest && size > 0 && size <= 250*1024*1024
}

// CreateArtifactIfAbsent uses the existing selector without changing legacy
// writers. Only the dormant OCR adapter calls it. No unconditional fallback.
func CreateArtifactIfAbsent(ctx context.Context, key, source, digest string, size int64) error {
	if !validCreateSource(key, digest, size) {
		return createError(ArtifactCreateInvalid)
	}
	if ctx.Err() != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	if RemoteStorageEnabled() {
		store, err := Default()
		if err != nil || store == nil {
			return createError(ArtifactCreateNotApplied)
		}
		return store.CreateArtifactIfAbsent(ctx, key, source, digest, size)
	}
	return createLocalArtifact(ctx, GetLocalStorageDir(), key, source, digest, size, localCreateOps{})
}

// MinIO wraps readers in a Seek-capable hook even when the source cannot seek.
// Permit its initial rewind, then reject all replays before a second HTTP PUT.
// Otherwise a lost acknowledgement could be masked by a retry returning 412.
type artifactSingleAttemptReader struct {
	io.Reader
	rewound bool
}

func (r *artifactSingleAttemptReader) Seek(offset int64, whence int) (int64, error) {
	if r.rewound || offset != 0 || whence != io.SeekStart {
		return 0, errors.New("artifact PUT replay requires reconciliation")
	}
	r.rewound = true
	return 0, nil
}

// CreateArtifactIfAbsent requires a provider honoring conditional single PUT.
// Actual R2 is not qualified by loopback protocol tests. ETags describe stored
// (possibly encrypted) bytes; they are never the logical PDF digest.
func (s *Store) CreateArtifactIfAbsent(ctx context.Context, key, source, digest string, size int64) error {
	if !validCreateSource(key, digest, size) {
		return createError(ArtifactCreateInvalid)
	}
	if ctx.Err() != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	if s == nil || s.client == nil {
		return createError(ArtifactCreateUnsupported)
	}
	if !s.supportsArtifactConditionalCreate() {
		return createError(ArtifactCreateUnsupported)
	}
	file, err := os.Open(source)
	if err != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return createError(ArtifactCreateInvalid)
	}
	logical, err := io.ReadAll(io.LimitReader(contextArtifactReader{ctx, file}, size+1))
	if err != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	hash := sha256.Sum256(logical)
	if int64(len(logical)) != size || hex.EncodeToString(hash[:]) != digest {
		return createError(ArtifactCreateInvalid)
	}
	encrypted, err := encryptData(logical)
	if err != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	if ctx.Err() != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	opts := minio.PutObjectOptions{ContentType: "application/pdf", DisableMultipart: true, DisableContentSha256: true}
	opts.SetMatchETagExcept("*")
	_, err = s.client.PutObject(ctx, s.bucket, key, &artifactSingleAttemptReader{Reader: bytes.NewReader(encrypted)}, int64(len(encrypted)), opts)
	if err == nil {
		return nil
	}
	response := minio.ToErrorResponse(err)
	switch {
	case response.StatusCode == 412:
		return createError(ArtifactCreateExists)
	case response.StatusCode == 409:
		return createError(ArtifactCreateConflict)
	case response.StatusCode == 401 || response.StatusCode == 403:
		return createError(ArtifactCreateDenied)
	case response.StatusCode == 501 || response.Code == "NotImplemented":
		return createError(ArtifactCreateUnsupported)
	default:
		return errors.Join(createError(ArtifactCreateUncertain), ctx.Err())
	}
}

// Other S3-compatible providers are not implicitly qualified. This is a bounded
// documented R2 endpoint contract, not proof of bucket retention/immutability.
func (s *Store) supportsArtifactConditionalCreate() bool {
	endpoint := s.client.EndpointURL()
	if ip := net.ParseIP(endpoint.Hostname()); ip != nil && ip.IsLoopback() && s.artifactConditionalTest {
		return true
	}
	const suffix = ".r2.cloudflarestorage.com"
	if endpoint.Scheme != "https" || endpoint.Port() != "" || !strings.HasSuffix(endpoint.Hostname(), suffix) {
		return false
	}
	account := strings.TrimSuffix(endpoint.Hostname(), suffix)
	value, err := hex.DecodeString(account)
	return err == nil && len(value) == 16 && hex.EncodeToString(value) == account
}
