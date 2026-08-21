package parsers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

func createTestRepository(t *testing.T, files map[string]string) string {
	dir := t.TempDir()
	for relPath, content := range files {
		absPath := filepath.Join(dir, relPath)
		require.NoError(t, os.MkdirAll(filepath.Dir(absPath), 0755))
		require.NoError(t, os.WriteFile(absPath, []byte(content), 0644))
	}
	return dir
}

func TestFixtureRedisOnlyAnalysis(t *testing.T) {
	repoDir := createTestRepository(t, map[string]string{
		"package.json": `{
			"name": "redis-queue-worker",
			"dependencies": {
				"ioredis": "^5.3.2"
			}
		}`,
		".env.example": "REDIS_URL=redis://localhost:6379\nPORT=8080",
		"src/index.ts": `
import Redis from 'ioredis';
const redis = new Redis(process.env.REDIS_URL);
app.get('/health', (req, res) => res.send('ok'));
`,
	})

	ctx := context.Background()
	inv, err := inventory.ScanRepository(ctx, repoDir, inventory.DefaultScannerOptions(nil))
	require.NoError(t, err)

	facts, err := AnalyzeRepositoryFacts(ctx, repoDir, inv)
	require.NoError(t, err)
	require.NotNil(t, facts)

	// Verify Redis Confirmed
	techMap := make(map[string]engine.TechnologyItem)
	for _, tech := range facts.Technologies {
		techMap[tech.Name] = tech
	}
	assert.Contains(t, techMap, "Redis")
	assert.Equal(t, engine.ConfidenceConfirmed, techMap["Redis"].Confidence)

	// Negative Assertions: PostgreSQL, MySQL, SQLite, MongoDB MUST be absent
	assert.NotContains(t, techMap, "PostgreSQL")
	assert.NotContains(t, techMap, "MySQL")
	assert.NotContains(t, techMap, "SQLite")
	assert.NotContains(t, techMap, "Gin")
	assert.Contains(t, techMap["Redis"].NegativeAssertionsPassed, "Kafka")

	// Environment vars
	assert.Equal(t, 2, len(facts.Environment))
}

func TestFixturePostgresPrismaAnalysis(t *testing.T) {
	repoDir := createTestRepository(t, map[string]string{
		"package.json": `{
			"name": "user-service",
			"dependencies": {
				"@prisma/client": "^5.12.0",
				"pg": "^8.11.0"
			}
		}`,
		"prisma/schema.prisma": `
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}
`,
		".env.example": "DATABASE_URL=postgres://user:pass@localhost:5432/db",
		"src/db.ts":    "import { PrismaClient } from '@prisma/client';",
	})

	ctx := context.Background()
	inv, err := inventory.ScanRepository(ctx, repoDir, inventory.DefaultScannerOptions(nil))
	require.NoError(t, err)

	facts, err := AnalyzeRepositoryFacts(ctx, repoDir, inv)
	require.NoError(t, err)

	techMap := make(map[string]engine.TechnologyItem)
	for _, tech := range facts.Technologies {
		techMap[tech.Name] = tech
	}

	assert.Contains(t, techMap, "PostgreSQL")
	assert.Contains(t, techMap, "Prisma")

	// Negative Assertions: MongoDB & Redis MUST be absent
	assert.NotContains(t, techMap, "MongoDB")
	assert.NotContains(t, techMap, "Redis")
	assert.Contains(t, techMap["PostgreSQL"].NegativeAssertionsPassed, "MongoDB")
}

