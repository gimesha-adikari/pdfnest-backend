package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"testing"
)

func TestArchitecture(t *testing.T) {
	g := graph.NewRelationshipGraph()

	workerEntity := &graph.GraphEntity{
		ID:   "worker1",
		Name: "worker.go",
		Path: "/src/worker.go",
		Kind: graph.EntityFile,
	}
	amqpEntity := &graph.GraphEntity{
		ID:   "amqp",
		Name: "amqp",
		Path: "github.com/streadway/amqp",
		Kind: graph.EntityPackage,
	}
	dbEntity := &graph.GraphEntity{
		ID:   "db1",
		Name: "db",
		Path: "database",
		Kind: graph.EntityStorage,
	}

	g.AddEntity(workerEntity)
	g.AddEntity(amqpEntity)
	g.AddEntity(dbEntity)

	g.AddEdge(&graph.GraphEdge{
		ID:       "edge1",
		SourceID: "worker1",
		TargetID: "amqp",
		Type:     graph.RelImports,
	})

	g.AddEdge(&graph.GraphEdge{
		ID:       "edge2",
		SourceID: "worker1",
		TargetID: "db1",
		Type:     graph.RelCalls,
	})

	archEngine := NewArchitectureEngine(g)
	components := archEngine.Analyze()

	foundWorker := false
	for _, comp := range components {
		if comp.EntityID == "worker1" {
			if comp.Tier != TierWorker {
				t.Errorf("Expected tier Worker, got %s", comp.Tier)
			}
			if comp.Confidence != engine.EpistemicConfidenceConfirmed {
				t.Errorf("Expected confidence CONFIRMED, got %s", comp.Confidence)
			}
			foundWorker = true
		}
	}

	if !foundWorker {
		t.Errorf("Worker component not found")
	}
}
