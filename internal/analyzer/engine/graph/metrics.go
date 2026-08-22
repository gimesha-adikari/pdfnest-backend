package graph

type LanguageCoverage string

type GraphMetrics struct {
	EntityCount                int                         `json:"entityCount"`
	EdgeCount                  int                         `json:"edgeCount"`
	RelationshipCounts         map[RelationType]int        `json:"relationshipCounts"`
	EvidenceCoveragePct        float64                     `json:"evidenceCoveragePct"`
	ConfirmedEdgeCount         int                         `json:"confirmedEdgeCount"`
	InferredEdgeCount          int                         `json:"inferredEdgeCount"`
	UnresolvedReferences       int                         `json:"unresolvedReferences"`
	CycleCount                 int                         `json:"cycleCount"`
	OrphanEntityCount          int                         `json:"orphanEntityCount"`
	LanguageResolutionCoverage map[string]LanguageCoverage `json:"languageResolutionCoverage"`
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
