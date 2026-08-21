package engine

import "time"

// SchemaVersion defines the authoritative version of the CanonicalAnalysisResult schema.
const SchemaVersion = "1.0.0"

// EngineNameGoAnalyzerWorker is the identifier for the dedicated Go analyzer worker engine.
const EngineNameGoAnalyzerWorker = "go_analyzer_worker"

// EngineNamePythonWorkerEscalated is the identifier for the Python worker escalation engine.
const EngineNamePythonWorkerEscalated = "python_worker_escalated"

// SourceType represents the origin medium of the analyzed repository.
type SourceType string

const (
	SourceTypeGit         SourceType = "git"
	SourceTypeZip         SourceType = "zip"
	SourceTypeLocalFolder SourceType = "local_folder"
)

// ConfidenceLevel represents the evidence certainty for a detected technology.
type ConfidenceLevel string

const (
	ConfidenceConfirmed   ConfidenceLevel = "confirmed"
	ConfidenceProbable    ConfidenceLevel = "probable"
	ConfidencePossible    ConfidenceLevel = "possible"
	ConfidenceNotDetected ConfidenceLevel = "not_detected"
)

// TechnologyCategory categorizes detected architectural tools and runtimes.
type TechnologyCategory string

const (
	CategoryLanguage       TechnologyCategory = "language"
	CategoryFramework      TechnologyCategory = "framework"
	CategoryDatabase       TechnologyCategory = "database"
	CategoryCache          TechnologyCategory = "cache"
	CategoryInfrastructure TechnologyCategory = "infrastructure"
	CategoryTesting        TechnologyCategory = "testing"
	CategoryBuildTool      TechnologyCategory = "build_tool"
)

// RuleType defines the category of deterministic evidence found.
type RuleType string

const (
	RuleManifestDep  RuleType = "manifest_dep"
	RuleConfigFile   RuleType = "config_file"
	RuleSourceImport RuleType = "source_import"
	RuleDockerImage  RuleType = "docker_image"
	RuleEnvVar       RuleType = "env_var"
	RuleFilePresence RuleType = "file_presence"
)

// EvidenceItem represents a concrete file/line evidence proof.
type EvidenceItem struct {
	FilePath   string   `json:"filePath"`
	RuleType   RuleType `json:"ruleType"`
	Detail     string   `json:"detail"`
	LineNumber *int     `json:"lineNumber"`
	Snippet    *string  `json:"snippet"`
}

// TechnologyItem represents a verified technology with traceable evidence.
type TechnologyItem struct {
	ID                       string             `json:"id"`
	Name                     string             `json:"name"`
	Category                 TechnologyCategory `json:"category"`
	Version                  *string            `json:"version"`
	Confidence               ConfidenceLevel    `json:"confidence"`
	Evidence                 []EvidenceItem     `json:"evidence"`
	NegativeAssertionsPassed []string           `json:"negativeAssertionsPassed"`
}

// DependencyItem represents a direct runtime or development package dependency.
type DependencyItem struct {
	Name    string  `json:"name"`
	Version string  `json:"version"`
	Manager string  `json:"manager"`
	License *string `json:"license"`
	IsDev   bool    `json:"isDev"`
}

// ApiRouteItem represents a detected REST or RPC endpoint.
type ApiRouteItem struct {
	Method          string  `json:"method"`
	Path            string  `json:"path"`
	SourceFile      string  `json:"sourceFile"`
	LineNumber      *int    `json:"lineNumber"`
	InferredHandler *string `json:"inferredHandler"`
	AuthRequired    bool    `json:"authRequired"`
}

// EnvVarType describes the inferred data type of an environment variable.
type EnvVarType string

const (
	EnvVarString  EnvVarType = "string"
	EnvVarNumber  EnvVarType = "number"
	EnvVarBoolean EnvVarType = "boolean"
	EnvVarURL     EnvVarType = "url"
	EnvVarSecret  EnvVarType = "secret"
)

// EnvironmentVariable represents an environment variable configuration entry.
type EnvironmentVariable struct {
	Name         string     `json:"name"`
	Required     bool       `json:"required"`
	DefaultValue *string    `json:"defaultValue"`
	InferredType EnvVarType `json:"inferredType"`
	Source       string     `json:"source"`
	References   []string   `json:"references"`
}

