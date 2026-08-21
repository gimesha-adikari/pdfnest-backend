package ast

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/parsers"
)

func TestGoAST_Extraction(t *testing.T) {
	tmpDir := t.TempDir()

	goSource := `package main

import (
	"os"
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type User struct {
	ID        uint           ` + "`gorm:\"primaryKey\" json:\"id\"`" + `
	Email     string         ` + "`gorm:\"uniqueIndex;not null\" json:\"email\"`" + `
	CreatedAt time.Time
}

type UserService interface {
	GetUser(id string) (*User, error)
}

func GetUserHandler(c *fiber.Ctx) error {
	secret := os.Getenv("JWT_SECRET")
	_ = secret
	return c.SendString("ok")
}

func RegisterRoutes(app *fiber.App) {
	app.Get("/api/v1/users", GetUserHandler)
	app.Post("/api/v1/users", GetUserHandler)
}
`
	err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(goSource), 0644)
	require.NoError(t, err)

	analyzer := NewAnalyzer(DefaultResourceLimits())
	ctx := context.Background()

	res, err := analyzer.Analyze(ctx, ASTRequest{
		RootDir:     tmpDir,
		TargetFiles: []string{"main.go"},
	})
	require.NoError(t, err)
	require.Equal(t, 1, res.FilesAnalyzed)
	require.Equal(t, 0, res.FilesSkipped)

	// Symbols
	assert.GreaterOrEqual(t, len(res.Symbols), 4) // User, UserService, GetUserHandler, RegisterRoutes
	var userStructFound bool
	for _, sym := range res.Symbols {
		if sym.Name == "User" && sym.Kind == SymbolKindStruct {
			userStructFound = true
		}
	}
	assert.True(t, userStructFound, "User struct must be extracted")

	// Models
	require.Len(t, res.ModelStructures, 1)
	assert.Equal(t, "User", res.ModelStructures[0].Name)
	assert.Equal(t, "gorm", res.ModelStructures[0].Framework)

	// Routes
	require.Len(t, res.Routes, 2)
	assert.Equal(t, "GET", res.Routes[0].Method)
	assert.Equal(t, "/api/v1/users", res.Routes[0].Path)
	assert.Equal(t, "POST", res.Routes[1].Method)
	assert.Equal(t, "/api/v1/users", res.Routes[1].Path)

	// Environment
	require.Len(t, res.EnvironmentReferences, 1)
	assert.Equal(t, "JWT_SECRET", res.EnvironmentReferences[0].Name)
	assert.Equal(t, "os.Getenv", res.EnvironmentReferences[0].AccessType)

	// Evidence
	assert.GreaterOrEqual(t, len(res.Evidence), 2) // Fiber, GORM
}

