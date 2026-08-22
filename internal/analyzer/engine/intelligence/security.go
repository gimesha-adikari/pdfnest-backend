package intelligence

import (
	"regexp"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
)

type Severity string

const (
	SeverityHigh   Severity = "HIGH"
	SeverityMedium Severity = "MEDIUM"
	SeverityLow    Severity = "LOW"
)

type SecurityFinding struct {
	RuleID      string
	Title       string
	Description string
	Severity    Severity
	Confidence  engine.EpistemicConfidence
	EntityID    string
	Evidence    []engine.Evidence
	Remediation string
}

type SecurityEngine struct {
	graph *graph.RelationshipGraph
}

func NewSecurityEngine(g *graph.RelationshipGraph) *SecurityEngine {
	return &SecurityEngine{graph: g}
}

func (e *SecurityEngine) Analyze() ([]SecurityFinding, error) {
	var findings []SecurityFinding

	ssrfRegex := regexp.MustCompile(`(?i)http\.Get\([^)]*req\.URL\.Query\(\)\.Get\("url"\)`)
	execRegex := regexp.MustCompile(`(?i)exec\.Command\([^)]*req\.URL`)

	for id, entity := range e.graph.Entities {
		for _, ev := range entity.Evidence {
			if ev.Confidence != engine.EpistemicConfidenceConfirmed {
				continue
			}

			if ssrfRegex.MatchString(ev.Description) {
				findings = append(findings, SecurityFinding{
					RuleID:      "SSRF_001",
					Title:       "Server-Side Request Forgery",
					Description: "Unvalidated URL passed to HTTP client.",
					Severity:    SeverityHigh,
					Confidence:  engine.EpistemicConfidenceConfirmed,
					EntityID:    id,
					Evidence:    []engine.Evidence{ev},
					Remediation: "Validate the URL against an allowlist.",
				})
			}
			if execRegex.MatchString(ev.Description) {
				findings = append(findings, SecurityFinding{
					RuleID:      "EXEC_001",
					Title:       "Command Injection",
					Description: "Unvalidated input passed to command execution.",
					Severity:    SeverityHigh,
					Confidence:  engine.EpistemicConfidenceConfirmed,
					EntityID:    id,
					Evidence:    []engine.Evidence{ev},
					Remediation: "Do not pass user input to command execution.",
				})
			}
		}
	}

	return findings, nil
}
