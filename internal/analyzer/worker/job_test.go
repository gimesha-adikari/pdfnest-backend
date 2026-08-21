package worker

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"pdfnest-backend/internal/analyzer/engine"
)

func TestValidateJob(t *testing.T) {
	tests := []struct {
		name        string
		job         *AnalyzerJob
		expectError bool
		expectedErr error
	}{
		{
			name: "Valid Git Job",
			job: &AnalyzerJob{
				JobVersion: "1.0.0",
				TaskID:     "task-git-1",
				SessionID:  "session-git-1",
				SourceType: engine.SourceTypeGit,
				GitURL:     "https://github.com/facebook/react.git",
			},
			expectError: false,
		},
		{
			name: "Valid ZIP Job",
			job: &AnalyzerJob{
				JobVersion:        "1.0.0",
				TaskID:            "task-zip-1",
				SessionID:         "session-zip-1",
				SourceType:        engine.SourceTypeZip,
				StagedArchivePath: "/tmp/uploads/repo.zip",
			},
			expectError: false,
		},
		{
			name:        "Nil Job Payload",
			job:         nil,
			expectError: true,
			expectedErr: ErrInvalidJob,
		},
		{
			name: "Missing Task ID",
			job: &AnalyzerJob{
				SessionID:  "session-1",
				SourceType: engine.SourceTypeGit,
				GitURL:     "https://github.com/facebook/react.git",
			},
			expectError: true,
			expectedErr: ErrInvalidJob,
		},
		{
			name: "Missing Session ID",
			job: &AnalyzerJob{
				TaskID:     "task-1",
				SourceType: engine.SourceTypeGit,
				GitURL:     "https://github.com/facebook/react.git",
			},
			expectError: true,
			expectedErr: ErrInvalidJob,
		},
		{
			name: "Unsupported Source Type",
			job: &AnalyzerJob{
				TaskID:     "task-1",
				SessionID:  "session-1",
				SourceType: "s3_direct",
			},
			expectError: true,
			expectedErr: ErrInvalidJob,
		},
		{
			name: "Reject Unsupported Deep AST Request in Phase 4B",
			job: &AnalyzerJob{
				TaskID:     "task-ast-1",
				SessionID:  "session-ast-1",
				SourceType: engine.SourceTypeGit,
				GitURL:     "https://github.com/facebook/react.git",
				DeepAst:    true,
			},
			expectError: true,
			expectedErr: ErrUnsupportedOperation,
		},
		{
			name: "Support AI Request in Phase 7C",
			job: &AnalyzerJob{
				TaskID:     "task-ai-1",
				SessionID:  "session-ai-1",
				SourceType: engine.SourceTypeGit,
				GitURL:     "https://github.com/facebook/react.git",
				EnableAi:   true,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateJob(tt.job)
			if tt.expectError {
				assert.Error(t, err)
				if tt.expectedErr != nil {
					assert.ErrorIs(t, err, tt.expectedErr)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
