package intelligence

import (
	"testing"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
)

func TestSecurityEngine(t *testing.T) {
	g := graph.NewRelationshipGraph()

	err := g.AddEntity(&graph.GraphEntity{
		ID:   "symbol:mock:func",
		Kind: graph.EntitySymbol,
		Name: "fetchData",
		Evidence: []engine.Evidence{
			{
				ID:          "ev-1",
				Confidence:  engine.EpistemicConfidenceConfirmed,
				Description: `resp, err := http.Get(req.URL.Query().Get("url"))`,
			},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error adding entity: %v", err)
	}

	se := NewSecurityEngine(g)
	findings, err := se.Analyze()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(findings))
	}

	finding := findings[0]
	if finding.RuleID != "SSRF_001" {
		t.Errorf("expected SSRF_001, got %s", finding.RuleID)
	}
	if finding.Severity != SeverityHigh {
		t.Errorf("expected HIGH severity, got %s", finding.Severity)
	}
	if finding.Confidence != engine.EpistemicConfidenceConfirmed {
		t.Errorf("expected CONFIRMED confidence, got %s", finding.Confidence)
	}
}
