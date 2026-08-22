package graph

import (
	"testing"
)

func TestGraph(t *testing.T) {
	g := NewRelationshipGraph()

	entA := &GraphEntity{ID: "A", Kind: EntityFile}
	entB := &GraphEntity{ID: "B", Kind: EntityFile}

	g.AddEntity(entA)
	g.AddEntity(entB)

	edge := &GraphEdge{
		ID:       "E1",
		SourceID: "A",
		TargetID: "B",
		Type:     RelImports,
	}

	err := g.AddEdge(edge)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(g.Outbound["A"]) != 1 {
		t.Errorf("expected 1 outbound edge for A")
	}
	if len(g.Inbound["B"]) != 1 {
		t.Errorf("expected 1 inbound edge for B")
	}

	// Test referential integrity
	err = g.AddEdge(&GraphEdge{ID: "E2", SourceID: "A", TargetID: "C", Type: RelImports})
	if err == nil {
		t.Errorf("expected error when adding edge to non-existent target")
	}
}

func TestTraversal(t *testing.T) {
	g := NewRelationshipGraph()
	g.AddEntity(&GraphEntity{ID: "A", Kind: EntityFile})
	g.AddEntity(&GraphEntity{ID: "B", Kind: EntityFile})
	g.AddEntity(&GraphEntity{ID: "C", Kind: EntityFile})

	g.AddEdge(&GraphEdge{ID: "E1", SourceID: "A", TargetID: "B", Type: RelImports})
	g.AddEdge(&GraphEdge{ID: "E2", SourceID: "B", TargetID: "C", Type: RelImports})

	// Forward traversal
	sub, err := g.TraverseImpact("A", DirectionForward, 10, TraversalFilters{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sub.Entities) != 3 {
		t.Errorf("expected 3 entities in subgraph, got %d", len(sub.Entities))
	}
	if len(sub.Edges) != 2 {
		t.Errorf("expected 2 edges in subgraph, got %d", len(sub.Edges))
	}

	// Backward traversal
	sub, err = g.TraverseImpact("C", DirectionBackward, 10, TraversalFilters{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sub.Entities) != 3 {
		t.Errorf("expected 3 entities in backward subgraph, got %d", len(sub.Entities))
	}

	// Depth limited traversal
	sub, err = g.TraverseImpact("A", DirectionForward, 1, TraversalFilters{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sub.Entities) != 2 {
		t.Errorf("expected 2 entities with depth=1, got %d", len(sub.Entities))
	}
}

func TestCycles(t *testing.T) {
	g := NewRelationshipGraph()
	g.AddEntity(&GraphEntity{ID: "A", Kind: EntityFile})
	g.AddEntity(&GraphEntity{ID: "B", Kind: EntityFile})
	g.AddEntity(&GraphEntity{ID: "C", Kind: EntityFile})

	g.AddEdge(&GraphEdge{ID: "E1", SourceID: "A", TargetID: "B", Type: RelImports})
	g.AddEdge(&GraphEdge{ID: "E2", SourceID: "B", TargetID: "C", Type: RelImports})
	g.AddEdge(&GraphEdge{ID: "E3", SourceID: "C", TargetID: "A", Type: RelImports})

	cycles := g.FindExplainableCycles()
	if len(cycles) != 1 {
		t.Errorf("expected 1 cycle, got %d", len(cycles))
	} else {
		if len(cycles[0].Hops) != 3 {
			t.Errorf("expected 3 hops in cycle, got %d", len(cycles[0].Hops))
		}
	}
}
