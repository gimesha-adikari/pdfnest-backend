package ast

import (
	"time"
)

// ResourceLimits defines configurable safety thresholds for AST parsing operations.
type ResourceLimits struct {
	MaxFileSize       int64         `json:"maxFileSize"`       // Maximum single source file size in bytes (default: 500KB)
	MaxDepth          int           `json:"maxDepth"`          // Maximum recursive AST node traversal depth (default: 50)
	MaxNodesPerFile   int64         `json:"maxNodesPerFile"`   // Maximum total AST nodes visited per file (default: 25,000)
	PerFileTimeout    time.Duration `json:"perFileTimeout"`    // Maximum execution duration per file (default: 500ms)
	MaxCandidateFiles int           `json:"maxCandidateFiles"` // Maximum candidate source files analyzed per run (default: 100)
}

// DefaultResourceLimits returns hardened, production-safe AST resource thresholds.
func DefaultResourceLimits() *ResourceLimits {
	return &ResourceLimits{
		MaxFileSize:       512 * 1024, // 500 KB
		MaxDepth:          50,
		MaxNodesPerFile:   25000,
		PerFileTimeout:    500 * time.Millisecond,
		MaxCandidateFiles: 100,
	}
}

// ValidateWithDefaults ensures all limit fields have safe, positive values.
func (l *ResourceLimits) ValidateWithDefaults() *ResourceLimits {
	def := DefaultResourceLimits()
	if l == nil {
		return def
	}
	res := *l
	if res.MaxFileSize <= 0 {
		res.MaxFileSize = def.MaxFileSize
	}
	if res.MaxDepth <= 0 {
		res.MaxDepth = def.MaxDepth
	}
	if res.MaxNodesPerFile <= 0 {
		res.MaxNodesPerFile = def.MaxNodesPerFile
	}
	if res.PerFileTimeout <= 0 {
		res.PerFileTimeout = def.PerFileTimeout
	}
	if res.MaxCandidateFiles <= 0 {
		res.MaxCandidateFiles = def.MaxCandidateFiles
	}
	return &res
}
