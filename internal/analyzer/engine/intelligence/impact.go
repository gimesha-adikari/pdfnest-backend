package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine/graph"
)

type RiskScore string

const (
	RiskHigh   RiskScore = "HIGH"
	RiskMedium RiskScore = "MEDIUM"
	RiskLow    RiskScore = "LOW"
)

type ImpactAnalysis struct {
	EntityID            string
	DirectDependents    int
	IndirectDependents  int
	ForwardDependencies int
	AffectedRoutes      int
	AffectedServices    int
	AffectedTests       int
	RiskScore           RiskScore
}

type ChangeImpactEngine struct {
	graph *graph.RelationshipGraph
}

func NewChangeImpactEngine(g *graph.RelationshipGraph) *ChangeImpactEngine {
	return &ChangeImpactEngine{graph: g}
}

func (e *ChangeImpactEngine) Analyze(entityID string, maxDepth int) (*ImpactAnalysis, error) {
	backward, err := e.graph.TraverseImpact(entityID, graph.DirectionBackward, maxDepth, graph.TraversalFilters{})
	if err != nil {
		return nil, err
	}

	forward, err := e.graph.TraverseImpact(entityID, graph.DirectionForward, maxDepth, graph.TraversalFilters{})
	if err != nil {
		return nil, err
	}

	analysis := &ImpactAnalysis{
		EntityID: entityID,
	}

	directMap := make(map[string]bool)
	
	// Avoid panics if graph is mutated, though we assume read-only here
	if e.graph.Inbound != nil {
		for _, edge := range e.graph.Inbound[entityID] {
			directMap[edge.SourceID] = true
		}
	}

	for id, entity := range backward.Entities {
		if id == entityID {
			continue
		}

		if directMap[id] {
			analysis.DirectDependents++
		} else {
			analysis.IndirectDependents++
		}

		switch entity.Kind {
		case graph.EntityRoute:
			analysis.AffectedRoutes++
		case graph.EntityService:
			analysis.AffectedServices++
		case graph.EntityTest:
			analysis.AffectedTests++
		}
	}

	for id := range forward.Entities {
		if id == entityID {
			continue
		}
		analysis.ForwardDependencies++
	}

	if analysis.AffectedRoutes > 0 || analysis.AffectedServices > 1 || analysis.AffectedTests > 5 {
		analysis.RiskScore = RiskHigh
	} else if analysis.DirectDependents > 3 || analysis.IndirectDependents > 5 || analysis.AffectedServices > 0 {
		analysis.RiskScore = RiskMedium
	} else {
		analysis.RiskScore = RiskLow
	}

	return analysis, nil
}
