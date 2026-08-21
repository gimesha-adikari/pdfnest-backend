package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// HashFileSHA256 computes the SHA-256 checksum of an immutable file (e.g. staged archive).
func HashFileSHA256(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("hash file: %w", err)
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("copy to hasher: %w", err)
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// HashBytesSHA256 computes the SHA-256 checksum of raw byte content.
func HashBytesSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ScopeHashInput contains configuration parameters for scope hashing.
type ScopeHashInput struct {
	CustomExclusions []string
	EnabledPresets   []string
	ForceIncludes    []string
	SelectedDomains  []string
}

// ComputeScopeHash generates a deterministic SHA-256 hash of a scope configuration.
// It canonicalizes, trims, deduplicates, and sorts all inputs so hash output is independent
// of map iteration order, whitespace, or slice ordering.
func ComputeScopeHash(input ScopeHashInput) string {
	canonicalize := func(items []string) []string {
		seen := make(map[string]struct{}, len(items))
		result := make([]string, 0, len(items))
		for _, it := range items {
			trimmed := strings.TrimSpace(it)
			if trimmed == "" {
				continue
			}
			if _, exists := seen[trimmed]; !exists {
				seen[trimmed] = struct{}{}
				result = append(result, trimmed)
			}
		}
		sort.Strings(result)
		return result
	}

	custom := canonicalize(input.CustomExclusions)
	presets := canonicalize(input.EnabledPresets)
	force := canonicalize(input.ForceIncludes)
	domains := canonicalize(input.SelectedDomains)

	var builder strings.Builder
	builder.WriteString("custom:")
	builder.WriteString(strings.Join(custom, ","))
	builder.WriteString(";presets:")
	builder.WriteString(strings.Join(presets, ","))
	builder.WriteString(";force:")
	builder.WriteString(strings.Join(force, ","))
	builder.WriteString(";domains:")
	builder.WriteString(strings.Join(domains, ","))

	return HashBytesSHA256([]byte(builder.String()))
}
