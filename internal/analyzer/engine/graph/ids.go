package graph

import (
	"fmt"
	"path/filepath"
	"strings"
)

// normalizePath ensures consistent slashes and no leading slash or `./`.
func normalizePath(p string) string {
	p = filepath.ToSlash(p)
	p = strings.TrimPrefix(p, "./")
	p = strings.TrimPrefix(p, "/")
	return p
}

// FileEntityID generates a deterministic ID for a file.
// Format: file:<normPath>
func FileEntityID(normPath string) string {
	return fmt.Sprintf("file:%s", normalizePath(normPath))
}

// SymbolEntityID generates a deterministic ID for a symbol within a file.
// Format: symbol:<normPath>#<symbolName>
func SymbolEntityID(normPath, symbolName string) string {
	return fmt.Sprintf("symbol:%s#%s", normalizePath(normPath), symbolName)
}

// PackageEntityID generates a deterministic ID for a package dependency.
// Format: package:<ecosystem>:<name>
func PackageEntityID(ecosystem, name string) string {
	return fmt.Sprintf("package:%s:%s", ecosystem, name)
}

// RouteEntityID generates a deterministic ID for an API route.
// Format: route:<method>:<path>
func RouteEntityID(method, path string) string {
	// e.g. route:POST:/api/v1/analyzer/upload
	return fmt.Sprintf("route:%s:%s", method, path)
}

// ModelEntityID generates a deterministic ID for a data model.
// Format: model:<normPath>#<modelName>
func ModelEntityID(normPath, modelName string) string {
	return fmt.Sprintf("model:%s#%s", normalizePath(normPath), modelName)
}

// ConfigEntityID generates a deterministic ID for a configuration key.
// Format: config:<key>
func ConfigEntityID(key string) string {
	return fmt.Sprintf("config:%s", key)
}

// TestEntityID generates a deterministic ID for a test file.
// Format: test:<normPath>
func TestEntityID(normPath string) string {
	return fmt.Sprintf("test:%s", normalizePath(normPath))
}
