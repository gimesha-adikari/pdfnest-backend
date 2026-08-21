package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine/exclusion"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

func createTestFixture(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	for relPath, content := range files {
		absPath := filepath.Join(dir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0755))
		require.NoError(t, os.WriteFile(absPath, []byte(content), 0644))
	}
	return dir
}

func TestFixtureRedisOnlyInventory(t *testing.T) {
	fixtureDir := createTestFixture(t, map[string]string{
		"package.json":      `{"name":"redis-api","dependencies":{"ioredis":"^5.3.2"}}`,
		".env.example":      "REDIS_URL=redis://localhost:6379",
		".env":              "REDIS_URL=redis://secret:pass@prod:6379",
		"src/index.ts":      "import Redis from 'ioredis';",
		"src/redis.ts":      "export const redis = new Redis();",
		"__tests__/api.ts":  "describe('api', () => {});",
		"README.md":         "# Redis API",
		"node_modules/a.js": "module.exports = {}",
	})

	scopeCfg := exclusion.Config{
		EnabledPresets: []string{"preset-node-modules"},
	}
	exEngine := exclusion.NewEngine(scopeCfg)
	ctx := context.Background()

	inv, err := inventory.ScanRepository(ctx, fixtureDir, inventory.ScannerOptions{
		ExclusionEngine: exEngine,
	})
	require.NoError(t, err)

	assert.Contains(t, inv.ManifestsFound, "package.json")
	assert.Contains(t, inv.LanguagesFound, "TypeScript")

	// Verify .env is strictly excluded
	var envFound bool
	for _, f := range inv.Files {
		if f.RelPath == ".env" {
			envFound = true
			assert.True(t, f.IsExcluded)
			assert.Equal(t, exclusion.PrecedenceMandatorySecurity, f.Exclusion.Precedence)
		}
	}
	assert.True(t, envFound)

	// Verify workload classification
	limits := DefaultPolicyLimits()
	class := ClassifyWorkload(ComplexityInput{
		TotalFiles:    inv.TotalFiles,
		IncludedFiles: inv.IncludedFiles,
		TotalBytes:    inv.TotalBytes,
		IncludedBytes: inv.IncludedBytes,
	}, limits)

	assert.Equal(t, Tier1Instant, class.Tier)
	assert.Equal(t, EngineNameGoAnalyzerWorker, class.TargetEngine)
}

func TestFixtureSecretMaskingSecurity(t *testing.T) {
	fixtureDir := createTestFixture(t, map[string]string{
		".env":             "SECRET_KEY=12345",
		".env.local":       "LOCAL_SECRET=abc",
		".env.production":  "PROD_SECRET=xyz",
		"certs/server.key": "PRIVATE KEY",
		"certs/ca.pem":     "CERTIFICATE",
		".ssh/id_rsa":      "OPENSSH PRIVATE KEY",
		".ssh/id_rsa.pub":  "ssh-rsa AAAA...",
		"credentials.json": `{"type":"service_account"}`,
		".env.example":     "PUBLIC_VAR=safe",
		"src/index.ts":     "console.log('safe');",
	})

	// Try to force-include secret files to test security invariant
	scopeCfg := exclusion.Config{
		ForceIncludes: []string{
			"!.env",
			"!.env.local",
			"!.env.production",
			"!certs/server.key",
			"!certs/ca.pem",
			"!.ssh/id_rsa",
			"!credentials.json",
		},
	}
	exEngine := exclusion.NewEngine(scopeCfg)
	ctx := context.Background()

	inv, err := inventory.ScanRepository(ctx, fixtureDir, inventory.ScannerOptions{
		ExclusionEngine: exEngine,
	})
	require.NoError(t, err)

	for _, f := range inv.Files {
		if f.RelPath == ".env.example" || f.RelPath == "src/index.ts" {
			assert.False(t, f.IsExcluded, "Safe file %s must be included", f.RelPath)
		} else {
			assert.True(t, f.IsExcluded, "Secret file %s MUST be excluded despite force-include", f.RelPath)
			assert.Equal(t, exclusion.PrecedenceMandatorySecurity, f.Exclusion.Precedence)
			assert.False(t, f.Exclusion.Overridable)
		}
	}
}

func TestFixtureFastApiNoDocker(t *testing.T) {
	fixtureDir := createTestFixture(t, map[string]string{
		"requirements.txt":  "fastapi==0.110.0\nuvicorn==0.28.0",
		"main.py":           "from fastapi import FastAPI\napp = FastAPI()",
		"tests/test_api.py": "def test_root(): pass",
		"README.md":         "# FastAPI Project",
	})

	ctx := context.Background()
	inv, err := inventory.ScanRepository(ctx, fixtureDir, inventory.DefaultScannerOptions(nil))
	require.NoError(t, err)

	assert.Contains(t, inv.ManifestsFound, "requirements.txt")
	assert.Contains(t, inv.LanguagesFound, "Python")

	// Classify workload
	class := ClassifyWorkload(ComplexityInput{
		TotalFiles:    inv.TotalFiles,
		IncludedFiles: inv.IncludedFiles,
		TotalBytes:    inv.TotalBytes,
		IncludedBytes: inv.IncludedBytes,
	}, DefaultPolicyLimits())

	assert.Equal(t, Tier1Instant, class.Tier)
	assert.Equal(t, EngineNameGoAnalyzerWorker, class.TargetEngine)
}

func TestFixtureGoFiberNoGin(t *testing.T) {
	fixtureDir := createTestFixture(t, map[string]string{
		"go.mod":       "module testservice\n\ngo 1.23\n\nrequire github.com/gofiber/fiber/v2 v2.52.0",
		"go.sum":       "github.com/gofiber/fiber/v2 v2.52.0 h1:...",
		"main.go":      "package main\nimport \"github.com/gofiber/fiber/v2\"\nfunc main() {}",
		"main_test.go": "package main\nimport \"testing\"\nfunc TestMain(t *testing.T) {}",
	})

	ctx := context.Background()
	inv, err := inventory.ScanRepository(ctx, fixtureDir, inventory.DefaultScannerOptions(nil))
	require.NoError(t, err)

	assert.Contains(t, inv.ManifestsFound, "go.mod")
	assert.Contains(t, inv.LanguagesFound, "Go")
}
