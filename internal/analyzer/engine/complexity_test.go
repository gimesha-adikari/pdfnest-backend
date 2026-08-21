package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWorkloadPolicyBoundaries(t *testing.T) {
	limits := DefaultPolicyLimits()

	tests := []struct {
		name         string
		input        ComplexityInput
		expectedTier WorkloadTier
		expectedEng  string
		isSupported  bool
	}{
		{
			name: "Tier 1: 499 files, 4 MB",
			input: ComplexityInput{
				IncludedFiles: 499,
				IncludedBytes: 4 * 1024 * 1024,
			},
			expectedTier: Tier1Instant,
			expectedEng:  EngineNameGoAnalyzerWorker,
			isSupported:  true,
		},
		{
			name: "Tier 2: Exactly 500 files, 4 MB",
			input: ComplexityInput{
				IncludedFiles: 500,
				IncludedBytes: 4 * 1024 * 1024,
			},
			expectedTier: Tier2Standard,
			expectedEng:  EngineNameGoAnalyzerWorker,
			isSupported:  true,
		},
		{
			name: "Tier 2: Exactly 2500 files, 25 MB",
			input: ComplexityInput{
				IncludedFiles: 2500,
				IncludedBytes: 25 * 1024 * 1024,
			},
			expectedTier: Tier2Standard,
			expectedEng:  EngineNameGoAnalyzerWorker,
			isSupported:  true,
		},
		{
			name: "Tier 3: 2501 files, 20 MB",
			input: ComplexityInput{
				IncludedFiles: 2501,
				IncludedBytes: 20 * 1024 * 1024,
			},
			expectedTier: Tier3Heavy,
			expectedEng:  EngineNamePythonWorkerEscalated,
			isSupported:  true,
		},
		{
			name: "Tier 3: 15000 files, 80 MB",
			input: ComplexityInput{
				IncludedFiles: 15000,
				IncludedBytes: 80 * 1024 * 1024,
			},
			expectedTier: Tier3Heavy,
			expectedEng:  EngineNamePythonWorkerEscalated,
			isSupported:  true,
		},
		{
			name: "Tier 4: Deep AST request with 100 files",
			input: ComplexityInput{
				IncludedFiles:  100,
				IncludedBytes:  1 * 1024 * 1024,
				DeepASTRequest: true,
			},
			expectedTier: Tier4DeepAST,
			expectedEng:  EngineNamePythonWorkerEscalated,
			isSupported:  true,
		},
		{
			name: "Tier 5: 25001 files (Exceeds hard ceiling)",
			input: ComplexityInput{
				IncludedFiles: 25001,
				IncludedBytes: 10 * 1024 * 1024,
			},
			expectedTier: Tier5Unsupported,
			expectedEng:  "",
			isSupported:  false,
		},
		{
			name: "Tier 5: 250 MB + 1 byte (Exceeds volume ceiling)",
			input: ComplexityInput{
				IncludedFiles: 1000,
				IncludedBytes: 250*1024*1024 + 1,
			},
			expectedTier: Tier5Unsupported,
			expectedEng:  "",
			isSupported:  false,
		},
		{
			name: "Most restrictive dimension: 200 files (Tier 1) + 30 MB (Tier 3)",
			input: ComplexityInput{
				IncludedFiles: 200,
				IncludedBytes: 30 * 1024 * 1024,
			},
			expectedTier: Tier3Heavy,
			expectedEng:  EngineNamePythonWorkerEscalated,
			isSupported:  true,
		},
		{
			name: "Most restrictive dimension: 5000 files (Tier 3) + 2 MB (Tier 1)",
			input: ComplexityInput{
				IncludedFiles: 5000,
				IncludedBytes: 2 * 1024 * 1024,
			},
			expectedTier: Tier3Heavy,
			expectedEng:  EngineNamePythonWorkerEscalated,
			isSupported:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := ClassifyWorkload(tt.input, limits)
			assert.Equal(t, tt.expectedTier, res.Tier)
			assert.Equal(t, tt.expectedEng, res.TargetEngine)
			assert.Equal(t, tt.isSupported, res.IsSupported)
			assert.GreaterOrEqual(t, res.ComplexityScore, 0.0)
		})
	}
}
