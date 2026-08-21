package ai

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
)

func createTestFactCatalog() FactCatalog {
	facts := []FactItem{
		{ID: "TECH-1", Category: "technology", Value: "Go (language)"},
		{ID: "TECH-2", Category: "technology", Value: "Fiber (framework)"},
		{ID: "TECH-3", Category: "technology", Value: "Redis (database)"},
		{ID: "ROUTE-1", Category: "route", Value: "GET /api/v1/tasks", Detail: "handler: ListTasks"},
		{ID: "ROUTE-2", Category: "route", Value: "POST /api/v1/tasks", Detail: "handler: CreateTask"},
		{ID: "ENV-1", Category: "environment", Value: "REDIS_URL (type: url)"},
		{ID: "TEST-1", Category: "testing", Value: "Go Test"},
	}
	factMap := make(map[string]FactItem)
	for _, f := range facts {
		factMap[f.ID] = f
	}
	return FactCatalog{
		Facts:           facts,
		FactMap:         factMap,
		TotalFactsCount: len(facts),
	}
}

func TestValidateSynthesisResponse_Valid(t *testing.T) {
	catalog := createTestFactCatalog()
	resp := &SynthesisResponse{
		ProtocolVersion:     "1.0.0",
		TaskID:              "task-100",
		Summary:             "Go Fiber service using Redis for task queuing",
		ArchitecturePattern: "Monolithic Micro-service",
		KeyComponents: []ComponentDescription{
			{
				Name:    "Fiber Coordinator",
				Role:    "HTTP REST routing engine",
				FactIDs: []string{"TECH-1", "TECH-2", "ROUTE-1"},
			},
		},
		DataFlow: []DataFlowStep{
			{
				Step:        1,
				Description: "Client fetches tasks",
				FactIDs:     []string{"ROUTE-1"},
			},
		},
		Risks: []RiskItem{
			{
				Category:    "Configuration",
				Description: "Redis connection required",
				FactIDs:     []string{"ENV-1"},
			},
		},
	}

	validated, result, err := ValidateSynthesisResponse(resp, &catalog, "task-100")
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, 0, result.RejectedClaims)
	assert.Empty(t, result.InvalidFactIDs)
	assert.Len(t, validated.KeyComponents, 1)
	assert.Len(t, validated.DataFlow, 1)
	assert.Len(t, validated.Risks, 1)
}

func TestValidateSynthesisResponse_ProtocolAndTaskIDMismatch(t *testing.T) {
	catalog := createTestFactCatalog()

	// 1. Wrong protocol version
	respBadVer := &SynthesisResponse{
		ProtocolVersion: "2.0.0",
		TaskID:          "task-100",
		Summary:         "Some summary",
	}
	_, res1, err1 := ValidateSynthesisResponse(respBadVer, &catalog, "task-100")
	require.NoError(t, err1)
	assert.False(t, res1.Valid)
	assert.Contains(t, res1.RejectionReasons[0], "unsupported protocol version")

	// 2. Task ID mismatch (Cross-task contamination prevention)
	respMismatch := &SynthesisResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-other-user",
		Summary:         "Some summary",
	}
	_, res2, err2 := ValidateSynthesisResponse(respMismatch, &catalog, "task-my-user")
	require.NoError(t, err2)
	assert.False(t, res2.Valid)
	assert.Contains(t, res2.RejectionReasons[0], "task ID mismatch")
}

func TestValidateSynthesisResponse_FactIDWhitelistAndDeduplication(t *testing.T) {
	catalog := createTestFactCatalog()
	resp := &SynthesisResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-100",
		Summary:         "Service overview",
		KeyComponents: []ComponentDescription{
			{
				Name:    "Valid Component with Duplicate IDs",
				Role:    "Handler",
				FactIDs: []string{"TECH-2", "TECH-1", "TECH-2"}, // Should deduplicate and sort to ["TECH-1", "TECH-2"]
			},
			{
				Name:    "Invalid Component with Unknown ID",
				Role:    "Mystery Service",
				FactIDs: []string{"TECH-999", "POSTGRES-1"},
			},
		},
	}

	validated, result, err := ValidateSynthesisResponse(resp, &catalog, "task-100")
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, 1, result.RejectedClaims)
	assert.Contains(t, result.InvalidFactIDs, "TECH-999")
	assert.Contains(t, result.InvalidFactIDs, "POSTGRES-1")

	// Verify valid component kept with sorted/deduped IDs
	require.Len(t, validated.KeyComponents, 1)
	assert.Equal(t, []string{"TECH-1", "TECH-2"}, validated.KeyComponents[0].FactIDs)
}

