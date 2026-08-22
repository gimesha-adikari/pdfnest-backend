package intelligence_test

import (
	"testing"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"pdfnest-backend/internal/analyzer/engine/intelligence"
)

func TestChangeImpact(t *testing.T) {
	g := graph.NewRelationshipGraph()
	
	// Create a central DB utility
	dbUtil := &graph.GraphEntity{ID: "db_util", Kind: graph.EntityFile, Name: "db_util.go"}
	g.AddEntity(dbUtil)
	
	// Services
	svc1 := &graph.GraphEntity{ID: "svc1", Kind: graph.EntityService, Name: "UserService"}
	svc2 := &graph.GraphEntity{ID: "svc2", Kind: graph.EntityService, Name: "BillingService"}
	g.AddEntity(svc1)
	g.AddEntity(svc2)
	
	// Routes
	route1 := &graph.GraphEntity{ID: "route1", Kind: graph.EntityRoute, Name: "GET /users"}
	route2 := &graph.GraphEntity{ID: "route2", Kind: graph.EntityRoute, Name: "POST /billing"}
	g.AddEntity(route1)
	g.AddEntity(route2)
	
	// Tests
	test1 := &graph.GraphEntity{ID: "test1", Kind: graph.EntityTest, Name: "db_util_test.go"}
	g.AddEntity(test1)
	
	// A leaf node
	leaf := &graph.GraphEntity{ID: "leaf", Kind: graph.EntityFile, Name: "utils.go"}
	g.AddEntity(leaf)
	
	// Edges (Backward from db_util to services to routes)
	g.AddEdge(&graph.GraphEdge{ID: "e1", SourceID: "svc1", TargetID: "db_util", Type: graph.RelDependsOn})
	g.AddEdge(&graph.GraphEdge{ID: "e2", SourceID: "svc2", TargetID: "db_util", Type: graph.RelDependsOn})
	g.AddEdge(&graph.GraphEdge{ID: "e3", SourceID: "route1", TargetID: "svc1", Type: graph.RelDependsOn})
	g.AddEdge(&graph.GraphEdge{ID: "e4", SourceID: "route2", TargetID: "svc2", Type: graph.RelDependsOn})
	g.AddEdge(&graph.GraphEdge{ID: "e5", SourceID: "test1", TargetID: "db_util", Type: graph.RelTests})
	
	engine := intelligence.NewChangeImpactEngine(g)
	
	// Test DB Util (High Impact)
	analysis, err := engine.Analyze("db_util", 10)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}
	
	if analysis.RiskScore != intelligence.RiskHigh {
		t.Errorf("Expected RiskHigh for db_util, got %s", analysis.RiskScore)
	}
	
	if analysis.DirectDependents != 3 { // svc1, svc2, test1
		t.Errorf("Expected 3 direct dependents, got %d", analysis.DirectDependents)
	}
	
	if analysis.IndirectDependents != 2 { // route1, route2
		t.Errorf("Expected 2 indirect dependents, got %d", analysis.IndirectDependents)
	}
	
	if analysis.AffectedRoutes != 2 {
		t.Errorf("Expected 2 affected routes, got %d", analysis.AffectedRoutes)
	}
	
	if analysis.AffectedServices != 2 {
		t.Errorf("Expected 2 affected services, got %d", analysis.AffectedServices)
	}
	
	// Test Leaf (Low Impact)
	analysisLeaf, err := engine.Analyze("leaf", 10)
	if err != nil {
		t.Fatalf("Analyze failed: %v", err)
	}
	
	if analysisLeaf.RiskScore != intelligence.RiskLow {
		t.Errorf("Expected RiskLow for leaf, got %s", analysisLeaf.RiskScore)
	}
}
