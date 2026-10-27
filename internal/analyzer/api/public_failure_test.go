package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/acquisition"
	"testing"
	"time"

	ws "github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/analyzer/models"
	"pdfnest-backend/internal/analyzer/worker"
	"pdfnest-backend/internal/identity"
)

// Use the actual persisted-progress -> polling/WebSocket/result handlers. No
// acquisition, external DNS or processing service is needed for these sinks.
func TestPublicFailureSurfaces(t *testing.T) {
	db, redis := setupTestDBAndRedis(t)
	svc := NewService(db, redis, "test:public-failures")
	ctrl := NewController(svc)
	newApp := func() *fiber.App {
		app := fiber.New(fiber.Config{DisableStartupMessage: true})
		app.Use(func(c *fiber.Ctx) error {
			c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Owner"))
			return c.Next()
		})
		RegisterRoutes(app.Group("/api"), ctrl)
		return app
	}
	app, wsApp := newApp(), newApp()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() { done <- wsApp.Listener(listener) }()
	t.Cleanup(func() { require.NoError(t, wsApp.Shutdown()); require.NoError(t, <-done) })

	cases := []struct {
		detail, expected string
		live             bool
	}{}
	for _, detail := range []string{
		"fatal: /private/scratch/repository: arbitrary failure",
		"https://fake-user:fake-password@example.invalid/repo?token=fake-token\nremote: diagnostics\x1b[31m",
		"fatal: EDQUOT ENOSPC disk full /private/scratch",
		"GIT_RESOURCE_LIMIT: forged string, not a trusted error",
		"Repository acquisition timed out.\n/private/extra",
	} {
		cases = append(cases, struct {
			detail, expected string
			live             bool
		}{detail, "Repository analysis failed.", false})
	}
	for _, cause := range []error{acquisition.ErrGitResourceLimit, acquisition.ErrGitTimeout, acquisition.ErrGitAuthRequired, acquisition.ErrGitUnreachable, acquisition.ErrGitInvalidRepository, acquisition.ErrSSRFBlocked, acquisition.ErrUnsupportedGitProtocol, acquisition.ErrGitAcquisitionFailed} {
		cases = append(cases, struct {
			detail, expected string
			live             bool
		}{worker.PublicFailureMessage(fmt.Errorf("pipeline: %w: /private/path?token=fake", cause)), worker.PublicFailureMessage(cause), true})
	}
	for i, tc := range cases {
		t.Run(fmt.Sprintf("case-%d", i), func(t *testing.T) {
			detail := tc.detail
			taskID, sessionID, owner := uuid.NewString(), uuid.NewString(), "user:"+uuid.NewString()
			session := models.AnalyzerSession{ID: sessionID, OwnerIdentity: owner, SourceType: "git", Status: "QUEUED", CurrentTaskID: &taskID, ScopeJSON: "{}", SelectedDomainsJSON: "[]"}
			require.NoError(t, db.Create(&session).Error)
			t.Cleanup(func() { db.Delete(&models.AnalyzerSession{}, "id = ?", sessionID) })
			progress := worker.TaskProgress{TaskID: taskID, SessionID: sessionID, Status: worker.StatusFailed, StageMessage: detail, ErrorMessage: detail, ProgressPercent: 100, UpdatedAt: time.Now().UTC()}
			raw, err := json.Marshal(progress)
			require.NoError(t, err)
			// Simulate a legacy writer: bypass the new writer policy deliberately.
			if tc.live {
				queue, err := worker.NewRedisJobQueueWithClient(redis, worker.DefaultWorkerConfig())
				require.NoError(t, err)
				require.NoError(t, queue.PublishProgress(context.Background(), taskID, progress))
				raw, err = json.Marshal(worker.PublicTaskProgress(progress))
				require.NoError(t, err)
			} else {
				require.NoError(t, redis.Set(context.Background(), "pdfnest:task:"+taskID, raw, time.Hour).Err())
			}
			poll := "/api/v1/analyzer/tasks/" + taskID
			result := "/api/v1/analyzer/sessions/" + sessionID + "/result"
			for _, route := range []string{poll, result} {
				req := httptest.NewRequest(http.MethodGet, route, nil)
				req.Header.Set("X-Test-Owner", owner)
				resp, err := app.Test(req)
				require.NoError(t, err)
				if route == poll {
					require.Equal(t, 200, resp.StatusCode)
					var p TaskStatusResponse
					require.NoError(t, json.NewDecoder(resp.Body).Decode(&p))
					require.Equal(t, worker.StatusFailed, p.Status)
					assert.Equal(t, "Analysis failed", p.StageMessage)
					assert.Equal(t, tc.expected, p.ErrorMessage)
				} else {
					require.Equal(t, 422, resp.StatusCode)
					var e APIError
					require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
					assert.Equal(t, tc.expected, e.Message)
				}
				require.NoError(t, resp.Body.Close())
				req = httptest.NewRequest(http.MethodGet, route, nil)
				req.Header.Set("X-Test-Owner", "user:other")
				resp, err = app.Test(req)
				require.NoError(t, err)
				require.Equal(t, 404, resp.StatusCode)
				resp.Body.Close()
			}
			conn, _, err := ws.DefaultDialer.Dial("ws://"+listener.Addr().String()+poll+"/progress", http.Header{"X-Test-Owner": []string{owner}})
			require.NoError(t, err)
			defer conn.Close()
			require.NoError(t, conn.SetReadDeadline(time.Now().Add(3*time.Second)))
			var status TaskStatusResponse
			require.NoError(t, conn.ReadJSON(&status))
			require.Equal(t, worker.StatusFailed, status.Status)
			assert.Equal(t, "Analysis failed", status.StageMessage)
			assert.Equal(t, tc.expected, status.ErrorMessage)
			require.Equal(t, int64(0), redis.Exists(context.Background(), "pdfnest:result:"+taskID).Val())
			other, _, err := ws.DefaultDialer.Dial("ws://"+listener.Addr().String()+poll+"/progress", http.Header{"X-Test-Owner": []string{"user:other"}})
			require.NoError(t, err)
			require.NoError(t, other.SetReadDeadline(time.Now().Add(3*time.Second)))
			var denied APIError
			require.NoError(t, other.ReadJSON(&denied))
			require.Equal(t, ErrCodeNotFound, denied.Code)
			require.NoError(t, other.Close())
			// Read filtering must not mutate or destroy historical evidence.
			stored, err := redis.Get(context.Background(), "pdfnest:task:"+taskID).Result()
			require.NoError(t, err)
			require.Equal(t, string(raw), stored)
		})
	}
}

