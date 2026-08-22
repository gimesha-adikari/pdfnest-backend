package intelligence

import (
	"testing"

	"pdfnest-backend/internal/analyzer/engine/graph"
)

func TestConfigRuntime(t *testing.T) {
	g := graph.NewRelationshipGraph()

	err := g.AddEntity(&graph.GraphEntity{
		ID:   "config:mock:DB_PASSWORD",
		Kind: graph.EntityConfig,
		Name: "DB_PASSWORD",
		Properties: map[string]any{
			"optional": false,
			"inDocs":   false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEntity(&graph.GraphEntity{
		ID:   "file:mock:db.go",
		Kind: graph.EntityFile,
		Name: "db.go",
		Path: "/src/db.go",
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEdge(&graph.GraphEdge{
		ID:       "edge1",
		SourceID: "file:mock:db.go",
		TargetID: "config:mock:DB_PASSWORD",
		Type:     graph.RelConsumes,
	})
	if err != nil {
		t.Fatal(err)
	}

	err = g.AddEntity(&graph.GraphEntity{
		ID:   "deploy:mock:dockerfile",
		Kind: graph.EntityDeployment,
		Name: "Dockerfile",
		Path: "/Dockerfile",
		Properties: map[string]any{
			"ports":      "8080:8080",
			"entrypoint": "./server",
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	engine := NewConfigRuntimeEngine(g)
	res := engine.Analyze()

	if len(res.ConfigUsages) != 1 {
		t.Fatalf("expected 1 config, got %d", len(res.ConfigUsages))
	}
	cfg := res.ConfigUsages["config:mock:DB_PASSWORD"]
	if !cfg.IsSecret {
		t.Errorf("expected DB_PASSWORD to be secret")
	}
	if cfg.InDocs {
		t.Errorf("expected InDocs to be false")
	}
	if !cfg.UsedInCode {
		t.Errorf("expected UsedInCode to be true")
	}

	if len(res.Runtime.Dockerfiles) != 1 {
		t.Errorf("expected 1 dockerfile, got %d", len(res.Runtime.Dockerfiles))
	}
	if len(res.Runtime.PortMappings) != 1 || res.Runtime.PortMappings[0] != "8080:8080" {
		t.Errorf("expected port 8080:8080")
	}
}
