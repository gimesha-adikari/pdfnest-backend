package storage

import (
	"errors"
	"strings"
)

var ErrPrivateOCRNamespace = errors.New("generic mutation of private OCR artifact namespace forbidden")

// Object keys are not URLs. Mirror sanitizeObjectKey's slash/backslash, dot and
// root-clamping semantics, checking component boundaries before a later '..'
// can erase entry into the reserved namespace. Sibling names remain distinct.
func rejectPrivateOCRMutation(raw string) error {
	var parts []string
	for _, part := range strings.Split(strings.TrimSpace(strings.ReplaceAll(raw, "\\", "/")), "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			if len(parts) > 0 {
				parts = parts[:len(parts)-1]
			}
		default:
			parts = append(parts, part)
		}
		if len(parts) >= 2 && parts[0] == "ocr-finalization" && parts[1] == "outputs" {
			return ErrPrivateOCRNamespace
		}
	}
	return nil
}
func sanitizeMutationKey(raw string) (string, error) {
	if err := rejectPrivateOCRMutation(raw); err != nil {
		return "", err
	}
	return sanitizeObjectKey(raw)
}
