package storage

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
)

// OwnedPrefix identifies a server-issued upload namespace. The owner comes from
// authenticated middleware, never a request field. Purpose separates disposable
// OCR inputs from sources that must survive subsequent editing and export.
func OwnedPrefix(owner, purpose string) string {
	digest := sha256.Sum256([]byte(owner))
	return "owned/" + hex.EncodeToString(digest[:]) + "/" + purpose
}

func NewOwnedKey(owner, purpose, suffix string) string {
	return BuildKey(OwnedPrefix(owner, purpose), suffix)
}

func IsOwnedKey(key, owner, purpose string) bool {
	if owner == "" {
		return false
	}
	clean, err := sanitizeObjectKey(key)
	return err == nil && clean == key && strings.HasPrefix(key, OwnedPrefix(owner, purpose)+"/")
}

func prepareOwnedUploads(req PresignRequest, owner string) (PresignRequest, error) {
	if owner == "" {
		return req, fmt.Errorf("upload owner is required")
	}
	switch req.Purpose {
	case "image_to_text_pdf", "repository_analyzer", "studio_source":
	default:
		return req, fmt.Errorf("unsupported upload purpose")
	}
	if len(req.Files) == 0 || len(req.Files) > 1000 {
		return req, fmt.Errorf("invalid file count")
	}
	req.Prefix = OwnedPrefix(owner, req.Purpose)
	req.Files = append([]PresignFile(nil), req.Files...)
	for i := range req.Files {
		file := &req.Files[i]
		if file.Size <= 0 || file.Size > 250*1024*1024 {
			return req, fmt.Errorf("invalid upload size")
		}
		// A fresh destination prevents a caller from overwriting any existing object,
		// including objects belonging to the same owner that an active job uses.
		file.Key = NewOwnedKey(owner, req.Purpose, filepath.Ext(filepath.Base(file.Name)))
	}
	return req, nil
}