// LanguageMetric summarizes bytes and file counts per language.
type LanguageMetric struct {
	Name       string  `json:"name"`
	Percentage float64 `json:"percentage"`
	FileCount  int     `json:"fileCount"`
	Bytes      int64   `json:"bytes"`
}

// RepositoryInfo contains source origin metadata.
type RepositoryInfo struct {
	Name          string     `json:"name"`
	SourceType    SourceType `json:"sourceType"`
	URL           *string    `json:"url"`
	DefaultBranch *string    `json:"defaultBranch"`
	CommitHash    *string    `json:"commitHash"`
}

// AnalysisMetrics contains aggregate numerical counts.
type AnalysisMetrics struct {
	TotalFiles    int              `json:"totalFiles"`
	IncludedFiles int              `json:"includedFiles"`
	ExcludedFiles int              `json:"excludedFiles"`
	TotalBytes    int64            `json:"totalBytes"`
	LinesOfCode   int              `json:"linesOfCode"`
	Languages     []LanguageMetric `json:"languages"`
}

// DependenciesBlock groups runtime and development packages.
type DependenciesBlock struct {
	Runtime     []DependencyItem `json:"runtime"`
	Development []DependencyItem `json:"development"`
}

// EnvironmentBlock groups environment variables.
type EnvironmentBlock struct {
	Variables []EnvironmentVariable `json:"variables"`
}

// SetupCommands describes a single setup command.
type SetupCommand struct {
	Label string `json:"label"`
	Cmd   string `json:"cmd"`
}

// SetupInfo provides onboarding and build commands.
type SetupInfo struct {
	Prerequisites   []string       `json:"prerequisites"`
	InstallCommands []SetupCommand `json:"installCommands"`
	RunCommands     []SetupCommand `json:"runCommands"`
	BuildCommands   []SetupCommand `json:"buildCommands"`
}

// TestingInfo summarizes test frameworks and runners.
type TestingInfo struct {
	Frameworks      []string `json:"frameworks"`
	TestCommands    []string `json:"testCommands"`
	TestDirectories []string `json:"testDirectories"`
	TestFileCount   int      `json:"testFileCount"`
}

// DeploymentCIWorkflow describes a single CI/CD pipeline workflow.
type DeploymentCIWorkflow struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Triggers []string `json:"triggers"`
}

// DeploymentInfo summarizes Docker and CI/CD configurations.
type DeploymentInfo struct {
	DockerAvailable bool                   `json:"dockerAvailable"`
	DockerfilePaths []string               `json:"dockerfilePaths"`
	ComposePaths    []string               `json:"composePaths"`
	CIWorkflows     []DeploymentCIWorkflow `json:"ciWorkflows"`
	TargetPlatforms []string               `json:"targetPlatforms"`
}

// Provenance tracks the exact execution and version lineage of an analysis result.
type Provenance struct {
	Engine               string  `json:"engine"`
	EngineVersion        string  `json:"engineVersion"`
	RulesVersion         string  `json:"rulesVersion"`
	SchemaVersion        string  `json:"schemaVersion"`
	DurationMs           int64   `json:"durationMs"`
	RulesEvaluatedCount  int     `json:"rulesEvaluatedCount"`
	ComplexityTier       string  `json:"complexityTier"`
	ComplexityScore      float64 `json:"complexityScore"`
	SourceArtifactSha256 string  `json:"sourceArtifactSha256"`
	ScopeHash            string  `json:"scopeHash"`
}

// CanonicalAnalysisResult is the single source of truth data structure for PDFNest Repository Analyzer.
type CanonicalAnalysisResult struct {
	SchemaVersion string            `json:"schemaVersion"`
	AnalysisID    string            `json:"analysisId"`
	CreatedAt     time.Time         `json:"createdAt"`
	Repository    RepositoryInfo    `json:"repository"`
	Metrics       AnalysisMetrics   `json:"metrics"`
	Technologies  []TechnologyItem  `json:"technologies"`
	Dependencies  DependenciesBlock `json:"dependencies"`
	Routes        []ApiRouteItem    `json:"routes"`
	Environment   EnvironmentBlock  `json:"environment"`
	Setup         SetupInfo         `json:"setup"`
	Testing       TestingInfo       `json:"testing"`
	Deployment    DeploymentInfo    `json:"deployment"`
	StructureTree string            `json:"structureTree"`
	Provenance    Provenance        `json:"provenance"`
}
