package intelligence

import (
	"testing"

	"pdfnest-backend/internal/analyzer/engine/graph"
)

func TestIntelligenceEngineAnalysis(t *testing.T) {
	g := graph.NewRelationshipGraph()

	err := g.AddEntity(&graph.GraphEntity{
		ID:   "symbol:mock:central",
		Kind: graph.EntitySymbol,
		Name: "CentralService",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEntity(&graph.GraphEntity{
		ID:   "symbol:mock:caller1",
		Kind: graph.EntitySymbol,
		Name: "Caller1",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEntity(&graph.GraphEntity{
		ID:   "symbol:mock:caller2",
		Kind: graph.EntitySymbol,
		Name: "Caller2",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEntity(&graph.GraphEntity{
		ID:   "test:mock:service_test",
		Kind: graph.EntityTest,
		Name: "CentralServiceTest",
		Path: "/tests/central_test.go",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEntity(&graph.GraphEntity{
		ID:   "symbol:mock:untested",
		Kind: graph.EntitySymbol,
		Name: "UntestedService",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEdge(&graph.GraphEdge{
		ID:       "edge1",
		SourceID: "symbol:mock:caller1",
		TargetID: "symbol:mock:central",
		Type:     graph.RelCalls,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEdge(&graph.GraphEdge{
		ID:       "edge2",
		SourceID: "symbol:mock:caller2",
		TargetID: "symbol:mock:central",
		Type:     graph.RelCalls,
	})
	if err != nil {
		t.Fatal(err)
	}

	// tests edge for central
	err = g.AddEdge(&graph.GraphEdge{
		ID:       "edge3",
		SourceID: "test:mock:service_test",
		TargetID: "symbol:mock:central",
		Type:     graph.RelTests,
	})
	if err != nil {
		t.Fatal(err)
	}

	// calls to untested
	err = g.AddEdge(&graph.GraphEdge{
		ID:       "edge4",
		SourceID: "symbol:mock:caller1",
		TargetID: "symbol:mock:untested",
		Type:     graph.RelCalls,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEdge(&graph.GraphEdge{
		ID:       "edge5",
		SourceID: "symbol:mock:caller2",
		TargetID: "symbol:mock:untested",
		Type:     graph.RelCalls,
	})
	if err != nil {
		t.Fatal(err)
	}
	
	// third call to push fan-in above threshold (e.g., fan-in > 2)
	err = g.AddEntity(&graph.GraphEntity{
		ID:   "symbol:mock:caller3",
		Kind: graph.EntitySymbol,
		Name: "Caller3",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEdge(&graph.GraphEdge{
		ID:       "edge6",
		SourceID: "symbol:mock:caller3",
		TargetID: "symbol:mock:untested",
		Type:     graph.RelCalls,
	})
	if err != nil {
		t.Fatal(err)
	}

	engine := NewTestIntelligenceEngine(g)
	result := engine.Analyze(2)

	if len(result.Mappings) != 1 {
		t.Errorf("expected 1 test mapping, got %d", len(result.Mappings))
	} else {
		if result.Mappings[0].EntityID != "symbol:mock:central" {
			t.Errorf("expected mapping for central, got %s", result.Mappings[0].EntityID)
		}
		if len(result.Mappings[0].TestFiles) != 1 || result.Mappings[0].TestFiles[0] != "/tests/central_test.go" {
			t.Errorf("expected central_test.go in mapping")
		}
	}

	if len(result.UntestedComponents) != 1 {
		t.Errorf("expected 1 untested component, got %d", len(result.UntestedComponents))
	} else {
		if result.UntestedComponents[0].EntityID != "symbol:mock:untested" {
			t.Errorf("expected untested component to be 'symbol:mock:untested', got %s", result.UntestedComponents[0].EntityID)
		}
	}
}