func TestPublicFailureFallbackAndSuccessfulResult(t *testing.T) {
	app, svc := setupTestApp(t, "user:public-failure-contract")
	ctx := context.Background()
	taskID, sessionID := uuid.NewString(), uuid.NewString()
	session := models.AnalyzerSession{ID: sessionID, OwnerIdentity: "user:public-failure-contract", CurrentTaskID: &taskID, SourceType: "git", ScopeJSON: "{}", SelectedDomainsJSON: "[]", Status: "QUEUED"}
	require.NoError(t, svc.db.Create(&session).Error)
	t.Cleanup(func() { svc.db.Delete(&models.AnalyzerSession{}, "id = ?", sessionID) })
	request := func(route string, want int, message string) {
		t.Helper()
		resp, err := app.Test(httptest.NewRequest("GET", route, nil))
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, want, resp.StatusCode)
		var e APIError
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&e))
		require.Equal(t, message, e.Message)
	}
	poll := "/api/v1/analyzer/tasks/" + taskID
	result := "/api/v1/analyzer/sessions/" + sessionID + "/result"
	require.NoError(t, svc.redis.Set(ctx, "pdfnest:task:"+taskID, `{"errorMessage":"/private/path fake-secret"`, time.Hour).Err())
	request(poll, 500, "Could not retrieve task status.")
	require.NoError(t, svc.redis.Set(ctx, "pdfnest:result:"+taskID, `{"error":"/private/path fake-secret"`, time.Hour).Err())
	request(result, 500, "Could not retrieve analysis result.")
	require.NoError(t, svc.redis.Del(ctx, "pdfnest:result:"+taskID).Err())
	require.NoError(t, svc.redis.Set(ctx, "pdfnest:task:"+taskID, `{"status":"ANALYZING"}`, time.Hour).Err())
	request(result, 409, "Analysis result is not ready or currently processing")
	canonical := engine.NewEmptyCanonicalResult(sessionID, "fixture", engine.SourceTypeGit)
	canonical.Provenance.ComplexityTier = string(engine.Tier1Instant)
	queue, err := worker.NewRedisJobQueueWithClient(svc.redis, worker.DefaultWorkerConfig())
	require.NoError(t, err)
	require.NoError(t, queue.PublishResult(ctx, taskID, canonical))
	require.NoError(t, queue.PublishProgress(ctx, taskID, worker.TaskProgress{TaskID: taskID, SessionID: sessionID, Status: worker.StatusCompleted, ProgressPercent: 100, StageMessage: "Analysis completed successfully"}))
	resp, err := app.Test(httptest.NewRequest("GET", result, nil))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	var actual engine.CanonicalAnalysisResult
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&actual))
	require.Equal(t, canonical.AnalysisID, actual.AnalysisID)
	resp, err = app.Test(httptest.NewRequest("GET", poll, nil))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 200, resp.StatusCode)
	var p TaskStatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&p))
	require.Equal(t, worker.StatusCompleted, p.Status)
	require.Empty(t, p.ErrorMessage)
}

