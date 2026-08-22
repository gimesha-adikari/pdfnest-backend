package worker

import (
	"context"
	"fmt"
	"sort"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/acquisition"
	"pdfnest-backend/internal/analyzer/engine/ai"
	"pdfnest-backend/internal/analyzer/engine/exclusion"
	"pdfnest-backend/internal/analyzer/engine/graph"
	"pdfnest-backend/internal/analyzer/engine/intelligence"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/parsers"
	"pdfnest-backend/internal/analyzer/engine/structure"
)

// ValidateJob validates the integrity and supported boundaries of an incoming analyzer job.
func ValidateJob(job *AnalyzerJob) error {
	if job == nil {
		return fmt.Errorf("%w: nil job payload", ErrInvalidJob)
	}
	if job.TaskID == "" {
		return fmt.Errorf("%w: missing taskId", ErrInvalidJob)
	}
	if job.SessionID == "" {
		return fmt.Errorf("%w: missing sessionId", ErrInvalidJob)
	}

	switch job.SourceType {
	case engine.SourceTypeGit:
		if job.GitURL == "" {
			return fmt.Errorf("%w: gitUrl required for source type git", ErrInvalidJob)
		}
		if err := acquisition.ValidateGitURL(job.GitURL); err != nil {
			return fmt.Errorf("%w: %v", ErrInvalidJob, err)
		}
	case engine.SourceTypeZip:
		if job.StagedArchivePath == "" {
			return fmt.Errorf("%w: stagedArchivePath required for source type zip", ErrInvalidJob)
		}
	case engine.SourceTypeLocalFolder:
		// Allowed in testing and local sandbox environments
	default:
		return fmt.Errorf("%w: unsupported sourceType '%s'", ErrInvalidJob, job.SourceType)
	}

	// Reject unsupported future phase requests in Phase 4B
	if job.DeepAst {
		return fmt.Errorf("%w: deep AST semantic analysis requires Phase 7 engine", ErrUnsupportedOperation)
	}

	return nil
}

// ProgressFunc reports step transitions and completion percentages.
type ProgressFunc func(status TaskStatus, percent int, message string)

