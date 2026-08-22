package graph

import (
	"testing"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/parsers"
	"pdfnest-backend/internal/analyzer/engine/ast"
)

func TestExtractors(t *testing.T) {
	inv := &inventory.ScopeInventory{
		Files: []inventory.FileEntry{
			{RelPath: "src/main.go", Category: inventory.CategorySource, IsDirectory: false},
			{RelPath: "src", Category: inventory.CategoryUnknown, IsDirectory: true},
		},
	}
	
	facts := &parsers.AnalysisFacts{}
	
	res := ExtractInventoryEntitiesAndEdges(inv, facts)
	if len(res.Entities) != 2 {
		t.Errorf("Expected 2 entities, got %d", len(res.Entities))
	}
	if len(res.Edges) != 1 {
		t.Errorf("Expected 1 edge, got %d", len(res.Edges))
	}
	
	astFacts := &ast.ASTAnalysisResult{
		Symbols: []ast.SymbolItem{
			{Name: "MyStruct", Kind: ast.SymbolKindStruct, SourceFile: "src/main.go"},
		},
	}
	
	astRes := ExtractASTEntitiesAndEdges(astFacts)
	if len(astRes.Entities) != 1 {
		t.Errorf("Expected 1 ast entity, got %d", len(astRes.Entities))
	}
	if len(astRes.Edges) != 1 {
		t.Errorf("Expected 1 ast edge, got %d", len(astRes.Edges))
	}
}
