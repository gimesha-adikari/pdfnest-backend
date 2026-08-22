package engine_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
	"pdfnest-backend/internal/analyzer/engine/structure"
)

func TestPipelineStructureAndMarkdownRegression(t *testing.T) {
	// 1. Inventory Walk
	inv := &inventory.ScopeInventory{
		LanguagesFound: []string{"Go", "Markdown"},
		Files: []inventory.FileEntry{
			{RelPath: "cmd/api/main.go", Category: inventory.CategorySource, Size: 1500},
			{RelPath: "internal/analyzer/engine.go", Category: inventory.CategorySource, Size: 3500},
			{RelPath: "go.mod", Category: inventory.CategoryConfig, Size: 500},
			{RelPath: "README.md", Category: inventory.CategoryUnknown, Size: 1000},
		},
	}

	treeOptions := structure.StructureDisplayOptions{
		MaxDepth: 5,
		MaxNodes: 100,
	}

	// 2. BuildProjectStructure
	structRoot, treeStr, err := structure.BuildProjectStructure(inv, ".", treeOptions)
	require.NoError(t, err)
	require.NotNil(t, structRoot)

	// 3. CanonicalAnalysisResult Assembly
	res := engine.CanonicalAnalysisResult{
		SchemaVersion: engine.SchemaVersion,
		Structure:     structRoot,
		StructureTree: treeStr,
	}
	
	// Assert invariants
	assert.NotNil(t, res.Structure)
	assert.NotEmpty(t, res.StructureTree)
	assert.Contains(t, res.StructureTree, "cmd")
	assert.Contains(t, res.StructureTree, "main.go")

	// 4. Markdown Export Simulation
	var mdBuilder strings.Builder
	mdBuilder.WriteString("## 1. Project Structure\n\n```text\n")
	mdBuilder.WriteString(res.StructureTree)
	mdBuilder.WriteString("\n```\n")

	mdOutput := mdBuilder.String()
	assert.Contains(t, mdOutput, "## 1. Project Structure")
	assert.Contains(t, mdOutput, "```text")
	assert.Contains(t, mdOutput, "cmd")
	
	// 5. Empty repository fixture correctly produces clean empty structure without failing or panicking
	emptyInv := &inventory.ScopeInventory{
		Files: []inventory.FileEntry{},
	}
	emptyRoot, emptyTree, emptyErr := structure.BuildProjectStructure(emptyInv, ".", treeOptions)
	require.NoError(t, emptyErr)
	require.NotNil(t, emptyRoot)
	
	assert.Equal(t, ".", strings.TrimSpace(emptyTree)) // Empty tree is just "."
}
