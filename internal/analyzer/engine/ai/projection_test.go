package ai

import (
	"testing"
	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/intelligence"
)

func TestFactProjection(t *testing.T) {
	canonical := &engine.CanonicalAnalysisResult{
		Repository: engine.RepositoryInfo{Name: "test-repo"},
		Intelligence: &engine.IntelligenceAnalysis{
			Architecture: []intelligence.ArchitectureComponent{
				{EntityID: "frontend", Tier: intelligence.TierFrontend, Confidence: engine.EpistemicConfidenceConfirmed},
			},
			Flow: []intelligence.ExecutionFlow{
				{ID: "flow-1"},
			},
			Scorecard: &engine.Scorecard{
				OverallGrade: "A",
				OverallScore: 95.0,
			},
		},
	}

	proj, cat, err := BuildSafeFactProjection(canonical)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if proj.RepositoryName != "test-repo" {
		t.Errorf("expected test-repo, got %s", proj.RepositoryName)
	}

	if len(proj.Topology) != 1 {
		t.Errorf("expected 1 topology item, got %d", len(proj.Topology))
	}

	if len(proj.ExecutionFlows) != 1 {
		t.Errorf("expected 1 flow item, got %d", len(proj.ExecutionFlows))
	}

	if len(proj.Scorecards) != 1 {
		t.Errorf("expected 1 scorecard item, got %d", len(proj.Scorecards))
	}

	if cat.TotalFactsCount != 3 {
		t.Errorf("expected 3 facts in catalog, got %d", cat.TotalFactsCount)
	}
}
