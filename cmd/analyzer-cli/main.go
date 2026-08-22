package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/exclusion"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"pdfnest-backend/internal/analyzer/engine/intelligence"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/parsers"
	"pdfnest-backend/internal/analyzer/engine/report"
	"pdfnest-backend/internal/analyzer/engine/structure"
)

func main() {
	outMdPath := flag.String("o", "", "Path to write output markdown report")
	outJsonPath := flag.String("json", "", "Path to write canonical JSON result")
	repoNameFlag := flag.String("name", "", "Repository name override")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("Usage: analyzer-cli [flags] <repository-directory>")
		flag.PrintDefaults()
		os.Exit(1)
	}

	repoDir := args[0]
	absRepoDir, err := filepath.Abs(repoDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error resolving absolute path: %v\n", err)
		os.Exit(1)
	}

	repoName := *repoNameFlag
	if repoName == "" {
		repoName = filepath.Base(absRepoDir)
	}

	startTime := time.Now()
	ctx := context.Background()

	fmt.Printf("🔍 Starting Analysis on repository: %s (%s)\n", repoName, absRepoDir)

	// 1. Setup Exclusion Engine with standard preset rules
	exConfig := exclusion.Config{
		CustomPatterns: []string{
			"**/.git/**",
			"**/.venv/**",
			"**/venv/**",
			"**/node_modules/**",
			"**/.next/**",
			"**/dist/**",
			"**/build/**",
			"**/coverage/**",
			"**/tesseract/**",
		},
	}
	exEngine := exclusion.NewEngine(exConfig)
	opts := inventory.DefaultScannerOptions(exEngine)

	// 2. Scan Inventory
	fmt.Println("📁 Scanning repository files and directory inventory...")
	inv, err := inventory.ScanRepository(ctx, absRepoDir, opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during inventory scan: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Total files: %d (Included: %d, Excluded: %d, Total bytes: %d)\n",
		inv.TotalFiles, inv.IncludedFiles, inv.ExcludedFiles, inv.TotalBytes)

	// 3. Build Canonical Structure Tree
	fmt.Println("🌳 Building canonical project structure tree...")
	structOpts := structure.DefaultDisplayOptions()
	projStructure, asciiTree, err := structure.BuildProjectStructure(inv, repoName, structOpts)
	if err != nil && inv.IncludedFiles > 0 {
		fmt.Fprintf(os.Stderr, "Error building project structure: %v\n", err)
		os.Exit(1)
	}

	// 4. Extract Static Facts & Manifests
	fmt.Println("📦 Parsing manifests and extracting technology facts...")
	facts, err := parsers.AnalyzeRepositoryFacts(ctx, absRepoDir, inv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error analyzing facts: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Discovered: %d technologies, %d runtime deps, %d dev deps, %d routes, %d env vars\n",
		len(facts.Technologies), len(facts.RuntimeDeps), len(facts.DevDeps), len(facts.Routes), len(facts.Environment))

	// 5. Build Semantic Relationship Graph
	fmt.Println("🕸️  Constructing entity relationship graph...")
	relGraph, graphMetrics, err := graph.BuildRelationshipGraph(ctx, inv, nil, facts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building relationship graph: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   Graph: %d entities, %d edges (Confirmed: %d, Inferred: %d)\n",
		graphMetrics.EntityCount, graphMetrics.EdgeCount, graphMetrics.ConfirmedEdgeCount, graphMetrics.InferredEdgeCount)

	// 6. Run Deep Intelligence Pipeline
	fmt.Println("🧠 Running deep intelligence engines (Architecture, Flows, Hotspots, Security, Tests, Scorecard)...")
	intelRes, err := intelligence.RunIntelligencePipeline(relGraph)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error running intelligence pipeline: %v\n", err)
		os.Exit(1)
	}

	// 7. Assemble Canonical Result
	res := engine.NewEmptyCanonicalResult("cli-session-1", repoName, engine.SourceTypeLocalFolder)
	res.CreatedAt = startTime
	res.Metrics.TotalFiles = inv.TotalFiles
	res.Metrics.IncludedFiles = inv.IncludedFiles
	res.Metrics.ExcludedFiles = inv.ExcludedFiles
	res.Metrics.TotalBytes = inv.TotalBytes

	// Calculate Language Metrics
	langStats := make(map[string]*engine.LanguageMetric)
	var totalLangBytes int64
	for _, f := range inv.Files {
		if f.IsExcluded || f.Language == "" {
			continue
		}
		if entry, ok := langStats[f.Language]; ok {
			entry.FileCount++
			entry.Bytes += f.Size
		} else {
			langStats[f.Language] = &engine.LanguageMetric{
				Name:      f.Language,
				FileCount: 1,
				Bytes:     f.Size,
			}
		}
		totalLangBytes += f.Size
	}
	res.Metrics.Languages = make([]engine.LanguageMetric, 0, len(langStats))
	for _, lm := range langStats {
		if totalLangBytes > 0 {
			lm.Percentage = float64(lm.Bytes) / float64(totalLangBytes) * 100.0
		}
		res.Metrics.Languages = append(res.Metrics.Languages, *lm)
	}
	sort.Slice(res.Metrics.Languages, func(i, j int) bool {
		return res.Metrics.Languages[i].Bytes > res.Metrics.Languages[j].Bytes
	})

	res.Technologies = facts.Technologies
	res.Dependencies.Runtime = facts.RuntimeDeps
	res.Dependencies.Development = facts.DevDeps
	res.Routes = facts.Routes
	res.Environment.Variables = facts.Environment
	res.Setup = facts.Setup
	res.Testing = facts.Testing
	res.Deployment = facts.Deployment
	res.Structure = projStructure
	res.StructureTree = asciiTree
	res.Graph = relGraph.Serialize()
	res.GraphMetrics = graphMetrics
	res.Intelligence = intelRes

	// Aggregate Canonical Evidence
	evidenceMap := make(map[string]engine.Evidence)
	if relGraph != nil {
		for _, ent := range relGraph.Entities {
			for _, ev := range ent.Evidence {
				if ev.ID != "" {
					evidenceMap[ev.ID] = ev
				}
			}
		}
		for _, ed := range relGraph.Edges {
			for _, ev := range ed.Evidence {
				if ev.ID != "" {
					evidenceMap[ev.ID] = ev
				}
			}
		}
	}
	for _, tech := range facts.Technologies {
		for idx, ev := range tech.Evidence {
			evID := fmt.Sprintf("ev:tech:%s:%d", tech.Name, idx+1)
			if _, exists := evidenceMap[evID]; !exists {
				lineStart := ev.LineNumber
				conf := engine.EpistemicConfidenceConfirmed
				if tech.Confidence == engine.ConfidenceProbable {
					conf = engine.EpistemicConfidenceStronglyInferred
				} else if tech.Confidence == engine.ConfidencePossible {
					conf = engine.EpistemicConfidenceWeaklyInferred
				}
				evidenceMap[evID] = engine.Evidence{
					ID:          evID,
					SourceType:  string(tech.Category),
					FilePath:    ev.FilePath,
					LineStart:   lineStart,
					Detector:    string(ev.RuleType),
					Confidence:  conf,
					Description: fmt.Sprintf("%s detected in %s: %s", tech.Name, ev.FilePath, ev.Detail),
				}
			}
		}
	}
	evList := make([]engine.Evidence, 0, len(evidenceMap))
	for _, ev := range evidenceMap {
		evList = append(evList, ev)
	}
	sort.Slice(evList, func(i, j int) bool {
		return evList[i].ID < evList[j].ID
	})
	res.Evidence = evList

	// Provenance
	durationMs := time.Since(startTime).Milliseconds()
	complexityInput := engine.ComplexityInput{
		TotalFiles:     inv.TotalFiles,
		IncludedFiles:  inv.IncludedFiles,
		TotalBytes:     inv.TotalBytes,
		IncludedBytes:  inv.IncludedBytes,
		ManifestCount:  len(inv.ManifestsFound),
		LanguageCount:  len(inv.LanguagesFound),
		MaxDepth:       inv.MaximumDepth,
		DeepASTRequest: false,
	}
	classification := engine.ClassifyWorkload(complexityInput, engine.DefaultPolicyLimits())
	res.Provenance = engine.Provenance{
		Engine:               "platen_analyzer_engine",
		EngineVersion:        "2.0.0",
		RulesVersion:         "2.0.0",
		SchemaVersion:        "1.0.0",
		DurationMs:           durationMs,
		RulesEvaluatedCount:  len(facts.Technologies)*10 + len(inv.Files),
		ComplexityTier:       string(classification.Tier),
		ComplexityScore:      classification.ComplexityScore,
		ScopeHash:            "cli-scope-hash",
		SourceArtifactSha256: "local-dir-sha256",
	}

	// 8. Generate 19-Section Markdown Report
	fmt.Println("📄 Generating comprehensive 19-section Architecture Analysis Report...")
	mdReport := report.GenerateMarkdownReport(res)

	// 9. Write outputs
	if *outMdPath != "" {
		if err := os.MkdirAll(filepath.Dir(*outMdPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating output dir: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*outMdPath, []byte(mdReport), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing markdown report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Architecture Analysis Report written to: %s\n", *outMdPath)
	}

	if *outJsonPath != "" {
		if err := os.MkdirAll(filepath.Dir(*outJsonPath), 0755); err != nil {
			fmt.Fprintf(os.Stderr, "Error creating JSON output dir: %v\n", err)
			os.Exit(1)
		}
		jsonBytes, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error serializing JSON result: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*outJsonPath, jsonBytes, 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing JSON result: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("✅ Canonical JSON Result written to: %s\n", *outJsonPath)
	}

	fmt.Printf("\n✨ Analysis completed in %d ms! Summary:\n", durationMs)
	fmt.Printf("   • Included Files: %d / %d\n", res.Metrics.IncludedFiles, res.Metrics.TotalFiles)
	fmt.Printf("   • Entities Extracted: %d\n", graphMetrics.EntityCount)
	fmt.Printf("   • Relationships Identified: %d\n", graphMetrics.EdgeCount)
	fmt.Printf("   • Evidence Items: %d\n", len(res.Evidence))
	if res.Intelligence != nil && res.Intelligence.Scorecard != nil {
		fmt.Printf("   • Quality Scorecard: Grade %s (%.1f/100)\n", res.Intelligence.Scorecard.OverallGrade, res.Intelligence.Scorecard.OverallScore)
	}
}
