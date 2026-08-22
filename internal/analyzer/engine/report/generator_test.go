package report

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"pdfnest-backend/internal/analyzer/engine/intelligence"
)

func strPtr(s string) *string {
	return &s
}

func TestGenerateMarkdownReport_Comprehensive(t *testing.T) {
	res := engine.NewEmptyCanonicalResult("sess-1", "test-repo", engine.SourceTypeGit)
	res.CreatedAt = time.Now()
	res.Metrics.TotalFiles = 50
	res.Metrics.IncludedFiles = 45
	res.Metrics.ExcludedFiles = 5
	res.Metrics.TotalBytes = 250000
	res.Metrics.LinesOfCode = 4500
	res.Metrics.Languages = []engine.LanguageMetric{
		{Name: "TypeScript", Percentage: 80.0, FileCount: 40, Bytes: 200000},
		{Name: "Go", Percentage: 20.0, FileCount: 10, Bytes: 50000},
	}
	res.Technologies = []engine.TechnologyItem{
		{
			Name:       "Next.js",
			Category:   "framework",
			Confidence: "confirmed",
			Version:    strPtr("15.0.0"),
		},
	}
	res.Routes = []engine.ApiRouteItem{
		{Method: "GET", Path: "/api/health", InferredHandler: strPtr("HealthHandler"), SourceFile: "src/routes.ts"},
	}
	res.StructureTree = "test-repo/\n├── src/\n└── package.json\n"

	// Mock Graph
	g := graph.NewRelationshipGraph()
	_ = g.AddEntity(&graph.GraphEntity{ID: "file:src/routes.ts", Kind: graph.EntityFile, Name: "routes.ts", Path: "src/routes.ts"})
	_ = g.AddEntity(&graph.GraphEntity{ID: "route:GET:/api/health", Kind: graph.EntityRoute, Name: "GET /api/health", Path: "src/routes.ts"})
	_ = g.AddEdge(&graph.GraphEdge{
		ID:         "edge:1",
		SourceID:   "file:src/routes.ts",
		TargetID:   "route:GET:/api/health",
		Type:       graph.RelDefines,
		Confidence: engine.EpistemicConfidenceConfirmed,
	})
	res.Graph = g.Serialize()
	res.GraphMetrics = &graph.GraphMetrics{
		EntityCount:         2,
		EdgeCount:           1,
		ConfirmedEdgeCount:  1,
		EvidenceCoveragePct: 100.0,
	}

	// Mock Intelligence
	res.Intelligence = &engine.IntelligenceAnalysis{
		Architecture: []intelligence.ArchitectureComponent{
			{EntityID: "route:GET:/api/health", Tier: intelligence.TierAPI, Confidence: engine.EpistemicConfidenceConfirmed},
		},
		Hotspots: []intelligence.HotspotScore{
			{EntityID: "file:src/routes.ts", FanIn: 3, FanOut: 1, Centrality: 0.8, Complexity: 10, IsTested: true, HotspotMetric: 4.5},
		},
		Scorecard: &engine.Scorecard{
			OverallScore: 92.0,
			OverallGrade: "A",
			Components: []engine.ScorecardQualityScore{
				{Component: "Security", Grade: "A", Score: 95.0, Rationale: "No security issues detected"},
			},
			Recommendations: []engine.ScorecardRecommendation{
				{Title: "Improve testing", Priority: "medium", Description: "Add more unit tests"},
			},
		},
	}

	res.Evidence = []engine.Evidence{
		{
			ID:          "ev:1",
			SourceType:  "framework",
			FilePath:    "package.json",
			Detector:    "manifest_dep",
			Confidence:  engine.EpistemicConfidenceConfirmed,
			Description: "Next.js dependency in package.json",
		},
	}

	md := GenerateMarkdownReport(res)
	require.NotEmpty(t, md)

	// Verify all 19 required sections are present
	assert.Contains(t, md, "# Architecture Analysis Report: test-repo")
	assert.Contains(t, md, "## 1. Executive Summary")
	assert.Contains(t, md, "## 2. Repository Overview")
	assert.Contains(t, md, "## 3. Project Structure")
	assert.Contains(t, md, "## 4. Technology Stack")
	assert.Contains(t, md, "## 5. Architecture / System Topology")
	assert.Contains(t, md, "## 6. Entities")
	assert.Contains(t, md, "## 7. Relationship Graph")
	assert.Contains(t, md, "## 8. Execution Flows")
	assert.Contains(t, md, "## 9. API / Entry Points")
	assert.Contains(t, md, "## 10. Change Impact Analysis")
	assert.Contains(t, md, "## 11. Engineering Hotspots")
	assert.Contains(t, md, "## 12. Security Findings")
	assert.Contains(t, md, "## 13. Test Intelligence")
	assert.Contains(t, md, "## 14. Architecture Scorecard")
	assert.Contains(t, md, "## 15. Evidence & Provenance")
	assert.Contains(t, md, "## 16. Environment / Deployment")
	assert.Contains(t, md, "## 17. Dependencies")
	assert.Contains(t, md, "## 18. Testing")
	assert.Contains(t, md, "## 19. Limitations / Confidence")
}
