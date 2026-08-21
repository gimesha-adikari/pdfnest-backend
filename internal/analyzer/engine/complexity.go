package engine

// WorkloadTier represents the deterministic admission tier for a repository workload.
type WorkloadTier string

const (
	Tier1Instant     WorkloadTier = "Tier 1 (Instant)"
	Tier2Standard    WorkloadTier = "Tier 2 (Standard)"
	Tier3Heavy       WorkloadTier = "Tier 3 (Heavy)"
	Tier4DeepAST     WorkloadTier = "Tier 4 (Deep AST)"
	Tier5Unsupported WorkloadTier = "Tier 5 (Unsupported)"
)

// WorkloadPolicyLimits defines the configurable boundaries for admission control.
type WorkloadPolicyLimits struct {
	Tier1MaxFiles int
	Tier1MaxBytes int64

	Tier2MaxFiles int
	Tier2MaxBytes int64

	Tier3MaxFiles int
	Tier3MaxBytes int64

	HardCeilingMaxFiles int
	HardCeilingMaxBytes int64
}

// DefaultPolicyLimits returns the standard production admission boundaries.
func DefaultPolicyLimits() WorkloadPolicyLimits {
	return WorkloadPolicyLimits{
		Tier1MaxFiles: 500,             // < 500 files
		Tier1MaxBytes: 5 * 1024 * 1024, // <= 5 MB

		Tier2MaxFiles: 2500,             // <= 2500 files
		Tier2MaxBytes: 25 * 1024 * 1024, // <= 25 MB

		Tier3MaxFiles: 15000,             // <= 15000 files
		Tier3MaxBytes: 100 * 1024 * 1024, // <= 100 MB

		HardCeilingMaxFiles: 25000,             // > 25000 files rejected
		HardCeilingMaxBytes: 250 * 1024 * 1024, // > 250 MB rejected
	}
}

// ComplexityInput encapsulates metrics required for workload classification.
type ComplexityInput struct {
	TotalFiles     int
	IncludedFiles  int
	TotalBytes     int64
	IncludedBytes  int64
	LargestFile    int64
	ManifestCount  int
	LanguageCount  int
	MaxDepth       int
	DeepASTRequest bool
}

// PolicyClassification contains the resolved admission decision.
type PolicyClassification struct {
	Tier            WorkloadTier
	TargetEngine    string
	IsSupported     bool
	ComplexityScore float64
	Reasons         []string
}

// ClassifyWorkload deterministically assigns a workload tier based on the authoritative policy.
// It enforces the "most restrictive applicable dimension" invariant: if a repository's file count
// qualifies for Tier 1 but its byte size qualifies for Tier 3, Tier 3 is selected.
// Any breach of the hard ceiling instantly triggers Tier 5 (Unsupported / Rejected).
func ClassifyWorkload(input ComplexityInput, limits WorkloadPolicyLimits) PolicyClassification {
	score := CalculateComplexityScore(input)
	reasons := make([]string, 0, 4)

	// Hard rejection check (Tier 5)
	if input.TotalFiles > limits.HardCeilingMaxFiles || input.IncludedFiles > limits.HardCeilingMaxFiles {
		reasons = append(reasons, "File count exceeds hard ceiling limit of 25,000 files")
		return PolicyClassification{
			Tier:            Tier5Unsupported,
			TargetEngine:    "",
			IsSupported:     false,
			ComplexityScore: score,
			Reasons:         reasons,
		}
	}
	if input.TotalBytes > limits.HardCeilingMaxBytes || input.IncludedBytes > limits.HardCeilingMaxBytes {
		reasons = append(reasons, "Source volume exceeds hard ceiling limit of 250 MB")
		return PolicyClassification{
			Tier:            Tier5Unsupported,
			TargetEngine:    "",
			IsSupported:     false,
			ComplexityScore: score,
			Reasons:         reasons,
		}
	}

	// Deep AST request override (Tier 4)
	if input.DeepASTRequest {
		reasons = append(reasons, "Deep semantic AST cross-file callgraphing requested")
		return PolicyClassification{
			Tier:            Tier4DeepAST,
			TargetEngine:    EngineNamePythonWorkerEscalated,
			IsSupported:     true,
			ComplexityScore: score,
			Reasons:         reasons,
		}
	}

	// Evaluate dimension by dimension
	fileCount := input.IncludedFiles
	byteSize := input.IncludedBytes
	if fileCount == 0 && input.TotalFiles > 0 {
		fileCount = input.TotalFiles
	}
	if byteSize == 0 && input.TotalBytes > 0 {
		byteSize = input.TotalBytes
	}

	// Determine file tier
	fileTier := Tier1Instant
	if fileCount >= limits.Tier1MaxFiles && fileCount <= limits.Tier2MaxFiles {
		fileTier = Tier2Standard
	} else if fileCount > limits.Tier2MaxFiles {
		fileTier = Tier3Heavy
	}

	// Determine byte size tier
	byteTier := Tier1Instant
	if byteSize > limits.Tier1MaxBytes && byteSize <= limits.Tier2MaxBytes {
		byteTier = Tier2Standard
	} else if byteSize > limits.Tier2MaxBytes {
		byteTier = Tier3Heavy
	}

	// Select the most restrictive tier
	resolvedTier := fileTier
	if tierSeverity(byteTier) > tierSeverity(resolvedTier) {
		resolvedTier = byteTier
	}

	var targetEngine string
	switch resolvedTier {
	case Tier1Instant, Tier2Standard:
		targetEngine = EngineNameGoAnalyzerWorker
		reasons = append(reasons, "Deterministic static analysis qualified for Go fast-path execution")
	case Tier3Heavy:
		targetEngine = EngineNamePythonWorkerEscalated
		reasons = append(reasons, "Heavy repository volume escalated to Python Dramatiq worker")
	}

	return PolicyClassification{
		Tier:            resolvedTier,
		TargetEngine:    targetEngine,
		IsSupported:     true,
		ComplexityScore: score,
		Reasons:         reasons,
	}
}

func tierSeverity(tier WorkloadTier) int {
	switch tier {
	case Tier1Instant:
		return 1
	case Tier2Standard:
		return 2
	case Tier3Heavy:
		return 3
	case Tier4DeepAST:
		return 4
	case Tier5Unsupported:
		return 5
	default:
		return 0
	}
}

// CalculateComplexityScore computes an advisory numerical score for provenance, logging, and metrics.
// It NEVER overrides the authoritative WorkloadTier admission decision.
func CalculateComplexityScore(input ComplexityInput) float64 {
	var astWeight float64
	if input.DeepASTRequest {
		astWeight = 250.0
	}

	score := (float64(input.IncludedFiles) * 1.0) +
		(float64(input.IncludedBytes) / 50000.0) +
		(float64(input.ManifestCount) * 15.0) +
		(float64(input.LanguageCount) * 20.0) +
		(float64(input.MaxDepth) * 5.0) +
		astWeight

	return score
}
