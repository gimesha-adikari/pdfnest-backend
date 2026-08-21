package ai

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	SupportedProtocolVersion = "1.0.0"
	MaxSummaryLength         = 4096
	MaxPatternLength         = 512
	MaxStringFieldLength     = 2048
	MaxComponentsCount       = 30
	MaxDataFlowCount         = 30
	MaxRisksCount            = 30
)

var (
	reSystemPromptLeak = regexp.MustCompile(`(?i)(system\s*prompt|developer\s*message|system\s*instruction|core\s*operational\s*invariants|closed-world\s*assumption|untrusted\s*data\s*containment)`)
	reInjectionEcho    = regexp.MustCompile(`(?i)(ignore\s+all\s+previous\s+instructions|reveal\s+your\s+prompt|pretend\s+you\s+are)`)

	knownTechKeywords = []string{
		"django", "flask", "fastapi", "spring", "rails", "express", "nestjs", "laravel",
		"gin", "fiber", "actix", "asp.net", "tornado",
		"postgresql", "postgres", "mysql", "mongodb", "mongo", "redis", "sqlite",
		"oracle", "cassandra", "dynamodb", "elasticsearch", "neo4j",
		"kubernetes", "k8s", "docker", "helm", "terraform", "ansible",
	}
)

// ValidationResult captures detailed diagnostic metrics regarding the grounding and safety of an AI response.
type ValidationResult struct {
	Valid            bool     `json:"valid"`
	RejectedClaims   int      `json:"rejectedClaims"`
	InvalidFactIDs   []string `json:"invalidFactIds,omitempty"`
	SanitizedFields  []string `json:"sanitizedFields,omitempty"`
	RejectionReasons []string `json:"rejectionReasons,omitempty"`
}