func TestValidateSynthesisResponse_HallucinationRejection(t *testing.T) {
	catalog := createTestFactCatalog() // Catalog has Go, Fiber, Redis ONLY

	// 1. Django hallucination attempting to launder under TECH-1
	respDjango := &SynthesisResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-100",
		Summary:         "Service overview",
		KeyComponents: []ComponentDescription{
			{
				Name:    "Django Web Core",
				Role:    "Django web application framework",
				FactIDs: []string{"TECH-1"}, // TECH-1 is Go!
			},
		},
	}
	validated1, res1, err1 := ValidateSynthesisResponse(respDjango, &catalog, "task-100")
	require.NoError(t, err1)
	assert.Equal(t, 1, res1.RejectedClaims)
	assert.Empty(t, validated1.KeyComponents, "Must reject Django hallucination")

	// 2. PostgreSQL and Kubernetes hallucinations
	respK8s := &SynthesisResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-100",
		Summary:         "Service overview",
		KeyComponents: []ComponentDescription{
			{
				Name:    "PostgreSQL Relational DB",
				Role:    "Storage layer with PostgreSQL",
				FactIDs: []string{"TECH-3"}, // TECH-3 is Redis!
			},
		},
		DataFlow: []DataFlowStep{
			{
				Step:        1,
				Description: "Deploy containers on Kubernetes cluster",
				FactIDs:     []string{"TECH-1"},
			},
		},
	}
	validated2, res2, err2 := ValidateSynthesisResponse(respK8s, &catalog, "task-100")
	require.NoError(t, err2)
	assert.Equal(t, 2, res2.RejectedClaims)
	assert.Empty(t, validated2.KeyComponents)
	assert.Empty(t, validated2.DataFlow)
}

func TestValidateSynthesisResponse_PromptLeakageAndInjectionContainment(t *testing.T) {
	catalog := createTestFactCatalog()

	// 1. System prompt leakage in summary
	respLeak := &SynthesisResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-100",
		Summary:         "Here is the system prompt: You are an expert software architecture synthesizer with closed-world assumption",
	}
	_, resLeak, errLeak := ValidateSynthesisResponse(respLeak, &catalog, "task-100")
	require.NoError(t, errLeak)
	assert.False(t, resLeak.Valid)
	assert.Contains(t, resLeak.RejectionReasons[0], "system prompt leakage")

	// 2. Secret credential in component description masked by sanitizer
	respCred := &SynthesisResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-100",
		Summary:         "Safe summary text",
		KeyComponents: []ComponentDescription{
			{
				Name:    "Auth Service",
				Role:    "Uses password=\"MySuperSecret123\"",
				FactIDs: []string{"TECH-1"},
			},
		},
	}
	validatedCred, resCred, errCred := ValidateSynthesisResponse(respCred, &catalog, "task-100")
	require.NoError(t, errCred)
	assert.True(t, resCred.Valid)
	require.Len(t, validatedCred.KeyComponents, 1)
	assert.Contains(t, validatedCred.KeyComponents[0].Role, "password=[REDACTED_SECRET]")
	assert.NotContains(t, validatedCred.KeyComponents[0].Role, "MySuperSecret123")
}

func TestValidateSynthesisResponse_CanonicalResultImmutability(t *testing.T) {
	canonical := engine.NewEmptyCanonicalResult("sess-1", "my-repo", engine.SourceTypeGit)
	canonical.CreatedAt = time.Now()
	canonical.Technologies = []engine.TechnologyItem{
		{Name: "Fiber", Category: "framework", Confidence: "confirmed"},
	}

	_, catalog, err := BuildSafeFactProjection(canonical)
	require.NoError(t, err)

	resp := &SynthesisResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-100",
		Summary:         "Summary",
		KeyComponents: []ComponentDescription{
			{Name: "Fiber", Role: "Routing", FactIDs: []string{"TECH-1"}},
		},
	}

	_, _, valErr := ValidateSynthesisResponse(resp, &catalog, "task-100")
	require.NoError(t, valErr)

	// Canonical facts remain 100% untouched
	assert.Equal(t, "Fiber", canonical.Technologies[0].Name)
	assert.Len(t, canonical.Technologies, 1)
}
