package ai

import (
	"fmt"
	"sort"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
)

// FactItem represents an individual verified repository fact with a deterministic ID.
type FactItem struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Value    string `json:"value"`
	Detail   string `json:"detail,omitempty"`
}

// FactCatalog holds the complete, deterministic list of facts available for AI synthesis.
type FactCatalog struct {
	Facts           []FactItem          `json:"facts"`
	FactMap         map[string]FactItem `json:"factMap"`
	TotalFactsCount int                 `json:"totalFactsCount"`
	Truncated       bool                `json:"truncated"`
}

// BuildSafeFactProjection extracts a sanitized, metadata-only projection and assigns deterministic Fact IDs.
// It never copies raw source code, repository file blobs, or environment variable secret values.
func BuildSafeFactProjection(canonical *engine.CanonicalAnalysisResult) (SafeFactProjection, FactCatalog, error) {
	if canonical == nil {
		return SafeFactProjection{}, FactCatalog{}, fmt.Errorf("canonical analysis result is nil")
	}

	projection := SafeFactProjection{
		RepositoryName:       sanitizeFactString(canonical.Repository.Name),
		PrimaryLanguages:     make([]string, 0),
		Technologies:         make([]string, 0),
		Endpoints:            make([]string, 0),
		Models:               make([]string, 0),
		EnvironmentVariables: make([]string, 0),
		TestingFrameworks:    make([]string, 0),
		DeploymentSystems:    make([]string, 0),
	}

	facts := make([]FactItem, 0)
	factMap := make(map[string]FactItem)

	// 1. Primary Languages
	langMetrics := make([]engine.LanguageMetric, len(canonical.Metrics.Languages))
	copy(langMetrics, canonical.Metrics.Languages)
	sort.Slice(langMetrics, func(i, j int) bool {
		if langMetrics[i].Percentage != langMetrics[j].Percentage {
			return langMetrics[i].Percentage > langMetrics[j].Percentage
		}
		return langMetrics[i].Name < langMetrics[j].Name
	})

	for _, lm := range langMetrics {
		if lm.Name != "" && lm.Percentage > 0.5 {
			projection.PrimaryLanguages = append(projection.PrimaryLanguages, lm.Name)
		}
	}

	// 2. Confirmed & Probable Technologies (TECH-*)
	techItems := make([]engine.TechnologyItem, 0, len(canonical.Technologies))
	for _, t := range canonical.Technologies {
		if t.Confidence == "confirmed" || t.Confidence == "probable" {
			techItems = append(techItems, t)
		}
	}
	sort.Slice(techItems, func(i, j int) bool {
		if techItems[i].Category != techItems[j].Category {
			return techItems[i].Category < techItems[j].Category
		}
		return techItems[i].Name < techItems[j].Name
	})

	for i, t := range techItems {
		id := fmt.Sprintf("TECH-%d", i+1)
		val := fmt.Sprintf("%s (%s)", sanitizeFactString(t.Name), t.Category)
		item := FactItem{
			ID:       id,
			Category: "technology",
			Value:    val,
			Detail:   fmt.Sprintf("confidence: %s", t.Confidence),
		}
		facts = append(facts, item)
		factMap[id] = item
		projection.Technologies = append(projection.Technologies, t.Name)
	}

	// 3. API Routes (ROUTE-*)
	routes := make([]engine.ApiRouteItem, len(canonical.Routes))
	copy(routes, canonical.Routes)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].Method != routes[j].Method {
			return routes[i].Method < routes[j].Method
		}
		return routes[i].Path < routes[j].Path
	})

	for i, r := range routes {
		id := fmt.Sprintf("ROUTE-%d", i+1)
		val := fmt.Sprintf("%s %s", r.Method, sanitizeFactString(r.Path))
		detail := ""
		if r.InferredHandler != nil && *r.InferredHandler != "" {
			detail = fmt.Sprintf("handler: %s", sanitizeFactString(*r.InferredHandler))
		}
		item := FactItem{
			ID:       id,
			Category: "route",
			Value:    val,
			Detail:   detail,
		}
		facts = append(facts, item)
		factMap[id] = item
		projection.Endpoints = append(projection.Endpoints, fmt.Sprintf("%s %s", r.Method, r.Path))
	}

	// 4. Environment Variable Names ONLY (ENV-*)
	// CRITICAL SECURITY INVARIANT: Values are completely omitted.
	envVars := make([]engine.EnvironmentVariable, len(canonical.Environment.Variables))
	copy(envVars, canonical.Environment.Variables)
	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})

	for i, ev := range envVars {
		id := fmt.Sprintf("ENV-%d", i+1)
		nameOnly := sanitizeFactString(ev.Name)
		val := nameOnly
		if ev.InferredType != "" {
			val = fmt.Sprintf("%s (type: %s)", nameOnly, ev.InferredType)
		}
		item := FactItem{
			ID:       id,
			Category: "environment",
			Value:    val,
			Detail:   fmt.Sprintf("required: %t", ev.Required),
		}
		facts = append(facts, item)
		factMap[id] = item
		projection.EnvironmentVariables = append(projection.EnvironmentVariables, nameOnly)
	}

	// 5. Testing Frameworks (TEST-*)
	testFrameworks := make([]string, len(canonical.Testing.Frameworks))
	copy(testFrameworks, canonical.Testing.Frameworks)
	sort.Strings(testFrameworks)

	for i, tf := range testFrameworks {
		id := fmt.Sprintf("TEST-%d", i+1)
		val := sanitizeFactString(tf)
		item := FactItem{
			ID:       id,
			Category: "testing",
			Value:    val,
		}
		facts = append(facts, item)
		factMap[id] = item
		projection.TestingFrameworks = append(projection.TestingFrameworks, tf)
	}

	// 6. Deployment Systems (DEPLOY-*)
	deployItems := make([]string, 0)
	if canonical.Deployment.DockerAvailable {
		deployItems = append(deployItems, "Docker")
	}
	for _, ci := range canonical.Deployment.CIWorkflows {
		if ci.Name != "" {
			deployItems = append(deployItems, fmt.Sprintf("CI: %s", ci.Name))
		}
	}
	sort.Strings(deployItems)

	for i, d := range deployItems {
		id := fmt.Sprintf("DEPLOY-%d", i+1)
		val := sanitizeFactString(d)
		item := FactItem{
			ID:       id,
			Category: "deployment",
			Value:    val,
		}
		facts = append(facts, item)
		factMap[id] = item
		projection.DeploymentSystems = append(projection.DeploymentSystems, d)
	}

	catalog := FactCatalog{
		Facts:           facts,
		FactMap:         factMap,
		TotalFactsCount: len(facts),
		Truncated:       false,
	}

	return projection, catalog, nil
}

func sanitizeFactString(s string) string {
	clean := strings.ReplaceAll(s, "\r", "")
	clean = strings.ReplaceAll(clean, "\n", " ")
	return strings.TrimSpace(clean)
}
