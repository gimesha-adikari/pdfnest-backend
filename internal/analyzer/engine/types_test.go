package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCanonicalAnalysisResultSerialization(t *testing.T) {
	lineNum := 42
	snippet := "const redis = new Redis();"

	res := CanonicalAnalysisResult{
		SchemaVersion: SchemaVersion,
		AnalysisID:    "9b1deb4d-3b7d-4bad-9bdd-2b0d7b3dcb6d",
		CreatedAt:     time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC),
		Repository: RepositoryInfo{
			Name:       "test-repo",
			SourceType: SourceTypeGit,
		},
		Metrics: AnalysisMetrics{
			TotalFiles:    10,
			IncludedFiles: 8,
			ExcludedFiles: 2,
			TotalBytes:    10240,
			LinesOfCode:   450,
			Languages: []LanguageMetric{
				{Name: "TypeScript", Percentage: 80.0, FileCount: 6, Bytes: 8192},
				{Name: "JSON", Percentage: 20.0, FileCount: 2, Bytes: 2048},
			},
		},
		Technologies: []TechnologyItem{
			{
				ID:         "redis",
				Name:       "Redis",
				Category:   CategoryDatabase,
				Confidence: ConfidenceConfirmed,
				Evidence: []EvidenceItem{
					{
						FilePath:   "src/redis.ts",
						RuleType:   RuleSourceImport,
						Detail:     "ioredis client instantiation",
						LineNumber: &lineNum,
						Snippet:    &snippet,
					},
				},
				NegativeAssertionsPassed: []string{"PostgreSQL", "MySQL", "SQLite"},
			},
		},
		Dependencies: DependenciesBlock{
			Runtime: []DependencyItem{
				{Name: "ioredis", Version: "^5.3.2", Manager: "npm", IsDev: false},
			},
			Development: []DependencyItem{
				{Name: "typescript", Version: "^5.4.0", Manager: "npm", IsDev: true},
			},
		},
		Routes: []ApiRouteItem{
			{
				Method:       "GET",
				Path:         "/api/health",
				SourceFile:   "app/api/health/route.ts",
				AuthRequired: false,
			},
		},
		Environment: EnvironmentBlock{
			Variables: []EnvironmentVariable{
				{
					Name:         "REDIS_URL",
					Required:     true,
					InferredType: EnvVarURL,
					Source:       ".env.example",
					References:   []string{"src/redis.ts:5"},
				},
			},
		},
		Setup: SetupInfo{
			Prerequisites: []string{"Node.js >= 18"},
			InstallCommands: []SetupCommand{
				{Label: "Install dependencies", Cmd: "npm install"},
			},
			RunCommands: []SetupCommand{
				{Label: "Start dev server", Cmd: "npm run dev"},
			},
			BuildCommands: []SetupCommand{
				{Label: "Build production bundle", Cmd: "npm run build"},
			},
		},
		Testing: TestingInfo{
			Frameworks:      []string{"Jest"},
			TestCommands:    []string{"npm test"},
			TestDirectories: []string{"__tests__"},
			TestFileCount:   2,
		},
		Deployment: DeploymentInfo{
			DockerAvailable: true,
			DockerfilePaths: []string{"Dockerfile"},
			ComposePaths:    []string{"docker-compose.yml"},
			CIWorkflows: []DeploymentCIWorkflow{
				{Name: "CI", Path: ".github/workflows/ci.yml", Triggers: []string{"push", "pull_request"}},
			},
			TargetPlatforms: []string{"Docker", "Node.js"},
		},
		StructureTree: "test-repo/\n├── src/\n└── package.json",
		Provenance: Provenance{
			Engine:               EngineNameGoAnalyzerWorker,
			EngineVersion:        "1.0.0",
			RulesVersion:         "1.0.0",
			SchemaVersion:        SchemaVersion,
			DurationMs:           245,
			RulesEvaluatedCount:  12,
			ComplexityTier:       string(Tier1Instant),
			ComplexityScore:      24.5,
			SourceArtifactSha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
			ScopeHash:            "a591a6d40bf420404a011733cfb7b190d62c65bf0bcda32b57b277d9ad9f146e",
		},
	}

	jsonData, err := ToCanonicalJSON(&res)
	require.NoError(t, err)
	assert.NotEmpty(t, jsonData)

	parsed, err := FromCanonicalJSON(jsonData)
	require.NoError(t, err)
	assert.Equal(t, res.AnalysisID, parsed.AnalysisID)
	assert.Equal(t, res.SchemaVersion, parsed.SchemaVersion)
	assert.Equal(t, res.Repository.Name, parsed.Repository.Name)
	assert.Equal(t, len(res.Technologies), len(parsed.Technologies))
	assert.Equal(t, res.Technologies[0].Name, parsed.Technologies[0].Name)
	assert.Equal(t, res.Provenance.Engine, parsed.Provenance.Engine)
}

func TestCanonicalValidationErrors(t *testing.T) {
	invalidRes := CanonicalAnalysisResult{
		SchemaVersion: "0.9.0", // Invalid version
	}

	err := ValidateCanonicalResult(&invalidRes)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid schemaVersion")
}
