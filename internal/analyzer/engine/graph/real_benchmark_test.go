package graph_test

import (
	"context"
	"os"
	"testing"
	
	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"pdfnest-backend/internal/analyzer/worker"
)

func TestRealBenchmark(t *testing.T) {
	if os.Getenv("RUN_REAL_BENCHMARKS") != "1" {
		t.Skip("Skipping real benchmarks. Set RUN_REAL_BENCHMARKS=1 to run.")
	}

	repos := []struct{
		Name string
		Url string
	}{
		{"cors", "https://github.com/expressjs/cors"},
		{"gin", "https://github.com/gin-gonic/gin"},
	}

	ctx := context.Background()

	for _, repo := range repos {
		t.Run(repo.Name, func(t *testing.T) {
			job := &worker.AnalyzerJob{
				TaskID: "bench-" + repo.Name,
				SessionID: "sess-" + repo.Name,
				SourceType: engine.SourceTypeGit,
				GitURL: repo.Url,
			}
			
			res, err := worker.ExecutePipeline(ctx, job, "/tmp", nil)
			if err != nil {
				t.Fatalf("Pipeline failed: %v", err)
			}
			
			metrics, ok := res.GraphMetrics.(*graph.GraphMetrics)
			if !ok {
				t.Fatalf("Expected GraphMetrics type")
			}
			
			t.Logf("--- Baseline Metrics for %s ---", repo.Name)
			t.Logf("Entity Count: %d", metrics.EntityCount)
			t.Logf("Edge Count: %d", metrics.EdgeCount)
			t.Logf("Unresolved References: %d", metrics.UnresolvedReferences)
			t.Logf("Orphan Entities: %d", metrics.OrphanEntityCount)
			t.Logf("Cycle Count: %d", metrics.CycleCount)
			
			if metrics.UnresolvedReferences != 0 {
				t.Errorf("Expected 0 dangling/unresolved references, got %d", metrics.UnresolvedReferences)
			}
		})
	}
}
