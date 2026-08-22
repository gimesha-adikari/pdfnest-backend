package intelligence_test

import (
	"testing"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"pdfnest-backend/internal/analyzer/engine/intelligence"
)

func TestHotspots(t *testing.T) {
	g := graph.NewRelationshipGraph()

	// High complexity, untested, high fan-in node (Primary Hotspot)
	n1 := &graph.GraphEntity{
		ID:   "n1",
		Kind: graph.EntityFile,
		Name: "core_engine.go",
		Properties: map[string]any{
			"complexity": 10.0,
		},
	}
	g.AddEntity(n1)

	// Low complexity, tested, low fan-in node (Not a Hotspot)
	n2 := &graph.GraphEntity{
		ID:   "n2",
		Kind: graph.EntityFile,
		Name: "utils.go",
		Properties: map[string]any{
			"complexity": 2.0,
		},
	}
	g.AddEntity(n2)
	
	testNode := &graph.GraphEntity{
		ID:   "test_n2",
		Kind: graph.EntityTest,
		Name: "utils_test.go",
	}
	g.AddEntity(testNode)

	// Consumers of n1
	c1 := &graph.GraphEntity{ID: "c1", Kind: graph.EntityService}
	c2 := &graph.GraphEntity{ID: "c2", Kind: graph.EntityService}
	g.AddEntity(c1)
	g.AddEntity(c2)

	g.AddEdge(&graph.GraphEdge{ID: "e1", SourceID: "c1", TargetID: "n1", Type: graph.RelDependsOn})
	g.AddEdge(&graph.GraphEdge{ID: "e2", SourceID: "c2", TargetID: "n1", Type: graph.RelDependsOn})

	// Consumer of n2, and test of n2
	g.AddEdge(&graph.GraphEdge{ID: "e3", SourceID: "c1", TargetID: "n2", Type: graph.RelImports})
	g.AddEdge(&graph.GraphEdge{ID: "e4", SourceID: "test_n2", TargetID: "n2", Type: graph.RelTests})

	engine := intelligence.NewHotspotEngine(g)
	scores := engine.Analyze()

	if len(scores) == 0 {
		t.Fatal("Expected scores, got none")
	}

	if scores[0].EntityID != "n1" {
		t.Errorf("Expected n1 to be the primary hotspot, got %s", scores[0].EntityID)
	}

	var n1Score, n2Score *intelligence.HotspotScore
	for i := range scores {
		if scores[i].EntityID == "n1" {
			n1Score = &scores[i]
		}
		if scores[i].EntityID == "n2" {
			n2Score = &scores[i]
		}
	}

	if n1Score.IsTested {
		t.Error("Expected n1 to be untested")
	}
	if !n2Score.IsTested {
		t.Error("Expected n2 to be tested")
	}

	if n1Score.HotspotMetric <= n2Score.HotspotMetric {
		t.Errorf("Expected n1 hotspot metric (%f) > n2 hotspot metric (%f)", n1Score.HotspotMetric, n2Score.HotspotMetric)
	}
	
	if n1Score.FanIn != 2 {
		t.Errorf("Expected n1 FanIn to be 2, got %d", n1Score.FanIn)
	}
	
	if n2Score.FanIn != 1 {
		t.Errorf("Expected n2 FanIn to be 1, got %d", n2Score.FanIn)
	}
}