func TestFixtureFastApiNoDockerAnalysis(t *testing.T) {
	repoDir := createTestRepository(t, map[string]string{
		"requirements.txt": "fastapi==0.110.0\nuvicorn==0.28.0",
		"app/main.py": `
from fastapi import FastAPI
app = FastAPI()

@app.get("/api/v1/ping")
async def ping():
    return {"status": "ok"}
`,
	})

	ctx := context.Background()
	inv, err := inventory.ScanRepository(ctx, repoDir, inventory.DefaultScannerOptions(nil))
	require.NoError(t, err)

	facts, err := AnalyzeRepositoryFacts(ctx, repoDir, inv)
	require.NoError(t, err)

	techMap := make(map[string]engine.TechnologyItem)
	for _, tech := range facts.Technologies {
		techMap[tech.Name] = tech
	}

	assert.Contains(t, techMap, "FastAPI")
	assert.Contains(t, techMap, "Python")

	// Docker & Kubernetes absent
	assert.NotContains(t, techMap, "Docker")
	assert.NotContains(t, techMap, "Kubernetes")
	assert.False(t, facts.Deployment.DockerAvailable)

	// Routes
	assert.Equal(t, 1, len(facts.Routes))
	assert.Equal(t, "GET", facts.Routes[0].Method)
	assert.Equal(t, "/api/v1/ping", facts.Routes[0].Path)
}

func TestFixtureReactViteNoNextAnalysis(t *testing.T) {
	repoDir := createTestRepository(t, map[string]string{
		"package.json": `{
			"name": "vite-spa",
			"dependencies": {
				"react": "^18.3.0",
				"react-dom": "^18.3.0"
			},
			"devDependencies": {
				"vite": "^5.2.0",
				"@vitejs/plugin-react": "^4.2.0"
			}
		}`,
		"vite.config.ts": "import { defineConfig } from 'vite';",
		"src/App.tsx":    "export const App = () => <h1>Hello</h1>;",
	})

	ctx := context.Background()
	inv, err := inventory.ScanRepository(ctx, repoDir, inventory.DefaultScannerOptions(nil))
	require.NoError(t, err)

	facts, err := AnalyzeRepositoryFacts(ctx, repoDir, inv)
	require.NoError(t, err)

	techMap := make(map[string]engine.TechnologyItem)
	for _, tech := range facts.Technologies {
		techMap[tech.Name] = tech
	}

	assert.Contains(t, techMap, "React")
	assert.Contains(t, techMap, "Vite")

	// Next.js & Express absent
	assert.NotContains(t, techMap, "Next.js")
	assert.NotContains(t, techMap, "Express")
	assert.Contains(t, techMap["React"].NegativeAssertionsPassed, "Vue")
}

func TestFixtureGoFiberNoGinAnalysis(t *testing.T) {
	repoDir := createTestRepository(t, map[string]string{
		"go.mod": `module api-service

go 1.23

require (
	github.com/gofiber/fiber/v2 v2.52.0
	gorm.io/gorm v1.25.7
)
`,
		"main.go": `
package main
import "github.com/gofiber/fiber/v2"

func main() {
	app := fiber.New()
	app.Get("/healthz", healthCheck)
}
`,
	})

	ctx := context.Background()
	inv, err := inventory.ScanRepository(ctx, repoDir, inventory.DefaultScannerOptions(nil))
	require.NoError(t, err)

	facts, err := AnalyzeRepositoryFacts(ctx, repoDir, inv)
	require.NoError(t, err)

	techMap := make(map[string]engine.TechnologyItem)
	for _, tech := range facts.Technologies {
		techMap[tech.Name] = tech
	}

	assert.Contains(t, techMap, "Fiber")
	assert.Contains(t, techMap, "GORM")
	assert.Contains(t, techMap, "Go")

	// Gin, Echo, Django absent
	assert.NotContains(t, techMap, "Gin")
	assert.NotContains(t, techMap, "Echo")
	assert.NotContains(t, techMap, "Django")
	assert.Contains(t, techMap["Fiber"].NegativeAssertionsPassed, "Gin")
	assert.Contains(t, techMap["Fiber"].NegativeAssertionsPassed, "Echo")

	// Routes
	assert.Equal(t, 1, len(facts.Routes))
	assert.Equal(t, "GET", facts.Routes[0].Method)
	assert.Equal(t, "/healthz", facts.Routes[0].Path)
}
