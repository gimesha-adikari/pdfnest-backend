package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
)

// RunIntelligencePipeline executes all intelligence engines and aggregates the findings.
func RunIntelligencePipeline(g *graph.RelationshipGraph) (*engine.IntelligenceAnalysis, error) {
	if g == nil {
		return nil, nil
	}

	res := &engine.IntelligenceAnalysis{}

	archEngine := NewArchitectureEngine(g)
	res.Architecture = archEngine.Analyze()

	flowEngine := NewExecutionFlowEngine(g)
	res.Flow = flowEngine.Analyze()

	hotspotEngine := NewHotspotEngine(g)
	hotspots := hotspotEngine.Analyze()
	res.Hotspots = hotspots

	securityEngine := NewSecurityEngine(g)
	security, _ := securityEngine.Analyze()
	res.Security = security

	testEngine := NewTestIntelligenceEngine(g)
	testData := testEngine.Analyze(2)
	res.Test = testData

	configEngine := NewConfigRuntimeEngine(g)
	configData := configEngine.Analyze()
	res.Config = configData

	impactEngine := NewChangeImpactEngine(g)
	impactResults := make(map[string]*ImpactAnalysis)
	for i, hs := range hotspots {
		if i >= 5 {
			break
		}
		if impact, err := impactEngine.Analyze(hs.EntityID, 3); err == nil {
			impactResults[hs.EntityID] = impact
		}
	}
	res.Impact = impactResults

	scorecardEngine := NewScorecardEngine(hotspots, security, testData, configData)
	res.Scorecard = scorecardEngine.Generate()

	return res, nil
}
