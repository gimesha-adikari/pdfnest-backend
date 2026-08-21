package engine

import (
	"encoding/json"
	"fmt"
	"time"
)

// ValidateCanonicalResult performs structural validation of a CanonicalAnalysisResult.
func ValidateCanonicalResult(res *CanonicalAnalysisResult) error {
	if res == nil {
		return fmt.Errorf("result is nil")
	}
	if res.SchemaVersion != SchemaVersion {
		return fmt.Errorf("invalid schemaVersion: expected %s, got %s", SchemaVersion, res.SchemaVersion)
	}
	if res.AnalysisID == "" {
		return fmt.Errorf("missing analysisId")
	}
	if res.CreatedAt.IsZero() {
		return fmt.Errorf("missing createdAt timestamp")
	}
	if res.Repository.Name == "" {
		return fmt.Errorf("missing repository.name")
	}
	if res.Repository.SourceType == "" {
		return fmt.Errorf("missing repository.sourceType")
	}
	if res.Provenance.Engine == "" {
		return fmt.Errorf("missing provenance.engine")
	}
	if res.Provenance.ComplexityTier == "" {
		return fmt.Errorf("missing provenance.complexityTier")
	}
	return nil
}

// ToCanonicalJSON serializes a CanonicalAnalysisResult to indented JSON.
func ToCanonicalJSON(res *CanonicalAnalysisResult) ([]byte, error) {
	if err := ValidateCanonicalResult(res); err != nil {
		return nil, fmt.Errorf("validate result: %w", err)
	}
	return json.MarshalIndent(res, "", "  ")
}

// FromCanonicalJSON deserializes and validates a CanonicalAnalysisResult from raw JSON.
func FromCanonicalJSON(data []byte) (*CanonicalAnalysisResult, error) {
	var res CanonicalAnalysisResult
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, fmt.Errorf("unmarshal canonical result: %w", err)
	}
	if err := ValidateCanonicalResult(&res); err != nil {
		return nil, fmt.Errorf("validate canonical result: %w", err)
	}
	return &res, nil
}

// NewEmptyCanonicalResult creates a cleanly initialized CanonicalAnalysisResult.
func NewEmptyCanonicalResult(analysisID string, repoName string, srcType SourceType) *CanonicalAnalysisResult {
	return &CanonicalAnalysisResult{
		SchemaVersion: SchemaVersion,
		AnalysisID:    analysisID,
		CreatedAt:     time.Now().UTC(),
		Repository: RepositoryInfo{
			Name:       repoName,
			SourceType: srcType,
		},
		Metrics: AnalysisMetrics{
			Languages: make([]LanguageMetric, 0),
		},
		Technologies: make([]TechnologyItem, 0),
		Dependencies: DependenciesBlock{
			Runtime:     make([]DependencyItem, 0),
			Development: make([]DependencyItem, 0),
		},
		Routes:      make([]ApiRouteItem, 0),
		Environment: EnvironmentBlock{Variables: make([]EnvironmentVariable, 0)},
		Setup: SetupInfo{
			Prerequisites:   make([]string, 0),
			InstallCommands: make([]SetupCommand, 0),
			RunCommands:     make([]SetupCommand, 0),
			BuildCommands:   make([]SetupCommand, 0),
		},
		Testing: TestingInfo{
			Frameworks:      make([]string, 0),
			TestCommands:    make([]string, 0),
			TestDirectories: make([]string, 0),
		},
		Deployment: DeploymentInfo{
			DockerfilePaths: make([]string, 0),
			ComposePaths:    make([]string, 0),
			CIWorkflows:     make([]DeploymentCIWorkflow, 0),
			TargetPlatforms: make([]string, 0),
		},
		StructureTree: "",
		Provenance: Provenance{
			Engine:        EngineNameGoAnalyzerWorker,
			EngineVersion: "1.0.0",
			RulesVersion:  "1.0.0",
			SchemaVersion: SchemaVersion,
		},
	}
}
