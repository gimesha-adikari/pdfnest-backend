package api

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/exclusion"
)

func TestSecurity_IDOR_Isolation(t *testing.T) {
	db, rClient := setupTestDBAndRedis(t)
	svc := NewService(db, rClient, "pdfnest:analyzer:jobs")
	ctx := context.Background()

	userAlice := "user:alice-" + uuid.NewString()
	userBob := "user:bob-" + uuid.NewString()

	// 1. Alice creates session
	sessionAlice, err := svc.CreateSession(ctx, userAlice, CreateSessionRequest{
		SourceType: engine.SourceTypeGit,
		GitURL:     "https://github.com/facebook/react.git",
	})
	require.NoError(t, err)

	// 2. Bob attempts to access Alice's session
	_, err = svc.GetSession(ctx, userBob, sessionAlice.SessionID)
	assert.ErrorIs(t, err, ErrSessionNotFound, "Bob must not access Alice's session")

	// 3. Bob attempts to update Alice's scope
	_, err = svc.UpdateScope(ctx, userBob, sessionAlice.SessionID, UpdateScopeRequest{
		CustomPatterns: []string{"*.malicious"},
	})
	assert.ErrorIs(t, err, ErrSessionNotFound, "Bob must not modify Alice's scope")

	// 4. Bob attempts to trigger analysis on Alice's session
	_, err = svc.Analyze(ctx, userBob, sessionAlice.SessionID, AnalyzeRequest{})
	assert.ErrorIs(t, err, ErrSessionNotFound, "Bob must not trigger analysis on Alice's session")

	// 5. Alice triggers analysis
	analyzeResp, err := svc.Analyze(ctx, userAlice, sessionAlice.SessionID, AnalyzeRequest{})
	require.NoError(t, err)

	// 6. Bob attempts to read Alice's task status
	_, err = svc.GetTaskStatus(ctx, userBob, analyzeResp.TaskID)
	assert.ErrorIs(t, err, ErrTaskNotFound, "Bob must not view Alice's task status")

	// 7. Bob attempts to get Alice's result
	_, err = svc.GetResult(ctx, userBob, sessionAlice.SessionID)
	assert.ErrorIs(t, err, ErrSessionNotFound, "Bob must not read Alice's analysis result")
}

func TestSecurity_MandatoryExclusionPrecedence(t *testing.T) {
	adapter := NewScopeConfigAdapter()

	// User attempts to force-include private keys and credentials
	req := UpdateScopeRequest{
		ForceIncludes: []string{"secrets/id_rsa", "certs/server.pem", "config/credentials.json"},
	}

	scopeConfig, _, err := adapter.AdaptAndValidate(req)
	require.NoError(t, err)

	// Build exclusion engine using the adapted scope
	exEngine := exclusion.NewEngine(scopeConfig.ToExclusionConfig())

	// Evaluate against mandatory security targets
	assert.True(t, exEngine.Evaluate("secrets/id_rsa").IsExcluded, "id_rsa must NEVER be included")
	assert.True(t, exEngine.Evaluate("certs/server.pem").IsExcluded, "server.pem must NEVER be included")
	assert.True(t, exEngine.Evaluate("config/credentials.json").IsExcluded, "credentials.json must NEVER be included")
}

func TestSecurity_SSRF_RejectionMatrix(t *testing.T) {
	db, rClient := setupTestDBAndRedis(t)
	svc := NewService(db, rClient, "pdfnest:analyzer:jobs")
	ctx := context.Background()

	user := "user:ssrf-matrix-" + uuid.NewString()

	maliciousURLs := []string{
		"http://127.0.0.1/repo.git",
		"https://127.0.0.1:8443/repo.git",
		"http://localhost/repo.git",
		"http://10.0.0.1/internal.git",
		"http://192.168.1.1/router.git",
		"http://172.16.0.1/docker.git",
		"http://169.254.169.254/metadata.git",
		"http://metadata.google.internal/computeMetadata/v1",
	}

	for _, u := range maliciousURLs {
		t.Run("Block SSRF "+u, func(t *testing.T) {
			_, err := svc.CreateSession(ctx, user, CreateSessionRequest{
				SourceType: engine.SourceTypeGit,
				GitURL:     u,
			})
			assert.Error(t, err, "Expected SSRF rejection for %s", u)
		})
	}
}
