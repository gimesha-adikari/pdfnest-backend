package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine/graph"
	"testing"
)

func TestExecutionFlow(t *testing.T) {
	g := graph.NewRelationshipGraph()

	routeEntity := &graph.GraphEntity{
		ID:   "route1",
		Name: "GET /api/data",
		Kind: graph.EntityRoute,
	}
	controllerEntity := &graph.GraphEntity{
		ID:   "ctrl1",
		Name: "DataController",
		Kind: graph.EntitySymbol,
	}
	dbEntity := &graph.GraphEntity{
		ID:   "db1",
		Name: "UsersDB",
		Kind: graph.EntityStorage,
	}

	g.AddEntity(routeEntity)
	g.AddEntity(controllerEntity)
	g.AddEntity(dbEntity)

	// Route -> Controller
	g.AddEdge(&graph.GraphEdge{
		ID:       "edge1",
		SourceID: "route1",
		TargetID: "ctrl1",
		Type:     graph.RelCalls,
	})

	// Controller -> DB
	g.AddEdge(&graph.GraphEdge{
		ID:       "edge2",
		SourceID: "ctrl1",
		TargetID: "db1",
		Type:     graph.RelPersistsTo,
	})

	// Add a cycle for cycle detection test
	g.AddEdge(&graph.GraphEdge{
		ID:       "edge_cycle",
		SourceID: "ctrl1",
		TargetID: "route1",
		Type:     graph.RelCalls,
	})

	flowEngine := NewExecutionFlowEngine(g)
	flows := flowEngine.Analyze()

	if len(flows) == 0 {
		t.Fatalf("Expected at least one flow, got 0")
	}

	flow := flows[0]
	if len(flow.Steps) != 2 {
		t.Errorf("Expected 2 steps in flow, got %d", len(flow.Steps))
	}

	if flow.Steps[0].EntityID != "route1" || flow.Steps[0].TargetID != "ctrl1" {
		t.Errorf("Step 1 incorrect: %+v", flow.Steps[0])
	}

	if flow.Steps[1].EntityID != "ctrl1" || flow.Steps[1].TargetID != "db1" {
		t.Errorf("Step 2 incorrect: %+v", flow.Steps[1])
	}
}
