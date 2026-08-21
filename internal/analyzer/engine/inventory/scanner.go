package inventory

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"pdfnest-backend/internal/analyzer/engine/exclusion"
)

// ScannerOptions defines options for executing a repository inventory walk.
type ScannerOptions struct {
	MaxFiles        int
	MaxTotalBytes   int64
	ExclusionEngine *exclusion.Engine
	ArtifactSha256  string
	ScopeHash       string
}

// DefaultScannerOptions provides safe production defaults for scanning.
func DefaultScannerOptions(exEngine *exclusion.Engine) ScannerOptions {
	return ScannerOptions{
		MaxFiles:        25000,
		MaxTotalBytes:   250 * 1024 * 1024,
		ExclusionEngine: exEngine,
	}
}

// ScanRepository performs a fast, zero-content-read, deterministic filesystem walk of the repository root.
// It enforces symlink confinement, evaluates 5-tier exclusions, classifies files, and aggregates metrics.
func ScanRepository(ctx context.Context, rootDir string, opts ScannerOptions) (*ScopeInventory, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve root directory: %w", err)
	}

	info, err := os.Stat(absRoot)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("invalid repository root: %s", absRoot)
	}

	exEngine := opts.ExclusionEngine
	if exEngine == nil {
		exEngine = exclusion.NewEngine(exclusion.Config{})
	}

	files := make([]FileEntry, 0, 512)
	manifestsSet := make(map[string]struct{})
	languagesSet := make(map[string]struct{})

	var totalBytes int64
	var includedBytes int64
	var excludedBytes int64
	var includedFiles int
	var excludedFiles int
	var directoriesCount int
	var maxDepth int

	walkFn := func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if path == absRoot {
			return nil
		}

		relPath, err := filepath.Rel(absRoot, path)
		if err != nil {
			return err
		}
		normRel := exclusion.NormalizePath(relPath)

		// Calculate directory depth
		depth := strings.Count(normRel, "/") + 1
		if depth > maxDepth {
			maxDepth = depth
		}

		// Handle Symlinks conservatively
		isSymlink := (d.Type() & fs.ModeSymlink) != 0
		if isSymlink {
			target, evalErr := filepath.EvalSymlinks(path)
			if evalErr != nil || !strings.HasPrefix(target, absRoot) {
				// Symlink escapes root: classify as excluded for safety
				fileInfo, _ := d.Info()
				var size int64
				if fileInfo != nil {
					size = fileInfo.Size()
				}
				files = append(files, FileEntry{
					Path:        path,
					RelPath:     normRel,
					Size:        size,
					Extension:   filepath.Ext(normRel),
					Category:    CategoryUnknown,
					Depth:       depth,
					IsExcluded:  true,
					IsDirectory: false,
					IsSymlink:   true,
					Exclusion: exclusion.EvaluationResult{
						IsExcluded:     true,
						MatchedPattern: "symlink_escape",
						Precedence:     exclusion.PrecedenceMandatorySecurity,
						Reason:         "Symlink points outside sandbox root",
						IsMandatory:    true,
						Overridable:    false,
					},
				})
				excludedFiles++
				return nil
			}
		}

		if d.IsDir() {
			directoriesCount++
			// Evaluate directory-level exclusions
			eval := exEngine.Evaluate(normRel + "/")
			if eval.IsExcluded {
				return filepath.SkipDir
			}
			return nil
		}

		// Regular file inspection
		fileInfo, err := d.Info()
		if err != nil {
			return nil
		}
		size := fileInfo.Size()
		totalBytes += size

		// Check hard limit ceilings
		if opts.MaxFiles > 0 && len(files) > opts.MaxFiles {
			return fmt.Errorf("repository file count exceeds ceiling limit of %d files", opts.MaxFiles)
		}
		if opts.MaxTotalBytes > 0 && totalBytes > opts.MaxTotalBytes {
			return fmt.Errorf("repository total bytes exceed ceiling limit of %d bytes", opts.MaxTotalBytes)
		}

		category, lang := ClassifyFile(normRel)
		if category == CategoryManifest {
			manifestsSet[filepath.Base(normRel)] = struct{}{}
		}
		if lang != "" && lang != "Config" && lang != "Markdown" {
			languagesSet[lang] = struct{}{}
		}

		// Evaluate 5-tier exclusion rules
		eval := exEngine.Evaluate(normRel)
		if eval.IsExcluded {
			excludedFiles++
			excludedBytes += size
		} else {
			includedFiles++
			includedBytes += size
		}

		files = append(files, FileEntry{
			Path:        path,
			RelPath:     normRel,
			Size:        size,
			Extension:   filepath.Ext(normRel),
			Category:    category,
			Language:    lang,
			Depth:       depth,
			IsExcluded:  eval.IsExcluded,
			Exclusion:   eval,
			IsDirectory: false,
			IsSymlink:   isSymlink,
		})

		return nil
	}

	if err := filepath.WalkDir(absRoot, walkFn); err != nil {
		return nil, fmt.Errorf("walk repository: %w", err)
	}

	// Guarantee deterministic sort order by relative path
	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})

	manifests := make([]string, 0, len(manifestsSet))
	for m := range manifestsSet {
		manifests = append(manifests, m)
	}
	sort.Strings(manifests)

	languages := make([]string, 0, len(languagesSet))
	for l := range languagesSet {
		languages = append(languages, l)
	}
	sort.Strings(languages)

	return &ScopeInventory{
		TotalFiles:       len(files),
		IncludedFiles:    includedFiles,
		ExcludedFiles:    excludedFiles,
		TotalBytes:       totalBytes,
		IncludedBytes:    includedBytes,
		ExcludedBytes:    excludedBytes,
		DirectoriesCount: directoriesCount,
		MaximumDepth:     maxDepth,
		Files:            files,
		ManifestsFound:   manifests,
		LanguagesFound:   languages,
		ArtifactSha256:   opts.ArtifactSha256,
		ScopeHash:        opts.ScopeHash,
	}, nil
}
