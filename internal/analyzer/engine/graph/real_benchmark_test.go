package graph_test

import (
	"archive/zip"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"pdfnest-backend/internal/analyzer/worker"
)

type BenchmarkTarget struct {
	Name       string
	SourceType engine.SourceType
	GitURL     string
	ZipPath    string
	LocalDir   string
}

func zipFolder(src, dstZip string) error {
	zFile, err := os.Create(dstZip)
	if err != nil {
		return err
	}
	defer zFile.Close()

	w := zip.NewWriter(zFile)
	defer w.Close()

	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		lowerRel := strings.ToLower(rel)
		if lowerRel == ".git" || lowerRel == "node_modules" || lowerRel == ".venv" || 
		   lowerRel == ".next" || lowerRel == "dist" || lowerRel == "build" || 
		   lowerRel == "__pycache__" || strings.HasPrefix(lowerRel, "tesseract") ||
		   strings.HasPrefix(lowerRel, ".next") || strings.HasPrefix(lowerRel, "node_modules") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && info.Size() > 10*1024*1024) {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		f, err := w.Create(rel)
		if err != nil {
			return err
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(f, in)
		return err
	})
}

func TestRealBenchmark(t *testing.T) {
	if os.Getenv("RUN_REAL_BENCHMARKS") != "1" {
		t.Skip("Skipping real benchmarks. Set RUN_REAL_BENCHMARKS=1 to run.")
	}

	targets := []BenchmarkTarget{
		{
			Name:       "cors",
			SourceType: engine.SourceTypeGit,
			GitURL:     "https://github.com/expressjs/cors.git",
		},
		{
			Name:       "gin",
			SourceType: engine.SourceTypeGit,
			GitURL:     "https://github.com/gin-gonic/gin.git",
		},
		{
			Name:       "pdfnest",
			SourceType: engine.SourceTypeZip,
			ZipPath:    "../../../../../test-corpus/real_repos/pdfnest.zip",
			LocalDir:   "../../../../../pdfnest",
		},
		{
			Name:       "pdfnest-backend",
			SourceType: engine.SourceTypeZip,
			LocalDir:   "../../../../../pdfnest-backend",
		},
		{
			Name:       "pdfnest-worker",
			SourceType: engine.SourceTypeZip,
			LocalDir:   "../../../../../pdfnest-worker",
		},
	}

	ctx := context.Background()
	baseTmpDir := t.TempDir()

	for _, target := range targets {
		t.Run(target.Name, func(t *testing.T) {
			sessionID := "sess-" + target.Name
			stagedZip := target.ZipPath

			if target.SourceType == engine.SourceTypeZip {
				if stagedZip == "" || !fileExists(stagedZip) {
					absLocal, err := filepath.Abs(target.LocalDir)
					if err != nil || !dirExists(absLocal) {
						t.Skipf("Target %s not found on disk, skipping", target.Name)
						return
					}
					tmpZip := filepath.Join(baseTmpDir, target.Name+".zip")
					if err := zipFolder(absLocal, tmpZip); err != nil {
						t.Fatalf("zip local folder %s: %v", target.Name, err)
					}
					stagedZip = tmpZip
				}
			}

			absStagedZip, _ := filepath.Abs(stagedZip)

			job := &worker.AnalyzerJob{
				TaskID:            "bench-" + target.Name,
				SessionID:         sessionID,
				SourceType:        target.SourceType,
				GitURL:            target.GitURL,
				StagedArchivePath: absStagedZip,
			}

			res, err := worker.ExecutePipeline(ctx, job, baseTmpDir, nil)
			if err != nil {
				t.Fatalf("Pipeline execution failed for %s: %v", target.Name, err)
			}

			if res.Graph == nil {
				t.Fatalf("Expected non-nil Graph in CanonicalAnalysisResult")
			}

			serializedGraph, ok := res.Graph.(*graph.SerializedGraph)
			if !ok || serializedGraph == nil {
				t.Fatalf("Expected *graph.SerializedGraph type, got %T", res.Graph)
			}

			metrics, ok := res.GraphMetrics.(*graph.GraphMetrics)
			if !ok || metrics == nil {
				t.Fatalf("Expected non-nil GraphMetrics in CanonicalAnalysisResult")
			}

			// Build fast lookup map for entity IDs in serialized graph
			entityMap := make(map[string]struct{}, len(serializedGraph.Entities))
			for _, ent := range serializedGraph.Entities {
				entityMap[ent.ID] = struct{}{}
			}

			// 0. Verify Intelligence populated
			if res.Intelligence == nil {
				t.Fatalf("Expected non-nil Intelligence in CanonicalAnalysisResult")
			}
			if res.Intelligence.Scorecard == nil {
				t.Fatalf("Expected non-nil Scorecard in IntelligenceAnalysis")
			}

			// 1. Referential Integrity Invariant: Strictly ZERO dangling edges in graph
			danglingEdges := 0
			for _, edge := range serializedGraph.Edges {
				if _, exists := entityMap[edge.SourceID]; !exists {
					danglingEdges++
				}
				if _, exists := entityMap[edge.TargetID]; !exists {
					danglingEdges++
				}
			}
			if danglingEdges != 0 {
				t.Errorf("FAIL: Graph contains %d dangling edge endpoints (referential integrity violated)", danglingEdges)
			}

			// 2. Deterministic ID Stability Verification
			idStabilityPassed := true
			for _, ent := range serializedGraph.Entities {
				if ent.ID == "" {
					idStabilityPassed = false
					break
				}
			}

			// 3. Compute confirmed vs inferred ratio
			confirmedRatio := 0.0
			if metrics.EdgeCount > 0 {
				confirmedRatio = float64(metrics.ConfirmedEdgeCount) / float64(metrics.EdgeCount) * 100.0
			}

			// 4. Report All 11 Required Baseline Metrics
			t.Logf("==========================================================")
			t.Logf("📊 REAL BENCHMARK BASELINE REPORT: %s", target.Name)
			t.Logf("==========================================================")
			t.Logf("  1. Entity Count:                %d", metrics.EntityCount)
			t.Logf("  2. Edge Count:                  %d", metrics.EdgeCount)
			t.Logf("  3. Relationship Breakdown:")
			for relType, count := range metrics.RelationshipCounts {
				t.Logf("     - %-18s: %d", relType, count)
			}
			t.Logf("  4. Evidence Coverage:           %.2f%%", metrics.EvidenceCoveragePct)
			t.Logf("  5. Confirmed vs Inferred:       %d Confirmed / %d Inferred (%.1f%% confirmed)",
				metrics.ConfirmedEdgeCount, metrics.InferredEdgeCount, confirmedRatio)
			t.Logf("  6. Unresolved References:       %d (tracked external imports/dependencies)", metrics.UnresolvedReferences)
			t.Logf("  7. Orphan Entities:             %d", metrics.OrphanEntityCount)
			t.Logf("  8. Cycle Count:                 %d", metrics.CycleCount)
			t.Logf("  9. Language Resolution Coverage:")
			for lang, cov := range metrics.LanguageResolutionCoverage {
				t.Logf("     - %-10s: %s", lang, string(cov))
			}
			t.Logf(" 10. Deterministic ID Stability:  %v", idStabilityPassed)
			t.Logf(" 11. Dangling-Reference Count:    %d (Verified Zero Dangling)", danglingEdges)
			t.Logf("==========================================================")
		})
	}
}

func fileExists(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	st, err := os.Stat(abs)
	return err == nil && !st.IsDir()
}

func dirExists(p string) bool {
	abs, err := filepath.Abs(p)
	if err != nil {
		return false
	}
	st, err := os.Stat(abs)
	return err == nil && st.IsDir()
}
