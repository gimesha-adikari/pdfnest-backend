package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/identity"
)

func setupTestApp(t *testing.T, identityID string) (*fiber.App, *Service) {
	db, rClient := setupTestDBAndRedis(t)
	svc := NewService(db, rClient, "pdfnest:analyzer:jobs")
	ctrl := NewController(svc)

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, identityID)
		return c.Next()
	})

	apiGroup := app.Group("/api")
	RegisterRoutes(apiGroup, ctrl)

	return app, svc
}

func TestController_EndToEndRoutes(t *testing.T) {
	userID := "user:controller-test-1"
	app, svc := setupTestApp(t, userID)

	// 1. POST /api/v1/analyzer/sessions
	createReq := CreateSessionRequest{
		SourceType: engine.SourceTypeGit,
		GitURL:     "https://github.com/torvalds/linux.git",
	}
	body, _ := json.Marshal(createReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyzer/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusCreated, resp.StatusCode)

	var sessionResp SessionResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&sessionResp))
	assert.NotEmpty(t, sessionResp.SessionID)
	assert.Equal(t, "linux", sessionResp.RepositoryName)

	sessionID := sessionResp.SessionID

	// 2. GET /api/v1/analyzer/sessions/:id
	req = httptest.NewRequest(http.MethodGet, "/api/v1/analyzer/sessions/"+sessionID, nil)
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 3. GET /api/v1/analyzer/sessions/:id/tree
	req = httptest.NewRequest(http.MethodGet, "/api/v1/analyzer/sessions/"+sessionID+"/tree", nil)
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 4. PUT /api/v1/analyzer/sessions/:id/scope
	scopeReq := UpdateScopeRequest{
		CustomPatterns:  []string{"*.log"},
		EnabledPresets:  []string{"node_modules"},
		SelectedDomains: []string{"Technology Stack"},
	}
	body, _ = json.Marshal(scopeReq)
	req = httptest.NewRequest(http.MethodPut, "/api/v1/analyzer/sessions/"+sessionID+"/scope", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 5. POST /api/v1/analyzer/sessions/:id/analyze
	analyzeReq := AnalyzeRequest{SelectedDomains: []string{"Technology Stack"}}
	body, _ = json.Marshal(analyzeReq)
	req = httptest.NewRequest(http.MethodPost, "/api/v1/analyzer/sessions/"+sessionID+"/analyze", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusAccepted, resp.StatusCode)

	var analyzeResp AnalyzeResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&analyzeResp))
	assert.NotEmpty(t, analyzeResp.TaskID)

	taskID := analyzeResp.TaskID

	// 6. GET /api/v1/analyzer/tasks/:id
	req = httptest.NewRequest(http.MethodGet, "/api/v1/analyzer/tasks/"+taskID, nil)
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)

	// 7. GET /api/v1/analyzer/sessions/:id/result (Before completion -> 409 Conflict)
	req = httptest.NewRequest(http.MethodGet, "/api/v1/analyzer/sessions/"+sessionID+"/result", nil)
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusConflict, resp.StatusCode)

	// 8. Publish mock result and retrieve (200 OK)
	canon := engine.NewEmptyCanonicalResult(sessionID, "linux", engine.SourceTypeGit)
	canon.Provenance.ComplexityTier = string(engine.Tier1Instant)
	canonJSON, err := engine.ToCanonicalJSON(canon)
	require.NoError(t, err)
	require.NoError(t, svc.redis.Set(req.Context(), "pdfnest:result:"+taskID, canonJSON, time.Hour).Err())

	req = httptest.NewRequest(http.MethodGet, "/api/v1/analyzer/sessions/"+sessionID+"/result", nil)
	resp, err = app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestController_SSRFBlocked(t *testing.T) {
	app, _ := setupTestApp(t, "user:ssrf-tester")

	// Attempt SSRF against AWS metadata IP
	createReq := CreateSessionRequest{
		SourceType: engine.SourceTypeGit,
		GitURL:     "http://169.254.169.254/latest/meta-data.git",
	}
	body, _ := json.Marshal(createReq)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/analyzer/sessions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	assert.Equal(t, http.StatusForbidden, resp.StatusCode)
}
