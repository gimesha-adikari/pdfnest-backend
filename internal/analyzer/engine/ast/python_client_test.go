package ast

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/parsers"
)

func TestSelectPythonCandidates(t *testing.T) {
	tmpDir := t.TempDir()

	// 1. Normal python file
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "app.py"), []byte("from fastapi import FastAPI"), 0644))
	// 2. Non-python file
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte("package main"), 0644))
	// 3. Huge python file (>500KB)
	hugeData := make([]byte, 600*1024)
	for i := range hugeData {
		hugeData[i] = ' '
	}
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "huge.py"), hugeData, 0644))

	candidates, diags := SelectPythonCandidates(tmpDir, []string{
		"app.py",
		"main.go",
		"huge.py",
		"../escape.py",
	}, 20, 512*1024)

	require.Len(t, candidates, 1)
	assert.Equal(t, "app.py", candidates[0].Path)
	assert.Equal(t, "from fastapi import FastAPI", candidates[0].Content)

	var hasSizeDiag, hasEscapeDiag bool
	for _, d := range diags {
		if d.Code == "FILE_SIZE_LIMIT_EXCEEDED" {
			hasSizeDiag = true
		}
		if d.Code == "SANDBOX_ESCAPE_REJECTED" {
			hasEscapeDiag = true
		}
	}
	assert.True(t, hasSizeDiag, "Must diagnose huge file")
	assert.True(t, hasEscapeDiag, "Must diagnose sandbox escape attempt")
}

func TestPythonClient_Success(t *testing.T) {
	lineNum := 12
	handler := "list_users"
	expectedResp := PythonASTResponse{
		ProtocolVersion: "1.0.0",
		TaskID:          "task-123",
		Status:          "SUCCESS",
		DurationMs:      25,
		NodesProcessed:  100,
		Routes: []engine.ApiRouteItem{
			{
				Method:          "GET",
				Path:            "/api/v1/users",
				SourceFile:      "app/api/users.py",
				LineNumber:      &lineNum,
				InferredHandler: &handler,
				AuthRequired:    false,
			},
		},
		Models: []ModelItem{
			{
				Name:       "UserDTO",
				SourceFile: "app/models/user.py",
				LineNumber: 5,
				Framework:  "pydantic",
			},
		},
		EnvReferences: []EnvironmentUsage{
			{
				Name:       "DATABASE_URL",
				SourceFile: "app/api/users.py",
				LineNumber: 15,
				AccessType: "os.getenv",
			},
		},
		Evidence: []engine.EvidenceItem{
			{
				FilePath: "app/api/users.py",
				RuleType: "source_import",
				Detail:   "from fastapi import FastAPI",
			},
		},
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/analyzer/python-ast", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.NotEmpty(t, r.Header.Get("X-Worker-Signature"))

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(expectedResp)
	}))
	defer ts.Close()

	client := NewPythonClient(ts.URL, "test-secret", 2*time.Second)
	ctx := context.Background()

	resp, err := client.AnalyzePython(ctx, PythonASTRequest{
		TaskID:    "task-123",
		SessionID: "sess-123",
		Files: []PythonFilePayload{
			{Path: "app/api/users.py", Content: "x = 1"},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, "SUCCESS", resp.Status)
	assert.Len(t, resp.Routes, 1)
	assert.Equal(t, "/api/v1/users", resp.Routes[0].Path)
	assert.Len(t, resp.Models, 1)
	assert.Equal(t, "UserDTO", resp.Models[0].Name)
}

func TestPythonClient_ErrorHandling(t *testing.T) {
	// 1. HTTP 413 Payload Too Large
	ts413 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		_ = json.NewEncoder(w).Encode(PythonASTResponse{
			ProtocolVersion: "1.0.0",
			TaskID:          "task-err",
			Status:          "ERROR",
			Error: &ErrorDetail{
				Code:    "PAYLOAD_TOO_LARGE",
				Message: "payload exceeds limit",
			},
		})
	}))
	defer ts413.Close()

	client := NewPythonClient(ts413.URL, "test-secret", 1*time.Second)
	ctx := context.Background()

	resp, err := client.AnalyzePython(ctx, PythonASTRequest{
		TaskID:    "task-err",
		SessionID: "sess-err",
		Files:     []PythonFilePayload{{Path: "a.py", Content: "x"}},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "PAYLOAD_TOO_LARGE")
	assert.NotNil(t, resp)

	// 2. Timeout handling
	tsSlow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer tsSlow.Close()

	clientSlow := NewPythonClient(tsSlow.URL, "test-secret", 50*time.Millisecond)
	_, slowErr := clientSlow.AnalyzePython(ctx, PythonASTRequest{
		TaskID: "task-slow",
	})
	assert.Error(t, slowErr)
}

func TestEnrichWithPythonAST(t *testing.T) {
	facts := &parsers.AnalysisFacts{
		Routes: []engine.ApiRouteItem{
			{
				Method:     "GET",
				Path:       "/health",
				SourceFile: "app.py",
			},
		},
		Environment: []engine.EnvironmentVariable{
			{
				Name:       "DATABASE_URL",
				Required:   true,
				References: []string{},
			},
		},
		Technologies: []engine.TechnologyItem{
			{
				Name:     "FastAPI",
				Evidence: []engine.EvidenceItem{},
			},
		},
	}

	line := 24
	handler := "get_users"
	pyRes := &PythonASTResponse{
		Status: "SUCCESS",
		Routes: []engine.ApiRouteItem{
			{
				Method:          "GET",
				Path:            "/users",
				SourceFile:      "users.py",
				LineNumber:      &line,
				InferredHandler: &handler,
			},
		},
		EnvReferences: []EnvironmentUsage{
			{
				Name:       "DATABASE_URL",
				SourceFile: "db.py",
			},
		},
		Evidence: []engine.EvidenceItem{
			{
				FilePath: "app.py",
				RuleType: "source_import",
				Detail:   "from fastapi import FastAPI",
			},
		},
	}

	EnrichWithPythonAST(facts, pyRes)

	// Routes enriched
	assert.Len(t, facts.Routes, 2)
	// Environment enriched
	assert.Len(t, facts.Environment[0].References, 1)
	assert.Equal(t, "db.py", facts.Environment[0].References[0])
	// Evidence enriched
	assert.Len(t, facts.Technologies[0].Evidence, 1)
}
