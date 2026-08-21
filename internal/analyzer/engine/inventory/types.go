package inventory

import (
	"pdfnest-backend/internal/analyzer/engine/exclusion"
)

// FileCategory represents the architectural classification of a file.
type FileCategory string

const (
	CategorySource        FileCategory = "SOURCE"
	CategoryConfig        FileCategory = "CONFIG"
	CategoryManifest      FileCategory = "MANIFEST"
	CategoryTest          FileCategory = "TEST"
	CategoryAsset         FileCategory = "ASSET"
	CategoryDocumentation FileCategory = "DOCUMENTATION"
	CategoryBinary        FileCategory = "BINARY"
	CategoryUnknown       FileCategory = "UNKNOWN"
)

// FileEntry contains metadata for an individual file inspected during inventory.
type FileEntry struct {
	Path        string                     `json:"path"`
	RelPath     string                     `json:"relPath"`
	Size        int64                      `json:"size"`
	Extension   string                     `json:"extension"`
	Category    FileCategory               `json:"category"`
	Language    string                     `json:"language,omitempty"`
	Depth       int                        `json:"depth"`
	IsExcluded  bool                       `json:"isExcluded"`
	Exclusion   exclusion.EvaluationResult `json:"exclusion"`
	IsDirectory bool                       `json:"isDirectory"`
	IsSymlink   bool                       `json:"isSymlink"`
}

// ScopeInventory summarizes the repository's scanned file inventory and scoping metrics.
type ScopeInventory struct {
	TotalFiles       int         `json:"totalFiles"`
	IncludedFiles    int         `json:"includedFiles"`
	ExcludedFiles    int         `json:"excludedFiles"`
	TotalBytes       int64       `json:"totalBytes"`
	IncludedBytes    int64       `json:"includedBytes"`
	ExcludedBytes    int64       `json:"excludedBytes"`
	DirectoriesCount int         `json:"directoriesCount"`
	MaximumDepth     int         `json:"maximumDepth"`
	Files            []FileEntry `json:"files"`
	ManifestsFound   []string    `json:"manifestsFound"`
	LanguagesFound   []string    `json:"languagesFound"`
	ArtifactSha256   string      `json:"artifactSha256"`
	ScopeHash        string      `json:"scopeHash"`
}
