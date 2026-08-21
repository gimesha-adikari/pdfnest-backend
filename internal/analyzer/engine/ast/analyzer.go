package ast

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
)

// Engine orchestrates deterministic static AST parsing across candidate source files.
type Engine struct {
	limits *ResourceLimits
}

// NewAnalyzer initializes an AST analysis engine with configured resource thresholds.
func NewAnalyzer(limits *ResourceLimits) *Engine {
	return &Engine{
		limits: limits.ValidateWithDefaults(),
	}
}

// Analyze performs static AST analysis across the provided target files within the sandbox boundary.
func (e *Engine) Analyze(ctx context.Context, req ASTRequest) (*ASTAnalysisResult, error) {
	if req.RootDir == "" {
		return nil, fmt.Errorf("missing sandbox root directory")
	}

	cleanRoot := filepath.Clean(req.RootDir)
	limits := req.Limits.ValidateWithDefaults()

	startTime := time.Now().UTC()
	result := &ASTAnalysisResult{
		Routes:                make([]engine.ApiRouteItem, 0),
		Symbols:               make([]SymbolItem, 0),
		Imports:               make([]ImportItem, 0),
		EnvironmentReferences: make([]EnvironmentUsage, 0),
		ModelStructures:       make([]ModelItem, 0),
		Evidence:              make([]engine.EvidenceItem, 0),
		Diagnostics:           make([]DiagnosticItem, 0),
	}

	// 1. Sort candidate files deterministically
	candidateFiles := make([]string, len(req.TargetFiles))
	copy(candidateFiles, req.TargetFiles)
	sort.Strings(candidateFiles)

	// Bounded Candidate Limit
	if len(candidateFiles) > limits.MaxCandidateFiles {
		candidateFiles = candidateFiles[:limits.MaxCandidateFiles]
		result.Diagnostics = append(result.Diagnostics, DiagnosticItem{
			Code:     "CANDIDATE_LIMIT_EXCEEDED",
			Message:  fmt.Sprintf("bounded candidate files to %d", limits.MaxCandidateFiles),
			Severity: "info",
		})
	}

	// 2. Process each candidate file
	for _, relPath := range candidateFiles {
		select {
		case <-ctx.Done():
			result.DurationMs = time.Since(startTime).Milliseconds()
			return result, ctx.Err()
		default:
		}

		cleanRel := filepath.Clean(filepath.ToSlash(relPath))
		if cleanRel == "." || strings.HasPrefix(cleanRel, "../") || strings.HasPrefix(cleanRel, "/") {
			result.FilesSkipped++
			result.Diagnostics = append(result.Diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "SANDBOX_ESCAPE_REJECTED",
				Message:    "path traversal or absolute path rejected",
				Severity:   "warning",
			})
			continue
		}

		absPath := filepath.Join(cleanRoot, filepath.FromSlash(cleanRel))
		if !strings.HasPrefix(absPath, cleanRoot) {
			result.FilesSkipped++
			result.Diagnostics = append(result.Diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "SANDBOX_ESCAPE_REJECTED",
				Message:    "resolved path escapes sandbox root",
				Severity:   "warning",
			})
			continue
		}

		info, statErr := os.Stat(absPath)
		if statErr != nil || info.IsDir() {
			result.FilesSkipped++
			continue
		}

		// File Size Budget Check
		if info.Size() > limits.MaxFileSize {
			result.FilesSkipped++
			result.Diagnostics = append(result.Diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "FILE_SIZE_LIMIT_EXCEEDED",
				Message:    fmt.Sprintf("file size %d exceeds limit %d bytes", info.Size(), limits.MaxFileSize),
				Severity:   "info",
			})
			continue
		}

		content, readErr := os.ReadFile(absPath)
		if readErr != nil {
			result.FilesSkipped++
			result.Diagnostics = append(result.Diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "FILE_READ_ERROR",
				Message:    readErr.Error(),
				Severity:   "warning",
			})
			continue
		}

		// Create per-file timeout context
		fileCtx, fileCancel := context.WithTimeout(ctx, limits.PerFileTimeout)
		fileRes, parseErr := e.parseFile(fileCtx, cleanRel, content, limits)
		fileCancel()

		if parseErr != nil {
			result.FilesSkipped++
			result.Diagnostics = append(result.Diagnostics, DiagnosticItem{
				SourceFile: relPath,
				Code:       "PARSER_ERROR",
				Message:    parseErr.Error(),
				Severity:   "warning",
			})
			continue
		}

		// Accumulate results
		result.FilesAnalyzed++
		result.NodesProcessed += fileRes.NodesProcessed
		result.Routes = append(result.Routes, fileRes.Routes...)
		result.Symbols = append(result.Symbols, fileRes.Symbols...)
		result.Imports = append(result.Imports, fileRes.Imports...)
		result.EnvironmentReferences = append(result.EnvironmentReferences, fileRes.EnvironmentReferences...)
		result.ModelStructures = append(result.ModelStructures, fileRes.ModelStructures...)
		result.Evidence = append(result.Evidence, fileRes.Evidence...)
		result.Diagnostics = append(result.Diagnostics, fileRes.Diagnostics...)
	}

	result.DurationMs = time.Since(startTime).Milliseconds()

	// 3. Deterministic Sorting across all outputs
	sort.Slice(result.Routes, func(i, j int) bool {
		if result.Routes[i].Method != result.Routes[j].Method {
			return result.Routes[i].Method < result.Routes[j].Method
		}
		if result.Routes[i].Path != result.Routes[j].Path {
			return result.Routes[i].Path < result.Routes[j].Path
		}
		return result.Routes[i].SourceFile < result.Routes[j].SourceFile
	})

	sort.Slice(result.Symbols, func(i, j int) bool {
		if result.Symbols[i].SourceFile != result.Symbols[j].SourceFile {
			return result.Symbols[i].SourceFile < result.Symbols[j].SourceFile
		}
		if result.Symbols[i].LineNumber != result.Symbols[j].LineNumber {
			return result.Symbols[i].LineNumber < result.Symbols[j].LineNumber
		}
		return result.Symbols[i].Name < result.Symbols[j].Name
	})

	sort.Slice(result.Imports, func(i, j int) bool {
		if result.Imports[i].SourceFile != result.Imports[j].SourceFile {
			return result.Imports[i].SourceFile < result.Imports[j].SourceFile
		}
		if result.Imports[i].LineNumber != result.Imports[j].LineNumber {
			return result.Imports[i].LineNumber < result.Imports[j].LineNumber
		}
		return result.Imports[i].ImportPath < result.Imports[j].ImportPath
	})

	sort.Slice(result.EnvironmentReferences, func(i, j int) bool {
		if result.EnvironmentReferences[i].Name != result.EnvironmentReferences[j].Name {
			return result.EnvironmentReferences[i].Name < result.EnvironmentReferences[j].Name
		}
		if result.EnvironmentReferences[i].SourceFile != result.EnvironmentReferences[j].SourceFile {
			return result.EnvironmentReferences[i].SourceFile < result.EnvironmentReferences[j].SourceFile
		}
		return result.EnvironmentReferences[i].LineNumber < result.EnvironmentReferences[j].LineNumber
	})

	sort.Slice(result.ModelStructures, func(i, j int) bool {
		if result.ModelStructures[i].Name != result.ModelStructures[j].Name {
			return result.ModelStructures[i].Name < result.ModelStructures[j].Name
		}
		return result.ModelStructures[i].SourceFile < result.ModelStructures[j].SourceFile
	})

	sort.Slice(result.Evidence, func(i, j int) bool {
		if result.Evidence[i].FilePath != result.Evidence[j].FilePath {
			return result.Evidence[i].FilePath < result.Evidence[j].FilePath
		}
		if result.Evidence[i].RuleType != result.Evidence[j].RuleType {
			return result.Evidence[i].RuleType < result.Evidence[j].RuleType
		}
		return result.Evidence[i].Detail < result.Evidence[j].Detail
	})

	sort.Slice(result.Diagnostics, func(i, j int) bool {
		if result.Diagnostics[i].SourceFile != result.Diagnostics[j].SourceFile {
			return result.Diagnostics[i].SourceFile < result.Diagnostics[j].SourceFile
		}
		return result.Diagnostics[i].Code < result.Diagnostics[j].Code
	})

	return result, nil
}

func (e *Engine) parseFile(
	ctx context.Context,
	relPath string,
	content []byte,
	limits *ResourceLimits,
) (*fileASTResult, error) {
	lowerPath := strings.ToLower(relPath)
	ext := filepath.Ext(lowerPath)

	switch ext {
	case ".go":
		return parseGoFile(ctx, relPath, content, limits)

	case ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".prisma":
		return parseTSJSFile(ctx, relPath, content, limits)

	default:
		return &fileASTResult{
			Diagnostics: []DiagnosticItem{
				{
					SourceFile: relPath,
					Code:       "UNSUPPORTED_LANGUAGE",
					Message:    fmt.Sprintf("no AST parser for extension %s", ext),
					Severity:   "info",
				},
			},
		}, nil
	}
}
