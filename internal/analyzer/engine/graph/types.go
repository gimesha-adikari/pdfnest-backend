package graph

import "pdfnest-backend/internal/analyzer/engine"

// EntityKind defines the type of graph entity.
type EntityKind string

const (
	EntityFile       EntityKind = "file"
	EntityDirectory  EntityKind = "directory"
	EntitySymbol     EntityKind = "symbol"
	EntityPackage    EntityKind = "package"
	EntityRoute      EntityKind = "route"
	EntityModel      EntityKind = "model"
	EntityConfig     EntityKind = "config"
	EntityTest       EntityKind = "test"
	EntityService    EntityKind = "service"
	EntityQueue      EntityKind = "queue"
	EntityStorage    EntityKind = "storage"
	EntityDeployment EntityKind = "deployment"
)

// SymbolKind defines the specific type of a code symbol.
type SymbolKind string

const (
	SymbolStruct    SymbolKind = "struct"
	SymbolInterface SymbolKind = "interface"
	SymbolFunction  SymbolKind = "function"
	SymbolMethod    SymbolKind = "method"
	SymbolClass     SymbolKind = "class"
	SymbolType      SymbolKind = "type"
)

// PackageManager defines the ecosystem for a package entity.
type PackageManager string

const (
	Npm      PackageManager = "npm"
	GoMod    PackageManager = "gomod"
	Cargo    PackageManager = "cargo"
	Pip      PackageManager = "pip"
	Composer PackageManager = "composer"
	Maven    PackageManager = "maven"
)

// HTTPMethod defines the REST method for a route entity.
type HTTPMethod string

const (
	GET    HTTPMethod = "GET"
	POST   HTTPMethod = "POST"
	PUT    HTTPMethod = "PUT"
	DELETE HTTPMethod = "DELETE"
	PATCH  HTTPMethod = "PATCH"
)

// Other tech-specific types
type DatabaseTech string
type QueueTech string
type TestFramework string

// GraphEntity represents a node in the semantic graph.
type GraphEntity struct {
	ID         string            `json:"id"`
	Kind       EntityKind        `json:"kind"`
	Name       string            `json:"name"`
	Path       string            `json:"path"`
	Properties map[string]any    `json:"properties,omitempty"` // for strongly typed fields we can use map or specific fields
	Evidence   []engine.Evidence `json:"evidence,omitempty"`
}

// RelationType defines the kind of relationship between entities.
type RelationType string

const (
	// Direct relationships
	RelDefines    RelationType = "defines"
	RelExports    RelationType = "exports"
	RelImports    RelationType = "imports"
	RelImplements RelationType = "implements"
	RelExposes    RelationType = "exposes"

	// Inferred relationships
	RelCalls        RelationType = "calls"
	RelConsumes     RelationType = "consumes"
	RelDependsOn    RelationType = "depends_on"
	RelConfigures   RelationType = "configures"
	RelTests        RelationType = "tests"
	RelPersistsTo   RelationType = "persists_to"
	RelPublishesTo  RelationType = "publishes_to"
	RelConsumesFrom RelationType = "consumes_from"
	RelDeploys      RelationType = "deploys"
)

type RelationshipKind string

const (
	RelationshipKindDirect   RelationshipKind = "direct"
	RelationshipKindInferred RelationshipKind = "inferred"
)

// RelationshipProvenance tracks how a relationship was discovered.
type RelationshipProvenance struct {
	Kind        RelationshipKind `json:"kind"`
	Detector    string           `json:"detector"`
	EvidenceIDs []string         `json:"evidenceIds,omitempty"`
	DerivedFrom []string         `json:"derivedFrom,omitempty"`
}

// GraphEdge represents a directed relationship between two entities.
type GraphEdge struct {
	ID         string                     `json:"id"`
	SourceID   string                     `json:"sourceId"`
	TargetID   string                     `json:"targetId"`
	Type       RelationType               `json:"type"`
	Confidence engine.EpistemicConfidence `json:"confidence"`
	Provenance RelationshipProvenance     `json:"provenance"`
	Evidence   []engine.Evidence          `json:"evidence,omitempty"`
	Properties map[string]string          `json:"properties,omitempty"`
}
