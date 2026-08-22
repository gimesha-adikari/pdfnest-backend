package graph

import (
	"context"
	"testing"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/parsers"
)

func TestSynthetic(t *testing.T) {
	// Simple synthetic test for cycles and dangling edge constraints
	ctx := context.Background()
	inv := &inventory.ScopeInventory{}
	parserFacts := &parsers.AnalysisFacts{}

	_, metrics, err := BuildRelationshipGraph(ctx, inv, nil, parserFacts)
	if err != nil {
		t.Fatalf("Failed to build graph: %v", err)
	}

	if metrics.UnresolvedReferences != 0 {
		t.Errorf("Expected 0 unresolved references, got %d", metrics.UnresolvedReferences)
	}
}
