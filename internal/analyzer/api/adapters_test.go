package api

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/analyzer/worker"
)

func TestScopeConfigAdapter_ValidationAndSanitization(t *testing.T) {
	adapter := NewScopeConfigAdapter()

	req := UpdateScopeRequest{
		CustomPatterns:  []string{"  *.tmp  ", "build/**", ""},
		EnabledPresets:  []string{"node_modules", "vendor"},
		ForceIncludes:   []string{"  src/secret.txt  "},
		GitignoreRules:  []string{".cache/"},
		SelectedDomains: []string{"Technology Stack", "Dependencies"},
	}

	scopeConfig, scopeHash, err := adapter.AdaptAndValidate(req)
	require.NoError(t, err)
	assert.NotEmpty(t, scopeHash)

	// Verify trimming and exclusion of empty patterns
	assert.Equal(t, []string{"*.tmp", "build/**"}, scopeConfig.CustomPatterns)
	assert.Equal(t, []string{"node_modules", "vendor"}, scopeConfig.EnabledPresets)
	assert.Equal(t, []string{"src/secret.txt"}, scopeConfig.ForceIncludes)
	assert.Equal(t, []string{".cache/"}, scopeConfig.GitignoreRules)
}

func TestScopeConfigAdapter_BoundariesAndRejections(t *testing.T) {
	adapter := NewScopeConfigAdapter()

	t.Run("Exceeds max custom patterns limit", func(t *testing.T) {
		patterns := make([]string, 101)
		for i := range patterns {
			patterns[i] = "*.ext"
		}
		_, _, err := adapter.AdaptAndValidate(UpdateScopeRequest{CustomPatterns: patterns})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "exceed maximum limit")
	})

	t.Run("Exceeds max pattern length", func(t *testing.T) {
		longPattern := strings.Repeat("a", 257)
		_, _, err := adapter.AdaptAndValidate(UpdateScopeRequest{CustomPatterns: []string{longPattern}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "exceeds maximum length")
	})

	t.Run("Rejects null bytes in pattern", func(t *testing.T) {
		nullPattern := "evil\x00pattern"
		_, _, err := adapter.AdaptAndValidate(UpdateScopeRequest{CustomPatterns: []string{nullPattern}})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "null bytes")
	})
}

func TestTaskProgressAdapter(t *testing.T) {
	adapter := NewTaskProgressAdapter()

	p := worker.TaskProgress{
		TaskID:          "task-123",
		SessionID:       "session-456",
		Status:          worker.StatusAnalyzing,
		ProgressPercent: 60,
		StageMessage:    "Analyzing repository facts",
	}

	resp := adapter.Adapt(p)
	assert.Equal(t, "task-123", resp.TaskID)
	assert.Equal(t, "session-456", resp.SessionID)
	assert.Equal(t, worker.StatusAnalyzing, resp.Status)
	assert.Equal(t, 60, resp.ProgressPercent)
	assert.Equal(t, "Analyzing repository facts", resp.StageMessage)
}
