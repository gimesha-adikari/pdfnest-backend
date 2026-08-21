package worker

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
)

func createTestRepoZip(t *testing.T) string {
	buf := new(bytes.Buffer)
	w := zip.NewWriter(buf)

	files := map[string]string{
		"package.json": `{
			"name": "fullstack-app",
			"version": "1.2.0",
			"dependencies": {
				"next": "14.2.0",
				"react": "^18.3.0",
				"@prisma/client": "^5.12.0",
				"pg": "^8.11.0",
				"ioredis": "^5.3.2"
			}
		}`,
		".env.example": "DATABASE_URL=postgres://localhost:5432/db\nREDIS_URL=redis://localhost:6379\nPORT=3000",
		"app/api/users/[id]/route.ts": `
export async function GET(req: Request) { return new Response("user"); }
export async function DELETE(req: Request) { return new Response("deleted"); }
`,
		"prisma/schema.prisma": `
datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}
`,
		"Dockerfile": "FROM node:18-alpine\nCOPY . .\nCMD [\"npm\", \"start\"]",
	}

	for name, content := range files {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		require.NoError(t, err)
		_, err = f.Write([]byte(content))
		require.NoError(t, err)
	}

	require.NoError(t, w.Close())

	zipFile := filepath.Join(t.TempDir(), "fullstack_app.zip")
	require.NoError(t, os.WriteFile(zipFile, buf.Bytes(), 0644))
	return zipFile
}

func TestExecutePipelineEndToEnd(t *testing.T) {
	zipPath := createTestRepoZip(t)
	tempBaseDir := t.TempDir()

	job := &AnalyzerJob{
		JobVersion:        JobVersion1,
		TaskID:            "task-fullstack-1",
		SessionID:         "session-fullstack-1",
		SourceType:        engine.SourceTypeZip,
		StagedArchivePath: zipPath,
	}

	stages := make([]TaskStatus, 0)
	onProgress := func(status TaskStatus, percent int, msg string) {
		stages = append(stages, status)
	}

	ctx := context.Background()
	result, err := ExecutePipeline(ctx, job, tempBaseDir, onProgress)
	require.NoError(t, err)
	require.NotNil(t, result)

	// 1. Verify Progress Progression
	assert.Contains(t, stages, StatusAcquiring)
	assert.Contains(t, stages, StatusInventory)
	assert.Contains(t, stages, StatusAnalyzing)
	assert.Contains(t, stages, StatusFinalizing)
	assert.Contains(t, stages, StatusCompleted)

	// 2. Validate Canonical Schema
	assert.NoError(t, engine.ValidateCanonicalResult(result))
	assert.Equal(t, engine.SchemaVersion, result.SchemaVersion)
	assert.Equal(t, "session-fullstack-1", result.AnalysisID)

	// 3. Verify Technologies & Negative Assertions
	techMap := make(map[string]engine.TechnologyItem)
	for _, tech := range result.Technologies {
		techMap[tech.Name] = tech
	}
	assert.Contains(t, techMap, "Next.js")
	assert.Contains(t, techMap, "React")
	assert.Contains(t, techMap, "PostgreSQL")
	assert.Contains(t, techMap, "Prisma")
	assert.Contains(t, techMap, "Redis")
	assert.Contains(t, techMap, "Docker")

	// Verify Negative Assertions
	assert.Contains(t, techMap["PostgreSQL"].NegativeAssertionsPassed, "MongoDB")
	assert.Contains(t, techMap["PostgreSQL"].NegativeAssertionsPassed, "MySQL")

	// 4. Verify Routes
	assert.Equal(t, 2, len(result.Routes))
	assert.Equal(t, "/api/users/:id", result.Routes[0].Path)

	// 5. Verify Provenance
	assert.Equal(t, engine.EngineNameGoAnalyzerWorker, result.Provenance.Engine)
	assert.NotEmpty(t, result.Provenance.SourceArtifactSha256)
	assert.NotEmpty(t, result.Provenance.ComplexityTier)

	// 6. Verify Sandbox Cleaned Up
	sandboxDir := filepath.Join(tempBaseDir, "platen_repo_session-fullstack-1")
	assert.NoDirExists(t, sandboxDir, "Sandbox directory must be cleaned up on completion")
}