// ValidateSynthesisResponse rigorously enforces the closed-world boundary, Fact-ID whitelisting,
// schema invariants, anti-hallucination checks, and secret sanitization on untrusted AI outputs.
func ValidateSynthesisResponse(
	response *SynthesisResponse,
	catalog *FactCatalog,
	expectedTaskID string,
) (*SynthesisResponse, ValidationResult, error) {
	result := ValidationResult{
		Valid:            true,
		InvalidFactIDs:   make([]string, 0),
		SanitizedFields:  make([]string, 0),
		RejectionReasons: make([]string, 0),
	}

	if response == nil {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons, "nil synthesis response")
		return nil, result, fmt.Errorf("nil synthesis response")
	}

	if catalog == nil {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons, "nil fact catalog")
		return nil, result, fmt.Errorf("nil fact catalog")
	}

	// 1. Protocol Version Validation
	if response.ProtocolVersion != SupportedProtocolVersion {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons,
			fmt.Sprintf("unsupported protocol version '%s' (expected '%s')", response.ProtocolVersion, SupportedProtocolVersion))
		return nil, result, nil
	}

	// 2. Task-ID Validation (Anti Cross-Task Contamination)
	if expectedTaskID != "" && response.TaskID != expectedTaskID {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons,
			fmt.Sprintf("task ID mismatch: response '%s' != expected '%s'", response.TaskID, expectedTaskID))
		return nil, result, nil
	}

	// 3. Summary Field Validation
	trimmedSummary := strings.TrimSpace(response.Summary)
	if trimmedSummary == "" {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons, "summary field is required and non-empty")
		return nil, result, nil
	}
	if len(trimmedSummary) > MaxSummaryLength {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons,
			fmt.Sprintf("summary length %d exceeds maximum %d bytes", len(trimmedSummary), MaxSummaryLength))
		return nil, result, nil
	}

	// 4. Prompt Leakage & Injection Echo Detection
	if reSystemPromptLeak.MatchString(trimmedSummary) || reInjectionEcho.MatchString(trimmedSummary) {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons, "system prompt leakage or injection echo detected in summary")
		return nil, result, nil
	}

	if len(response.ArchitecturePattern) > MaxPatternLength {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons,
			fmt.Sprintf("architecturePattern length exceeds maximum %d bytes", MaxPatternLength))
		return nil, result, nil
	}

	// 5. Build Validated Defensive Copy
	validated := &SynthesisResponse{
		ProtocolVersion:     response.ProtocolVersion,
		TaskID:              response.TaskID,
		Summary:             ScrubSecrets(trimmedSummary),
		ArchitecturePattern: ScrubSecrets(strings.TrimSpace(response.ArchitecturePattern)),
		KeyComponents:       make([]ComponentDescription, 0),
		DataFlow:            make([]DataFlowStep, 0),
		Risks:               make([]RiskItem, 0),
		Provider:            response.Provider,
		Model:               response.Model,
		InputTokens:         response.InputTokens,
		OutputTokens:        response.OutputTokens,
		DurationMs:          response.DurationMs,
	}

	if validated.Summary != trimmedSummary {
		result.SanitizedFields = append(result.SanitizedFields, "summary")
	}

	// 6. Validate Key Components
	if len(response.KeyComponents) > MaxComponentsCount {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons,
			fmt.Sprintf("keyComponents count %d exceeds maximum %d", len(response.KeyComponents), MaxComponentsCount))
		return nil, result, nil
	}

	for _, comp := range response.KeyComponents {
		name := strings.TrimSpace(comp.Name)
		role := strings.TrimSpace(comp.Role)
		if name == "" || role == "" {
			result.RejectedClaims++
			result.RejectionReasons = append(result.RejectionReasons, "component rejected: empty name or role")
			continue
		}
		if len(name) > MaxStringFieldLength || len(role) > MaxStringFieldLength {
			result.RejectedClaims++
			result.RejectionReasons = append(result.RejectionReasons, "component rejected: field exceeds max length")
			continue
		}

		validIDs, invalidIDs := validateAndDeduplicateFactIDs(comp.FactIDs, catalog)
		if len(invalidIDs) > 0 {
			result.InvalidFactIDs = append(result.InvalidFactIDs, invalidIDs...)
			result.RejectedClaims++
			result.RejectionReasons = append(result.RejectionReasons,
				fmt.Sprintf("component '%s' references unknown Fact IDs: %v", name, invalidIDs))
			continue
		}

		if len(validIDs) == 0 && catalog.TotalFactsCount > 0 {
			result.RejectedClaims++
			result.RejectionReasons = append(result.RejectionReasons,
				fmt.Sprintf("component '%s' lacks supporting Fact IDs", name))
			continue
		}

		// Anti-Hallucination & Claim Grounding Check
		if isUngroundedHallucination(name+" "+role, validIDs, catalog) {
			result.RejectedClaims++
			result.RejectionReasons = append(result.RejectionReasons,
				fmt.Sprintf("component '%s' asserts technology unsupported by cited Fact IDs", name))
			continue
		}

		validated.KeyComponents = append(validated.KeyComponents, ComponentDescription{
			Name:    ScrubSecrets(name),
			Role:    ScrubSecrets(role),
			FactIDs: validIDs,
		})
	}

	// 7. Validate Data Flow
	if len(response.DataFlow) > MaxDataFlowCount {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons,
			fmt.Sprintf("dataFlow steps count %d exceeds maximum %d", len(response.DataFlow), MaxDataFlowCount))
		return nil, result, nil
	}

	for _, step := range response.DataFlow {
		desc := strings.TrimSpace(step.Description)
		if desc == "" || len(desc) > MaxStringFieldLength {
			result.RejectedClaims++
			continue
		}

		validIDs, invalidIDs := validateAndDeduplicateFactIDs(step.FactIDs, catalog)
		if len(invalidIDs) > 0 {
			result.InvalidFactIDs = append(result.InvalidFactIDs, invalidIDs...)
			result.RejectedClaims++
			result.RejectionReasons = append(result.RejectionReasons,
				fmt.Sprintf("dataFlow step %d references unknown Fact IDs: %v", step.Step, invalidIDs))
			continue
		}

		if isUngroundedHallucination(desc, validIDs, catalog) {
			result.RejectedClaims++
			result.RejectionReasons = append(result.RejectionReasons,
				fmt.Sprintf("dataFlow step %d asserts unsupported technology", step.Step))
			continue
		}

		validated.DataFlow = append(validated.DataFlow, DataFlowStep{
			Step:        step.Step,
			Description: ScrubSecrets(desc),
			FactIDs:     validIDs,
		})
	}

	// 8. Validate Risks
	if len(response.Risks) > MaxRisksCount {
		result.Valid = false
		result.RejectionReasons = append(result.RejectionReasons,
			fmt.Sprintf("risks count %d exceeds maximum %d", len(response.Risks), MaxRisksCount))
		return nil, result, nil
	}

	for _, risk := range response.Risks {
		cat := strings.TrimSpace(risk.Category)
		desc := strings.TrimSpace(risk.Description)
		if cat == "" || desc == "" || len(desc) > MaxStringFieldLength {
			result.RejectedClaims++
			continue
		}

		validIDs, invalidIDs := validateAndDeduplicateFactIDs(risk.FactIDs, catalog)
		if len(invalidIDs) > 0 {
			result.InvalidFactIDs = append(result.InvalidFactIDs, invalidIDs...)
			result.RejectedClaims++
			result.RejectionReasons = append(result.RejectionReasons,
				fmt.Sprintf("risk '%s' references unknown Fact IDs: %v", cat, invalidIDs))
			continue
		}

		validated.Risks = append(validated.Risks, RiskItem{
			Category:    ScrubSecrets(cat),
			Description: ScrubSecrets(desc),
			FactIDs:     validIDs,
		})
	}

	// Sort InvalidFactIDs deterministically
	sort.Strings(result.InvalidFactIDs)

	return validated, result, nil
}

