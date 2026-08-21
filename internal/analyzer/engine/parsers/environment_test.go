package parsers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

func TestEnvironmentScanner(t *testing.T) {
	tempDir := t.TempDir()

	// Safe .env.example
	envExampleContent := `
# App Config
PORT=3000
DATABASE_URL=postgres://localhost:5432/app
JWT_SECRET=super-secret-must-not-be-leaked
ENABLE_METRICS=true
`
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, ".env.example"), []byte(envExampleContent), 0644))

	// Source file with process.env references
	tsSource := `
const port = process.env.PORT || 3000;
const apiKey = process.env.STRIPE_API_KEY;
`
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "src", "server.ts"), []byte(tsSource), 0644))

	inv := &inventory.ScopeInventory{
		Files: []inventory.FileEntry{
			{RelPath: ".env.example", Category: inventory.CategoryConfig, IsExcluded: false},
			{RelPath: "src/server.ts", Category: inventory.CategorySource, IsExcluded: false, Size: int64(len(tsSource))},
		},
	}

	scanner := NewEnvironmentScanner()
	vars := scanner.ScanEnvironmentVariables(tempDir, inv)
	require.NotEmpty(t, vars)

	varMap := make(map[string]engine.EnvironmentVariable)
	for _, v := range vars {
		varMap[v.Name] = v
	}

	// 1. PORT
	assert.Contains(t, varMap, "PORT")
	assert.Equal(t, engine.EnvVarNumber, varMap["PORT"].InferredType)
	assert.NotNil(t, varMap["PORT"].DefaultValue)
	assert.Equal(t, "3000", *varMap["PORT"].DefaultValue)

	// 2. DATABASE_URL
	assert.Contains(t, varMap, "DATABASE_URL")
	assert.Equal(t, engine.EnvVarURL, varMap["DATABASE_URL"].InferredType)

	// 3. JWT_SECRET - verify SECRET value is masked / not stored
	assert.Contains(t, varMap, "JWT_SECRET")
	assert.Equal(t, engine.EnvVarSecret, varMap["JWT_SECRET"].InferredType)
	assert.Nil(t, varMap["JWT_SECRET"].DefaultValue, "Secret default values must never be stored")

	// 4. STRIPE_API_KEY from source reference
	assert.Contains(t, varMap, "STRIPE_API_KEY")
	assert.Equal(t, engine.EnvVarSecret, varMap["STRIPE_API_KEY"].InferredType)
	assert.Contains(t, varMap["STRIPE_API_KEY"].References, "src/server.ts:3")
}