// ExecutePipeline orchestrates the deterministic 10-step analysis pipeline inside a dedicated sandbox.
func ExecutePipeline(
	ctx context.Context,
	job *AnalyzerJob,
	sandboxBaseDir string,
	onProgress ProgressFunc,
) (*engine.CanonicalAnalysisResult, error) {
	if err := ValidateJob(job); err != nil {
		return nil, err
	}

	startTime := time.Now().UTC()

	// 1. Establish Ephemeral Sandbox
	sandbox, err := acquisition.NewSandbox(sandboxBaseDir, job.SessionID)
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	defer sandbox.Cleanup()

	// 2. Acquire Source Repository
	if onProgress != nil {
		onProgress(StatusAcquiring, 15, "Acquiring repository source workspace")
	}

	var acqResult *acquisition.AcquisitionResult
	acqLimits := acquisition.DefaultAcquisitionLimits()

	switch job.SourceType {
	case engine.SourceTypeGit:
		res, cloneErr := acquisition.CloneGitRepository(ctx, job.GitURL, sandbox, acqLimits)
		if cloneErr != nil {
			return nil, fmt.Errorf("git acquisition failed: %w", cloneErr)
		}
		acqResult = res

	case engine.SourceTypeZip:
		res, zipErr := acquisition.ExtractZipArchive(ctx, job.StagedArchivePath, sandbox, acqLimits)
		if zipErr != nil {
			return nil, fmt.Errorf("zip acquisition failed: %w", zipErr)
		}
		acqResult = res

	case engine.SourceTypeLocalFolder:
		acqResult = &acquisition.AcquisitionResult{
			SandboxPath:    sandbox.RootPath,
			SourceType:     engine.SourceTypeLocalFolder,
			RepositoryName: "local-repository",
		}
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	// 3. Scan Repository Inventory & Apply 5-Tier Precedence
	if onProgress != nil {
		onProgress(StatusInventory, 40, "Scanning files and evaluating exclusion rules")
	}

	exEngine := exclusion.NewEngine(job.Scope.ToExclusionConfig())
	scopeHashInput := engine.ScopeHashInput{
		CustomExclusions: job.Scope.CustomPatterns,
		EnabledPresets:   job.Scope.EnabledPresets,
		ForceIncludes:    job.Scope.ForceIncludes,
		SelectedDomains:  job.SelectedDomains,
	}
	scopeHash := engine.ComputeScopeHash(scopeHashInput)

	opts := inventory.DefaultScannerOptions(exEngine)
	opts.ArtifactSha256 = acqResult.ArchiveSha256
	opts.ScopeHash = scopeHash

	inv, err := inventory.ScanRepository(ctx, sandbox.RootPath, opts)
	if err != nil {
		return nil, fmt.Errorf("inventory scanner failed: %w", err)
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}

	repoNameStr := acqResult.RepositoryName
	if repoNameStr == "" {
		repoNameStr = "repository"
	}

	projStructure, asciiTree, structErr := structure.BuildProjectStructure(inv, repoNameStr, structure.DefaultDisplayOptions())
	if structErr != nil && inv.IncludedFiles > 0 {
		return nil, fmt.Errorf("structure builder failed on non-empty inventory: %w", structErr)
	}

	// 4. Deterministic Static Manifest & Evidence Analysis
	if onProgress != nil {
		onProgress(StatusAnalyzing, 70, "Parsing manifests and extracting technology evidence")
	}

	facts, err := parsers.AnalyzeRepositoryFacts(ctx, sandbox.RootPath, inv)
	if err != nil {
		return nil, fmt.Errorf("analyzer facts extraction failed: %w", err)
	}

	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Build Semantic Graph
	relGraph, graphMetrics, err := graph.BuildRelationshipGraph(ctx, inv, nil, facts)
	if err != nil {
		return nil, fmt.Errorf("relationship graph construction failed: %w", err)
	}

	// 5. Construct Canonical Result
	if onProgress != nil {
		onProgress(StatusFinalizing, 90, "Assembling canonical analysis result")
	}

	repoName := acqResult.RepositoryName
	if repoName == "" {
		repoName = "repository"
	}

	res := engine.NewEmptyCanonicalResult(job.SessionID, repoName, job.SourceType)
	res.CreatedAt = startTime

	if acqResult.CommitHash != "" {
		res.Repository.CommitHash = &acqResult.CommitHash
	}
	if acqResult.DefaultBranch != "" {
		res.Repository.DefaultBranch = &acqResult.DefaultBranch
	}

	// Metrics
	res.Metrics.TotalFiles = inv.TotalFiles
	res.Metrics.IncludedFiles = inv.IncludedFiles
	res.Metrics.ExcludedFiles = inv.ExcludedFiles
	res.Metrics.TotalBytes = inv.TotalBytes

	langStats := make(map[string]*engine.LanguageMetric)
	var totalLangBytes int64
	for _, f := range inv.Files {
		if f.IsExcluded || f.Language == "" {
			continue
		}
		if entry, ok := langStats[f.Language]; ok {
			entry.FileCount++
			entry.Bytes += f.Size
		} else {
			langStats[f.Language] = &engine.LanguageMetric{
				Name:      f.Language,
				FileCount: 1,
				Bytes:     f.Size,
			}
		}
		totalLangBytes += f.Size
	}

	res.Metrics.Languages = make([]engine.LanguageMetric, 0, len(langStats))
	for _, lm := range langStats {
		if totalLangBytes > 0 {
			lm.Percentage = float64(lm.Bytes) / float64(totalLangBytes) * 100.0
		}
		res.Metrics.Languages = append(res.Metrics.Languages, *lm)
	}
	sort.Slice(res.Metrics.Languages, func(i, j int) bool {
		return res.Metrics.Languages[i].Bytes > res.Metrics.Languages[j].Bytes
	})

	// Intelligence Blocks
	res.Technologies = facts.Technologies
	res.Dependencies.Runtime = facts.RuntimeDeps
	res.Dependencies.Development = facts.DevDeps
	res.Routes = facts.Routes
	res.Environment.Variables = facts.Environment
	res.Setup = facts.Setup
	res.Testing = facts.Testing
	res.Deployment = facts.Deployment
	res.Structure = projStructure
	res.StructureTree = asciiTree
	res.Graph = relGraph.Serialize()
	res.GraphMetrics = graphMetrics

	if relGraph != nil {
		intelRes, _ := intelligence.RunIntelligencePipeline(relGraph)
		res.Intelligence = intelRes
	}

	// Provenance & Complexity Policy
	durationMs := time.Since(startTime).Milliseconds()
	complexityInput := engine.ComplexityInput{
		TotalFiles:     inv.TotalFiles,
		IncludedFiles:  inv.IncludedFiles,
		TotalBytes:     inv.TotalBytes,
		IncludedBytes:  inv.IncludedBytes,
		ManifestCount:  len(inv.ManifestsFound),
		LanguageCount:  len(inv.LanguagesFound),
		MaxDepth:       inv.MaximumDepth,
		DeepASTRequest: false,
	}
	classification := engine.ClassifyWorkload(complexityInput, engine.DefaultPolicyLimits())

	res.Provenance = engine.Provenance{
		Engine:               engine.EngineNameGoAnalyzerWorker,
		EngineVersion:        "1.0.0",
		RulesVersion:         "1.0.0",
		SchemaVersion:        engine.SchemaVersion,
		DurationMs:           durationMs,
		ComplexityScore:      classification.ComplexityScore,
		ComplexityTier:       string(classification.Tier),
		RulesEvaluatedCount:  len(parsers.RuleCatalog()),
		ScopeHash:            scopeHash,
		SourceArtifactSha256: acqResult.ArchiveSha256,
	}

	// 6. Validate Canonical Schema
	if err := engine.ValidateCanonicalResult(res); err != nil {
		return nil, fmt.Errorf("canonical validation failed: %w", err)
	}

	// 7. Optional Phase 7C AI Architecture Synthesis (Non-fatal companion enrichment)
	if job.EnableAi {
		aiCfg := ai.LoadConfigFromEnv()
		if aiCfg.Enabled {
			if onProgress != nil {
				onProgress(StatusFinalizing, 95, "Synthesizing AI architecture summary")
			}
			_, _, _ = ai.SynthesizeArchitectureSummary(ctx, aiCfg, nil, res, job.TaskID, job.SessionID, true)
		}
	}

	if onProgress != nil {
		onProgress(StatusCompleted, 100, "Repository analysis completed successfully")
	}

	return res, nil
}
