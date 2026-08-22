package graph

import (
	"context"
	"testing"

	"pdfnest-backend/internal/analyzer/engine/ast"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/parsers"
)

func TestBuilderAndMetrics(t *testing.T) {
	inv := &inventory.ScopeInventory{
		Files: []inventory.FileEntry{
			{RelPath: "src/main.go", Category: inventory.CategorySource, IsDirectory: false},
			{RelPath: "src", Category: inventory.CategoryUnknown, IsDirectory: true},
		},
	}
	
	parserFacts := &parsers.AnalysisFacts{}
	astFacts := &ast.ASTAnalysisResult{}

	g, metrics, err := BuildRelationshipGraph(context.Background(), inv, astFacts, parserFacts)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}

	if g == nil {
		t.Fatal("Expected graph, got nil")
	}

	if metrics == nil {
		t.Fatal("Expected metrics, got nil")
	}

	if metrics.EntityCount != 2 {
		t.Errorf("Expected 2 entities, got %d", metrics.EntityCount)
	}
	if metrics.EdgeCount != 1 {
		t.Errorf("Expected 1 edge, got %d", metrics.EdgeCount)
	}
	if metrics.UnresolvedReferences != 0 {
		t.Errorf("Expected 0 unresolved references, got %d", metrics.UnresolvedReferences)
	}
}
