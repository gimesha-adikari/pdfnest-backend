package storage

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/webp"
)

const localOCRGrantLifetime = 5 * time.Minute
const maxLocalOCRImagePixels int64 = 10_000_000
const maxLocalOCRImageDimension = 10_000

var errLocalOCRGrantUsed = errors.New("local OCR upload grant already consumed")
var errLocalOCRGrantExpired = errors.New("local OCR upload grant expired")

// This gates only the untrusted development upload adapter, not internal saves.
func localOCRUploadsEnabled() bool {
	env := strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("STORAGE_MODE")))
	return (env == "development" || env == "test") && (mode == "local" || mode == "filesystem") && !RemoteStorageEnabled()
}

func canonicalLocalOCRMediaType(value string) string {
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	switch strings.ToLower(mediaType) {
	case "image/jpg", "image/jpeg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/webp":
		return "image/webp"
	default:
		return ""
	}
}

type localOCRGrant struct {
	Version   int    `json:"v"`
	Owner     string `json:"owner"`
	Key       string `json:"key"`
	Purpose   string `json:"purpose"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
	Expires   int64  `json:"expires"`
}

func (ctrl *Controller) signLocalOCRGrant(payload string) []byte {
	mac := hmac.New(sha256.New, ctrl.uploadGrantKey)
	_, _ = mac.Write([]byte("platen:local-ocr-upload:v1\x00" + payload))
	return mac.Sum(nil)
}

func (ctrl *Controller) issueLocalOCRGrant(owner, key string, size int64, mediaType string) (string, error) {
	if len(ctrl.uploadGrantKey) != 32 {
		return "", errors.New("upload grant signing unavailable")
	}
	grant := localOCRGrant{1, owner, key, "image_to_text_pdf", size, mediaType, ctrl.now().Add(localOCRGrantLifetime).Unix()}
	raw, err := json.Marshal(grant)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	return payload + "." + base64.RawURLEncoding.EncodeToString(ctrl.signLocalOCRGrant(payload)), nil
}

func (ctrl *Controller) verifyLocalOCRGrant(token, owner string) (localOCRGrant, error) {
	var grant localOCRGrant
	if len(ctrl.uploadGrantKey) != 32 || owner == "" || len(token) > 4096 {
		return grant, errors.New("invalid grant")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 2 {
		return grant, errors.New("invalid grant")
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !hmac.Equal(signature, ctrl.signLocalOCRGrant(parts[0])) {
		return grant, errors.New("invalid signature")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return grant, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&grant); err != nil {
		return grant, err
	}
	var extra interface{}
	if decoder.Decode(&extra) != io.EOF || grant.Version != 1 || grant.Owner != owner ||
		grant.Purpose != "image_to_text_pdf" || !IsOwnedKey(grant.Key, owner, grant.Purpose) ||
		grant.Size <= 0 || grant.Size > maxLocalOCRUploadBytes ||
		grant.MediaType == "" || canonicalLocalOCRMediaType(grant.MediaType) != grant.MediaType ||
		ctrl.now().Unix() >= grant.Expires {
		return grant, errors.New("invalid or expired grant")
	}
	return grant, nil
}

func validateLocalOCRImage(body []byte, mediaType string) error {
	config, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil || canonicalLocalOCRMediaType("image/"+format) != mediaType {
		return errors.New("image format mismatch")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > maxLocalOCRImageDimension ||
		config.Height > maxLocalOCRImageDimension || int64(config.Width)*int64(config.Height) > maxLocalOCRImagePixels {
		return errors.New("unsafe image dimensions")
	}
	decoded, actualFormat, err := image.Decode(bytes.NewReader(body))
	if err != nil || actualFormat != format || decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return errors.New("corrupt image")
	}
	return nil
}

// Publish a complete validated object without changing SaveLocalStream's overwrite
// contract for server-created outputs. A spent receipt survives object cleanup
// and process restarts. Failed/crashed publication consumes the grant fail closed;
// the client must request a fresh destination. Receipts are tiny local tombstones.
func publishLocalOCRImage(grant localOCRGrant, body []byte, now func() time.Time) error {
	if !localOCRUploadsEnabled() {
		return errors.New("local uploads disabled")
	}
	key := grant.Key
	clean, err := sanitizeObjectKey(key)
	if err != nil || clean != key {
		return errors.New("invalid key")
	}
	root := GetLocalStorageDir()
	target := filepath.Join(root, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".ocr-upload-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if _, err := tmp.Write(body); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	receipts := filepath.Join(root, ".local-ocr-upload-grants")
	if err := os.MkdirAll(receipts, 0700); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(key))
	if now().Unix() >= grant.Expires {
		return errLocalOCRGrantExpired
	}
	receipt, err := os.OpenFile(filepath.Join(receipts, hex.EncodeToString(digest[:])), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return errLocalOCRGrantUsed
	}
	if err != nil {
		return err
	}
	if err := receipt.Close(); err != nil {
		return err
	}
	// Same-filesystem link is atomic and fails if any destination already exists.
	if err := os.Link(tmp.Name(), target); errors.Is(err, os.ErrExist) {
		return errLocalOCRGrantUsed
	} else if err != nil {
		return fmt.Errorf("create-only publication: %w", err)
	}
	return nil
}
