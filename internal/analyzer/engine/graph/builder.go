package graph

import (
	"context"
	
	"pdfnest-backend/internal/analyzer/engine/ast"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/parsers"
)

func BuildRelationshipGraph(
	ctx context.Context, 
	inv *inventory.ScopeInventory, 
	astFacts *ast.ASTAnalysisResult, 
	parserFacts *parsers.AnalysisFacts,
) (*RelationshipGraph, *GraphMetrics, error) {

	g := NewRelationshipGraph()
	unresolvedCount := 0

	// 1. Extract from all sources
	invRes := ExtractInventoryEntitiesAndEdges(inv, parserFacts)
	astRes := ExtractASTEntitiesAndEdges(astFacts)
	routeRes := ExtractRouteEntitiesAndEdges(parserFacts)
	modelRes := ExtractModelEntitiesAndEdges(astFacts)
	configRes := ExtractConfigEntitiesAndEdges(parserFacts)
	testRes := ExtractTestEntitiesAndEdges(inv, parserFacts)
	deployRes := ExtractDeploymentEntitiesAndEdges(parserFacts)

	// 2. Add all entities first
	allResults := []*ExtractionResult{
		invRes, astRes, routeRes, modelRes, configRes, testRes, deployRes,
	}

	for _, res := range allResults {
		if res == nil {
			continue
		}
		for _, ent := range res.Entities {
			// Ignore if it already exists, or overwrite. Let's ignore err for duplicates.
			_ = g.AddEntity(ent)
		}
	}

	// 3. Add edges, recording unresolved references instead of creating dangling edges
	for _, res := range allResults {
		if res == nil {
			continue
		}
		for _, edge := range res.Edges {
			err := g.AddEdge(edge)
			if err != nil {
				unresolvedCount++
			}
		}
	}

	metrics := ComputeGraphMetrics(g)
	metrics.UnresolvedReferences = unresolvedCount

	return g, metrics, nil
}