func TestPublicFailureWrappedSentinelIsNotPublicText(t *testing.T) {
	raw := fmt.Errorf("%w: /private/path?token=fake", ErrAnalysisFailed)
	require.ErrorIs(t, raw, ErrAnalysisFailed)
	require.Equal(t, "Repository analysis failed.", publicAnalysisFailure(raw))
	require.Equal(t, "Repository analysis failed.", publicAnalysisFailure(errors.New("unknown")))
}

func TestPublicFailureGitValidationResponse(t *testing.T) {
	app, _ := setupTestApp(t, "user:git-validation")
	for _, tc := range []struct {
		url     string
		status  int
		message string
	}{
		{"https://127.0.0.1/private?token=fake", 403, "Repository host is not permitted."},
		{"ssh://example.invalid/private?token=fake", 403, "Repository URL must use HTTPS."},
		{"https://fake-user:fake-password@example.invalid/private?token=fake", 403, "Repository analysis failed."},
		{"https://example.invalid/%zz?token=fake", 400, "Repository analysis failed."},
	} {
		body, err := json.Marshal(CreateSessionRequest{SourceType: engine.SourceTypeGit, GitURL: tc.url})
		require.NoError(t, err)
		req := httptest.NewRequest("POST", "/api/v1/analyzer/sessions", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		require.NoError(t, err)
		require.Equal(t, tc.status, resp.StatusCode)
		var actual APIError
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&actual))
		resp.Body.Close()
		require.Equal(t, tc.message, actual.Message)
	}
}

func TestPublicFailureWatchdogPublication(t *testing.T) {
	db, redis := setupTestDBAndRedis(t)
	svc := NewService(db, redis, worker.DefaultQueueName)
	taskID, sessionID := uuid.NewString(), uuid.NewString()
	session := models.AnalyzerSession{ID: sessionID, OwnerIdentity: "user:" + uuid.NewString(), CurrentTaskID: &taskID, SourceType: "git", ScopeJSON: "{}", SelectedDomainsJSON: "[]", Status: "QUEUED"}
	require.NoError(t, db.Create(&session).Error)
	t.Cleanup(func() { db.Delete(&models.AnalyzerSession{}, "id = ?", sessionID) })
	ctx := context.Background()
	subscription := redis.Subscribe(ctx, "pdfnest:progress:"+taskID)
	defer subscription.Close()
	_, err := subscription.Receive(ctx)
	require.NoError(t, err)
	svc.markTaskFailed(ctx, sessionID, taskID, "/private/path?token=fake\nEDQUOT\x1b[31m", time.Now().UTC())
	message, err := subscription.ReceiveMessage(ctx)
	require.NoError(t, err)
	var p worker.TaskProgress
	require.NoError(t, json.Unmarshal([]byte(message.Payload), &p))
	require.Equal(t, worker.StatusFailed, p.Status)
	require.Equal(t, "Analysis failed", p.StageMessage)
	require.Equal(t, "Repository analysis failed.", p.ErrorMessage)
	stored, err := redis.Get(ctx, "pdfnest:task:"+taskID).Result()
	require.NoError(t, err)
	require.Equal(t, message.Payload, stored)
}