func TestTSJS_Extraction(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Next.js App Router file: app/api/auth/[id]/route.ts
	appRouterDir := filepath.Join(tmpDir, "app", "api", "auth", "[id]")
	require.NoError(t, os.MkdirAll(appRouterDir, 0755))

	appRouterSource := `import { NextResponse } from 'next/server';

export async function GET(request: Request) {
    const apiKey = process.env.API_KEY;
    return NextResponse.json({ ok: true });
}

export async function POST(request: Request) {
    const dbUrl = process.env["DATABASE_URL"];
    return NextResponse.json({ ok: true });
}
`
	require.NoError(t, os.WriteFile(filepath.Join(appRouterDir, "route.ts"), []byte(appRouterSource), 0644))

	// 2. Express file: server.js
	expressSource := `const express = require('express');
const app = express();

app.get('/api/health', (req, res) => {
    res.json({ status: 'ok' });
});

export function StartServer() {
    app.listen(3000);
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "server.js"), []byte(expressSource), 0644))

	// 3. Prisma Schema: schema.prisma
	prismaSource := `datasource db {
  provider = "postgresql"
  url      = env("DATABASE_URL")
}

model Account {
  id        String   @id @default(uuid())
  email     String   @unique
  isActive  Boolean  @default(true)
}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "schema.prisma"), []byte(prismaSource), 0644))

	analyzer := NewAnalyzer(DefaultResourceLimits())
	ctx := context.Background()

	res, err := analyzer.Analyze(ctx, ASTRequest{
		RootDir: tmpDir,
		TargetFiles: []string{
			"app/api/auth/[id]/route.ts",
			"server.js",
			"schema.prisma",
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 3, res.FilesAnalyzed)

	// Routes: Next.js App router + Express route
	assert.GreaterOrEqual(t, len(res.Routes), 3)

	var nextGetFound, nextPostFound, expressHealthFound bool
	for _, r := range res.Routes {
		if r.Method == "GET" && r.Path == "/api/auth/:id" {
			nextGetFound = true
		}
		if r.Method == "POST" && r.Path == "/api/auth/:id" {
			nextPostFound = true
		}
		if r.Method == "GET" && r.Path == "/api/health" {
			expressHealthFound = true
		}
	}
	assert.True(t, nextGetFound, "Next.js App Router GET route must be extracted")
	assert.True(t, nextPostFound, "Next.js App Router POST route must be extracted")
	assert.True(t, expressHealthFound, "Express GET /api/health route must be extracted")

	// Environment: API_KEY, DATABASE_URL
	assert.GreaterOrEqual(t, len(res.EnvironmentReferences), 2)

	// Prisma Model
	require.Len(t, res.ModelStructures, 1)
	assert.Equal(t, "Account", res.ModelStructures[0].Name)
	assert.Equal(t, "prisma", res.ModelStructures[0].Framework)
	assert.GreaterOrEqual(t, len(res.ModelStructures[0].Fields), 3)
}

func TestResourceLimits_Safety(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Huge file (> MaxFileSize)
	hugeFile := filepath.Join(tmpDir, "huge.go")
	hugeData := make([]byte, 600*1024) // 600KB > 500KB
	for i := range hugeData {
		hugeData[i] = ' '
	}
	require.NoError(t, os.WriteFile(hugeFile, hugeData, 0644))

	// 2. Malformed Go source
	malformedFile := filepath.Join(tmpDir, "bad.go")
	require.NoError(t, os.WriteFile(malformedFile, []byte("package main\nfunc syntax_error() {{"), 0644))

	// 3. Valid Go source
	validFile := filepath.Join(tmpDir, "good.go")
	require.NoError(t, os.WriteFile(validFile, []byte("package main\nvar x = 1"), 0644))

	analyzer := NewAnalyzer(&ResourceLimits{
		MaxFileSize: 512 * 1024,
	})

	ctx := context.Background()
	res, err := analyzer.Analyze(ctx, ASTRequest{
		RootDir: tmpDir,
		TargetFiles: []string{
			"huge.go",
			"bad.go",
			"good.go",
			"../escape.go", // Sandbox escape attempt
		},
	})
	require.NoError(t, err)

	// good.go and bad.go are attempted; huge.go and escape.go are skipped
	assert.Equal(t, 2, res.FilesSkipped)
	assert.Equal(t, 2, res.FilesAnalyzed)

	var hasSizeDiag, hasEscapeDiag, hasParseDiag bool
	for _, d := range res.Diagnostics {
		if d.Code == "FILE_SIZE_LIMIT_EXCEEDED" {
			hasSizeDiag = true
		}
		if d.Code == "SANDBOX_ESCAPE_REJECTED" {
			hasEscapeDiag = true
		}
		if d.Code == "PARSE_SYNTAX_ERROR" {
			hasParseDiag = true
		}
	}
	assert.True(t, hasSizeDiag, "Must diagnose huge file size limit")
	assert.True(t, hasEscapeDiag, "Must diagnose sandbox escape attempt")
	assert.True(t, hasParseDiag, "Must diagnose syntax error gracefully")
}

func TestDeterministic_Ordering(t *testing.T) {
	tmpDir := t.TempDir()

	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "b.go"), []byte("package b\nfunc B() {}\nfunc A() {}"), 0644))
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "a.go"), []byte("package a\nfunc D() {}\nfunc C() {}"), 0644))

	analyzer := NewAnalyzer(DefaultResourceLimits())
	ctx := context.Background()

	res1, err := analyzer.Analyze(ctx, ASTRequest{
		RootDir:     tmpDir,
		TargetFiles: []string{"b.go", "a.go"},
	})
	require.NoError(t, err)

	res2, err := analyzer.Analyze(ctx, ASTRequest{
		RootDir:     tmpDir,
		TargetFiles: []string{"a.go", "b.go"},
	})
	require.NoError(t, err)

	assert.Equal(t, len(res1.Symbols), len(res2.Symbols))
	for i := range res1.Symbols {
		assert.Equal(t, res1.Symbols[i].Name, res2.Symbols[i].Name)
		assert.Equal(t, res1.Symbols[i].SourceFile, res2.Symbols[i].SourceFile)
	}
}

func TestAdapter_EnrichAnalysisFacts(t *testing.T) {
	facts := &parsers.AnalysisFacts{
		Routes: []engine.ApiRouteItem{
			{
				Method:     "GET",
				Path:       "/api/v1/users",
				SourceFile: "routes.go",
			},
		},
		Environment: []engine.EnvironmentVariable{
			{
				Name:       "PORT",
				Required:   false,
				References: []string{},
			},
		},
		Technologies: []engine.TechnologyItem{
			{
				Name:     "Fiber",
				Evidence: []engine.EvidenceItem{},
			},
		},
	}

	lineNum := 42
	handler := "GetUserHandler"
	astRes := &ASTAnalysisResult{
		Routes: []engine.ApiRouteItem{
			{
				Method:          "GET",
				Path:            "/api/v1/users",
				SourceFile:      "routes.go",
				LineNumber:      &lineNum,
				InferredHandler: &handler,
			},
			{
				Method:     "POST",
				Path:       "/api/v1/users",
				SourceFile: "routes.go",
			},
		},
		EnvironmentReferences: []EnvironmentUsage{
			{
				Name:       "PORT",
				SourceFile: "main.go",
			},
		},
		Evidence: []engine.EvidenceItem{
			{
				FilePath: "main.go",
				RuleType: "source_import",
				Detail:   "import github.com/gofiber/fiber/v2",
			},
		},
	}

	EnrichAnalysisFacts(facts, astRes)

	// Route upgraded & new route appended
	require.Len(t, facts.Routes, 2)
	assert.Equal(t, &handler, facts.Routes[0].InferredHandler)
	assert.Equal(t, &lineNum, facts.Routes[0].LineNumber)

	// Environment references enriched
	require.Len(t, facts.Environment[0].References, 1)
	assert.Equal(t, "main.go", facts.Environment[0].References[0])

	// Evidence enriched
	require.Len(t, facts.Technologies[0].Evidence, 1)
	assert.Equal(t, "main.go", facts.Technologies[0].Evidence[0].FilePath)
}

func TestConcurrency_RaceDetector(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "app.go"), []byte("package app\nfunc Run() {}"), 0644))

	analyzer := NewAnalyzer(DefaultResourceLimits())
	ctx := context.Background()

	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := analyzer.Analyze(ctx, ASTRequest{
				RootDir:     tmpDir,
				TargetFiles: []string{"app.go"},
			})
			assert.NoError(t, err)
			done <- true
		}()
	}

	for i := 0; i < 10; i++ {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent test timed out")
		}
	}
}
