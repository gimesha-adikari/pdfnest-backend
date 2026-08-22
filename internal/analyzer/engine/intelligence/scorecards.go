package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine"
)

type ScorecardEngine struct {
	hotspots []HotspotScore
	security []SecurityFinding
	test     TestIntelligence
	config   ConfigRuntimeIntelligence
}

func NewScorecardEngine(
	hotspots []HotspotScore,
	security []SecurityFinding,
	test TestIntelligence,
	config ConfigRuntimeIntelligence,
) *ScorecardEngine {
	return &ScorecardEngine{
		hotspots: hotspots,
		security: security,
		test:     test,
		config:   config,
	}
}

func (e *ScorecardEngine) Generate() *engine.Scorecard {
	scorecard := &engine.Scorecard{
		Components:      []engine.ScorecardQualityScore{},
		Recommendations: []engine.ScorecardRecommendation{},
	}

	totalScore := 0.0

	// 1. Security Score
	securityScore := 100.0
	for _, finding := range e.security {
		if finding.Severity == SeverityHigh {
			securityScore -= 20.0
			scorecard.Recommendations = append(scorecard.Recommendations, engine.ScorecardRecommendation{
				Title:        "Fix High Severity Security Issue: " + finding.Title,
				Description:  finding.Description + " " + finding.Remediation,
				Priority:     "high",
				TargetNodeID: finding.EntityID,
			})
		} else if finding.Severity == SeverityMedium {
			securityScore -= 10.0
			scorecard.Recommendations = append(scorecard.Recommendations, engine.ScorecardRecommendation{
				Title:        "Review Medium Severity Security Issue: " + finding.Title,
				Description:  finding.Description + " " + finding.Remediation,
				Priority:     "medium",
				TargetNodeID: finding.EntityID,
			})
		}
	}
	if securityScore < 0 {
		securityScore = 0
	}
	scorecard.Components = append(scorecard.Components, engine.ScorecardQualityScore{
		Component: "Security",
		Score:     securityScore,
		Grade:     getGrade(securityScore),
		Rationale: "Based on detected vulnerabilities.",
	})
	totalScore += securityScore

	// 2. Test Score
	testScore := 100.0
	for _, untested := range e.test.UntestedComponents {
		testScore -= 5.0
		if untested.FanIn > 2 { // high enough for recommendation
			scorecard.Recommendations = append(scorecard.Recommendations, engine.ScorecardRecommendation{
				Title:        "Add tests for high fan-in component",
				Description:  "This component is heavily used but lacks test coverage.",
				Priority:     "high",
				TargetNodeID: untested.EntityID,
			})
		}
	}
	if testScore < 0 {
		testScore = 0
	}
	scorecard.Components = append(scorecard.Components, engine.ScorecardQualityScore{
		Component: "Testing",
		Score:     testScore,
		Grade:     getGrade(testScore),
		Rationale: "Based on untested components with high fan-in.",
	})
	totalScore += testScore

	// 3. Hotspots Score
	hotspotScore := 100.0
	for _, hs := range e.hotspots {
		if hs.HotspotMetric > 10.0 {
			hotspotScore -= 5.0
			scorecard.Recommendations = append(scorecard.Recommendations, engine.ScorecardRecommendation{
				Title:        "Refactor high complexity hotspot",
				Description:  "This component has high centrality and complexity.",
				Priority:     "medium",
				TargetNodeID: hs.EntityID,
			})
		}
	}
	if hotspotScore < 0 {
		hotspotScore = 0
	}
	scorecard.Components = append(scorecard.Components, engine.ScorecardQualityScore{
		Component: "Maintainability",
		Score:     hotspotScore,
		Grade:     getGrade(hotspotScore),
		Rationale: "Based on component complexity and centrality hotspots.",
	})
	totalScore += hotspotScore

	// 4. Config Score
	configScore := 100.0
	for _, cfg := range e.config.ConfigUsages {
		if cfg.IsSecret && !cfg.InDocs {
			configScore -= 10.0
			scorecard.Recommendations = append(scorecard.Recommendations, engine.ScorecardRecommendation{
				Title:        "Document secret configuration: " + cfg.ConfigName,
				Description:  "Secret configuration should be documented.",
				Priority:     "medium",
				TargetNodeID: cfg.ConfigID,
			})
		}
	}
	if configScore < 0 {
		configScore = 0
	}
	scorecard.Components = append(scorecard.Components, engine.ScorecardQualityScore{
		Component: "Configuration",
		Score:     configScore,
		Grade:     getGrade(configScore),
		Rationale: "Based on configuration management and secrets handling.",
	})
	totalScore += configScore

	scorecard.OverallScore = totalScore / 4.0
	scorecard.OverallGrade = getGrade(scorecard.OverallScore)

	return scorecard
}

func getGrade(score float64) string {
	if score >= 90.0 {
		return "A"
	} else if score >= 80.0 {
		return "B"
	} else if score >= 70.0 {
		return "C"
	} else if score >= 60.0 {
		return "D"
	}
	return "F"
}