func validateAndDeduplicateFactIDs(ids []string, catalog *FactCatalog) ([]string, []string) {
	seen := make(map[string]bool)
	var valid []string
	var invalid []string

	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true

		if _, exists := catalog.FactMap[trimmed]; exists {
			valid = append(valid, trimmed)
		} else {
			invalid = append(invalid, trimmed)
		}
	}

	sort.Strings(valid)
	sort.Strings(invalid)
	return valid, invalid
}

func isUngroundedHallucination(claimText string, citedIDs []string, catalog *FactCatalog) bool {
	claimLower := strings.ToLower(claimText)

	// Combine all text values from the cited Fact IDs
	var citedFactsText strings.Builder
	for _, id := range citedIDs {
		if item, ok := catalog.FactMap[id]; ok {
			citedFactsText.WriteString(" ")
			citedFactsText.WriteString(strings.ToLower(item.Value))
			citedFactsText.WriteString(" ")
			citedFactsText.WriteString(strings.ToLower(item.Detail))
		}
	}
	combinedFacts := citedFactsText.String()

	// Check if claim explicitly asserts a known tech keyword that is NOT present in the cited facts
	for _, kw := range knownTechKeywords {
		if containsWord(claimLower, kw) {
			// Keyword is asserted in claim text; verify if it is grounded in cited facts or the overall catalog
			if !containsWord(combinedFacts, kw) {
				// If not present in cited facts, verify if it exists anywhere in the catalog
				existsInCatalog := false
				for _, fact := range catalog.Facts {
					if containsWord(strings.ToLower(fact.Value), kw) {
						existsInCatalog = true
						break
					}
				}
				if !existsInCatalog {
					return true // Definite ungrounded hallucination
				}
			}
		}
	}

	return false
}

func containsWord(text, word string) bool {
	// Simple word boundary check
	pattern := `\b` + regexp.QuoteMeta(word) + `\b`
	matched, _ := regexp.MatchString(pattern, text)
	return matched
}
