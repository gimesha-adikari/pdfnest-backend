package intelligence_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"pdfnest-backend/internal/analyzer/engine/intelligence"
)

func TestScorecards(t *testing.T) {
	hotspots := []intelligence.HotspotScore{
		{EntityID: "file:main.go", HotspotMetric: 15.0},
	}
	
	security := []intelligence.SecurityFinding{
		{
			Title: "Test Sec",
			Severity: intelligence.SeverityHigh,
			EntityID: "file:auth.go",
			Description: "Auth problem",
			Remediation: "Fix it",
		},
	}
	
	testData := intelligence.TestIntelligence{
		UntestedComponents: []intelligence.UntestedComponent{
			{EntityID: "file:util.go", FanIn: 5},
		},
	}
	
	configData := intelligence.ConfigRuntimeIntelligence{
		ConfigUsages: map[string]intelligence.ConfigUsage{
			"env:SECRET": {ConfigID: "env:SECRET", ConfigName: "SECRET", IsSecret: true, InDocs: false},
		},
	}

	engine := intelligence.NewScorecardEngine(hotspots, security, testData, configData)
	scorecard := engine.Generate()

	assert.NotNil(t, scorecard)
	assert.Len(t, scorecard.Components, 4)
	
	// Should have recommendations from each component
	assert.GreaterOrEqual(t, len(scorecard.Recommendations), 4)

	// Validate Security score (100 - 20)
	var secScore float64
	for _, c := range scorecard.Components {
		if c.Component == "Security" {
			secScore = c.Score
		}
	}
	assert.Equal(t, 80.0, secScore)

	hasActionableRec := false
	for _, rec := range scorecard.Recommendations {
		if rec.TargetNodeID != "" {
			hasActionableRec = true
		}
	}
	assert.True(t, hasActionableRec, "Produces an explainable scorecard with at least one actionable recommendation citing a specific file/symbol.")
}
