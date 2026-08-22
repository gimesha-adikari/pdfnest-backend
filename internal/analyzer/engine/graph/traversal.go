package graph

import (
	"pdfnest-backend/internal/analyzer/engine"
)

type TraversalDirection string

const (
	DirectionForward      TraversalDirection = "forward"
	DirectionBackward     TraversalDirection = "backward"
	DirectionBidirectional TraversalDirection = "bidirectional"
)

type TraversalFilters struct {
	RelationTypes   []RelationType
	ConfidenceTiers []engine.EpistemicConfidence
	EntityKinds     []EntityKind
}

func (f *TraversalFilters) matchEdge(edge *GraphEdge, targetEntity *GraphEntity) bool {
	if len(f.RelationTypes) > 0 {
		matched := false
		for _, rt := range f.RelationTypes {
			if edge.Type == rt {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if len(f.ConfidenceTiers) > 0 {
		matched := false
		for _, ct := range f.ConfidenceTiers {
			if edge.Confidence == ct {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	if len(f.EntityKinds) > 0 {
		matched := false
		for _, ek := range f.EntityKinds {
			if targetEntity.Kind == ek {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

type ImpactSubgraph struct {
	Entities map[string]*GraphEntity
	Edges    map[string]*GraphEdge
}

// TraverseImpact returns a subgraph of nodes reachable from startID within maxDepth.
func (g *RelationshipGraph) TraverseImpact(startID string, direction TraversalDirection, maxDepth int, filters TraversalFilters) (*ImpactSubgraph, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()

	startEntity, ok := g.Entities[startID]
	if !ok {
		return nil, nil // or error
	}

	subgraph := &ImpactSubgraph{
		Entities: make(map[string]*GraphEntity),
		Edges:    make(map[string]*GraphEdge),
	}

	visitedNodes := make(map[string]bool)
	visitedEdges := make(map[string]bool)

	type queueItem struct {
		id    string
		depth int
	}

	queue := []queueItem{{id: startID, depth: 0}}
	visitedNodes[startID] = true
	subgraph.Entities[startID] = startEntity

	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]

		if maxDepth > 0 && curr.depth >= maxDepth {
			continue
		}

		var edgesToConsider []*GraphEdge

		if direction == DirectionForward || direction == DirectionBidirectional {
			edgesToConsider = append(edgesToConsider, g.Outbound[curr.id]...)
		}
		if direction == DirectionBackward || direction == DirectionBidirectional {
			edgesToConsider = append(edgesToConsider, g.Inbound[curr.id]...)
		}

		for _, edge := range edgesToConsider {
			if visitedEdges[edge.ID] {
				continue
			}

			var targetID string
			if edge.SourceID == curr.id {
				targetID = edge.TargetID
			} else {
				targetID = edge.SourceID
			}

			targetEntity := g.Entities[targetID]

			if filters.matchEdge(edge, targetEntity) {
				visitedEdges[edge.ID] = true
				subgraph.Edges[edge.ID] = edge

				if !visitedNodes[targetID] {
					visitedNodes[targetID] = true
					subgraph.Entities[targetID] = targetEntity
					queue = append(queue, queueItem{id: targetID, depth: curr.depth + 1})
				}
			}
		}
	}

	return subgraph, nil
}
