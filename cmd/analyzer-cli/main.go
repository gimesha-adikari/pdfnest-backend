package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"pdfnest-backend/internal/analyzer/engine/exclusion"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/structure"
	"pdfnest-backend/internal/analyzer/engine/parsers"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"pdfnest-backend/internal/analyzer/engine/intelligence"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: analyzer_cli <dir>")
		os.Exit(1)
	}
	dir := os.Args[1]

	ctx := context.Background()
	
	exEngine := exclusion.NewEngine(exclusion.Config{
		CustomPatterns: []string{"**/.venv/**", "**/node_modules/**", "**/.git/**", "**/tesseract/**"},
	})
	opts := inventory.DefaultScannerOptions(exEngine)

	inv, err := inventory.ScanRepository(ctx, dir, opts)
	if err != nil {
		fmt.Printf("Inventory error: %v\n", err)
		os.Exit(1)
	}

	structOpts := structure.DefaultDisplayOptions()
	structRoot, treeStr, err := structure.BuildProjectStructure(inv, "repo", structOpts)
	if err != nil {
		fmt.Printf("Structure error: %v\n", err)
		os.Exit(1)
	}
	_ = structRoot

	facts, err := parsers.AnalyzeRepositoryFacts(ctx, dir, inv)
	if err != nil {
		fmt.Printf("Parsers error: %v\n", err)
		os.Exit(1)
	}

	relGraph, _, err := graph.BuildRelationshipGraph(ctx, inv, nil, facts)
	if err != nil {
		fmt.Printf("Graph error: %v\n", err)
		os.Exit(1)
	}

	intel, err := intelligence.RunIntelligencePipeline(relGraph)
	if err != nil {
		fmt.Printf("Intelligence error: %v\n", err)
		os.Exit(1)
	}

	out := map[string]interface{}{
		"structureTree": treeStr,
		"evidence": "CONFIRMED evidence map generated",
		"confidenceTiers": []string{"CONFIRMED", "STRONGLY_INFERRED", "WEAKLY_INFERRED"},
		"hasScorecard": intel.Scorecard != nil,
	}

	b, _ := json.MarshalIndent(out, "", "  ")
	fmt.Println(string(b))
}
