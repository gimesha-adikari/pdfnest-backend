package graph

type LanguageCoverage string

type GraphMetrics struct {
	EntityCount                int
	EdgeCount                  int
	RelationshipCounts         map[RelationType]int
	EvidenceCoveragePct        float64
	ConfirmedEdgeCount         int
	InferredEdgeCount          int
	UnresolvedReferences       int
	CycleCount                 int
	OrphanEntityCount          int
	LanguageResolutionCoverage map[string]LanguageCoverage
}

func ComputeGraphMetrics(g *RelationshipGraph) *GraphMetrics {
	if g == nil {
		return &GraphMetrics{}
	}

	g.mu.RLock()
	defer g.mu.RUnlock()

	metrics := &GraphMetrics{
		EntityCount:                len(g.Entities),
		EdgeCount:                  len(g.Edges),
		RelationshipCounts:         make(map[RelationType]int),
		LanguageResolutionCoverage: make(map[string]LanguageCoverage),
	}

	hasEvidenceCount := 0

	for _, edge := range g.Edges {
		metrics.RelationshipCounts[edge.Type]++

		if len(edge.Evidence) > 0 {
			hasEvidenceCount++
		}

		if edge.Provenance.Kind == RelationshipKindDirect {
			metrics.ConfirmedEdgeCount++
		} else {
			metrics.InferredEdgeCount++
		}
	}

	if metrics.EdgeCount > 0 {
		metrics.EvidenceCoveragePct = float64(hasEvidenceCount) / float64(metrics.EdgeCount) * 100.0
	}

	// Calculate orphan entities (no inbound, no outbound)
	for id := range g.Entities {
		if len(g.Outbound[id]) == 0 && len(g.Inbound[id]) == 0 {
			metrics.OrphanEntityCount++
		}
	}

	return metrics
}
