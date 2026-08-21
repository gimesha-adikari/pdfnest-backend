package ast

import (
	"pdfnest-backend/internal/analyzer/engine"
)

// SymbolKind categorizes AST symbol declarations.
type SymbolKind string

const (
	SymbolKindStruct    SymbolKind = "struct"
	SymbolKindInterface SymbolKind = "interface"
	SymbolKindFunction  SymbolKind = "function"
	SymbolKindMethod    SymbolKind = "method"
	SymbolKindClass     SymbolKind = "class"
	SymbolKindType      SymbolKind = "type"
)

// SymbolItem represents an extracted type or function symbol.
type SymbolItem struct {
	Name       string     `json:"name"`
	Kind       SymbolKind `json:"kind"`
	SourceFile string     `json:"sourceFile"`
	LineNumber int        `json:"lineNumber"`
	Receiver   string     `json:"receiver,omitempty"`
	Signature  string     `json:"signature,omitempty"`
	Exported   bool       `json:"exported"`
}

// ImportItem represents a source-level dependency import.
type ImportItem struct {
	SourceFile string `json:"sourceFile"`
	ImportPath string `json:"importPath"`
	Alias      string `json:"alias,omitempty"`
	LineNumber int    `json:"lineNumber"`
}

// EnvironmentUsage captures AST-detected environment variable lookups.
type EnvironmentUsage struct {
	Name       string `json:"name"`
	SourceFile string `json:"sourceFile"`
	LineNumber int    `json:"lineNumber"`
	AccessType string `json:"accessType"` // e.g. "os.Getenv", "process.env"
}

// ModelField captures fields on detected ORM database models.
type ModelField struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Tag      string `json:"tag,omitempty"`
	Required bool   `json:"required"`
}

// ModelItem represents a statically detected data or ORM model structure.
type ModelItem struct {
	Name       string       `json:"name"`
	SourceFile string       `json:"sourceFile"`
	LineNumber int          `json:"lineNumber"`
	Framework  string       `json:"framework,omitempty"` // e.g. "gorm", "prisma", "typeorm"
	Fields     []ModelField `json:"fields,omitempty"`
}

// DiagnosticItem represents warnings, skipped files, or non-fatal parser errors.
type DiagnosticItem struct {
	SourceFile string `json:"sourceFile,omitempty"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	Severity   string `json:"severity"` // "warning", "info", "error"
}

// ASTRequest defines the input payload for static AST analysis.
type ASTRequest struct {
	RootDir     string          `json:"rootDir"`
	TargetFiles []string        `json:"targetFiles"`
	Languages   []string        `json:"languages,omitempty"`
	Limits      *ResourceLimits `json:"limits,omitempty"`
}

// ASTAnalysisResult aggregates all deterministic facts extracted via static AST analysis.
type ASTAnalysisResult struct {
	FilesAnalyzed         int                   `json:"filesAnalyzed"`
	FilesSkipped          int                   `json:"filesSkipped"`
	Routes                []engine.ApiRouteItem `json:"routes"`
	Symbols               []SymbolItem          `json:"symbols"`
	Imports               []ImportItem          `json:"imports"`
	EnvironmentReferences []EnvironmentUsage    `json:"environmentReferences"`
	ModelStructures       []ModelItem           `json:"modelStructures"`
	Evidence              []engine.EvidenceItem `json:"evidence"`
	Diagnostics           []DiagnosticItem      `json:"diagnostics"`
	DurationMs            int64                 `json:"durationMs"`
	NodesProcessed        int64                 `json:"nodesProcessed"`
}
