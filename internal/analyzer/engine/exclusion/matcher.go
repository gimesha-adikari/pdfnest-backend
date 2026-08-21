package exclusion

import (
	"path/filepath"
	"strings"
)

// NormalizePath standardizes separators to forward slashes and trims leading/trailing slashes.
func NormalizePath(p string) string {
	cleaned := filepath.ToSlash(filepath.Clean(p))
	cleaned = strings.TrimPrefix(cleaned, "./")
	cleaned = strings.TrimPrefix(cleaned, "/")
	cleaned = strings.TrimSuffix(cleaned, "/")
	return cleaned
}

// MatchGlob checks if a normalized file path matches a glob pattern.
// Supports:
//   - "**/" prefix/suffix for arbitrary directory depth
//   - "*" for single path segment wildcard
//   - "?" for single character wildcard
//   - Exact path equality
//   - Directory prefix matches
func MatchGlob(pattern string, relPath string) bool {
	normPattern := NormalizePath(pattern)
	normPath := NormalizePath(relPath)

	if normPattern == "" || normPath == "" {
		return false
	}

	// Exact match
	if normPattern == normPath {
		return true
	}

	// If pattern is a directory like "node_modules" or "dist"
	if normPattern == normPath || strings.HasPrefix(normPath, normPattern+"/") {
		return true
	}

	// Double-star handling
	if strings.HasPrefix(normPattern, "**/") {
		subPattern := strings.TrimPrefix(normPattern, "**/")
		// Match against full path or any suffix segment
		if matchSegment(subPattern, normPath) {
			return true
		}
		parts := strings.Split(normPath, "/")
		for i := 1; i < len(parts); i++ {
			suffix := strings.Join(parts[i:], "/")
			if matchSegment(subPattern, suffix) {
				return true
			}
		}
		// Also check if subPattern ends with /**
		if strings.HasSuffix(subPattern, "/**") {
			dirName := strings.TrimSuffix(subPattern, "/**")
			for _, part := range parts {
				if part == dirName {
					return true
				}
			}
		}
		return false
	}

	if strings.HasSuffix(normPattern, "/**") {
		dirPrefix := strings.TrimSuffix(normPattern, "/**")
		if normPath == dirPrefix || strings.HasPrefix(normPath, dirPrefix+"/") {
			return true
		}
	}

	return matchSegment(normPattern, normPath)
}

func matchSegment(pattern string, path string) bool {
	matched, err := filepath.Match(pattern, path)
	if err == nil && matched {
		return true
	}

	// Check if pattern is a file extension wildcard like *.pem matching base
	if strings.HasPrefix(pattern, "*.") {
		base := filepath.Base(path)
		matched, err := filepath.Match(pattern, base)
		if err == nil && matched {
			return true
		}
	}

	// Check if pattern has wildcard base like id_rsa*
	if strings.Contains(pattern, "*") && !strings.Contains(pattern, "/") {
		base := filepath.Base(path)
		matched, err := filepath.Match(pattern, base)
		if err == nil && matched {
			return true
		}
	}

	// Check directory prefix
	if strings.HasSuffix(pattern, "/*") {
		dir := strings.TrimSuffix(pattern, "/*")
		if filepath.Dir(path) == dir {
			return true
		}
	}

	return false
}
