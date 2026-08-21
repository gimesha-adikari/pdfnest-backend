package engine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestComputeScopeHashDeterminism(t *testing.T) {
	input1 := ScopeHashInput{
		CustomExclusions: []string{"node_modules/**", "dist/**", "build/**"},
		EnabledPresets:   []string{"preset-node-modules", "preset-build-dist"},
		ForceIncludes:    []string{"!dist/bundle.js"},
		SelectedDomains:  []string{"Project Structure", "Technology Stack"},
	}

	// input2 has identical elements in reversed order with duplicate values and extra whitespace
	input2 := ScopeHashInput{
		CustomExclusions: []string{"dist/**", "build/**", "node_modules/**", " dist/** "},
		EnabledPresets:   []string{"preset-build-dist", "preset-node-modules"},
		ForceIncludes:    []string{"!dist/bundle.js"},
		SelectedDomains:  []string{"Technology Stack", "Project Structure"},
	}

	hash1 := ComputeScopeHash(input1)
	hash2 := ComputeScopeHash(input2)

	assert.NotEmpty(t, hash1)
	assert.Equal(t, hash1, hash2, "Canonical scope hashes must be identical regardless of item ordering or duplicates")

	// input3 has modified exclusion pattern
	input3 := ScopeHashInput{
		CustomExclusions: []string{"node_modules/**", "different/**"},
		EnabledPresets:   []string{"preset-node-modules"},
	}
	hash3 := ComputeScopeHash(input3)
	assert.NotEqual(t, hash1, hash3, "Different scope inputs must produce different hashes")
}

func TestHashFileSHA256(t *testing.T) {
	tempFile := filepath.Join(t.TempDir(), "artifact.zip")
	content := []byte("PK\x03\x04sample-archive-bytes")
	require.NoError(t, os.WriteFile(tempFile, content, 0644))

	fileHash, err := HashFileSHA256(tempFile)
	require.NoError(t, err)

	bytesHash := HashBytesSHA256(content)
	assert.Equal(t, bytesHash, fileHash, "File hash must match byte hash of identical content")
}
