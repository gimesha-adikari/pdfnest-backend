package api

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/models"
	"pdfnest-backend/internal/analyzer/worker"
)

func setupTestDBAndRedis(t *testing.T) (*gorm.DB, *redis.Client) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = "host=localhost user=postgres password=2021 dbname=pdfnest port=5432 sslmode=disable"
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Skip("PostgreSQL database unavailable, skipping service integration test")
	}

	err = db.AutoMigrate(&models.AnalyzerSession{})
	require.NoError(t, err)

	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(func() { mr.Close() })

	rClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return db, rClient
}

func TestService_SessionLifecycleAndOwnership(t *testing.T) {
	db, rClient := setupTestDBAndRedis(t)
	svc := NewService(db, rClient, "pdfnest:analyzer:jobs")
	ctx := context.Background()

	userA := "user:alice-" + uuid.NewString()
	userB := "user:bob-" + uuid.NewString()

	// 1. Create Git Session
	gitReq := CreateSessionRequest{
		SourceType: engine.SourceTypeGit,
		GitURL:     "https://github.com/facebook/react.git",
	}
	sessionA, err := svc.CreateSession(ctx, userA, gitReq)
	require.NoError(t, err)
	assert.NotEmpty(t, sessionA.SessionID)
	assert.Equal(t, "react", sessionA.RepositoryName)
	assert.Equal(t, "CREATED", sessionA.Status)

	// 2. IDOR: User A can fetch session, User B is denied
	sA, err := svc.GetSession(ctx, userA, sessionA.SessionID)
	require.NoError(t, err)
	assert.Equal(t, sessionA.SessionID, sA.ID)

	sB, err := svc.GetSession(ctx, userB, sessionA.SessionID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
	assert.Nil(t, sB)

	// 3. Update Scope
	scopeReq := UpdateScopeRequest{
		CustomPatterns:  []string{"*.log", "dist/**"},
		EnabledPresets:  []string{"node_modules"},
		SelectedDomains: []string{"Technology Stack"},
	}
	scopeResp, err := svc.UpdateScope(ctx, userA, sessionA.SessionID, scopeReq)
	require.NoError(t, err)
	assert.Equal(t, []string{"*.log", "dist/**"}, scopeResp.CustomPatterns)
	assert.NotEmpty(t, scopeResp.ScopeHash)

	// User B cannot update User A's scope
	_, err = svc.UpdateScope(ctx, userB, sessionA.SessionID, scopeReq)
	assert.ErrorIs(t, err, ErrSessionNotFound)

	// 4. Submit Valid Analysis
	analyzeResp, err := svc.Analyze(ctx, userA, sessionA.SessionID, AnalyzeRequest{
		SelectedDomains: []string{"Technology Stack"},
		EnableAi:        true,
	})
	require.NoError(t, err)
	assert.NotEmpty(t, analyzeResp.TaskID)
	assert.Equal(t, "QUEUED", analyzeResp.Status)

	// 6. Idempotency check: Repeated submit returns existing active task
	analyzeResp2, err := svc.Analyze(ctx, userA, sessionA.SessionID, AnalyzeRequest{})
	require.NoError(t, err)
	assert.Equal(t, analyzeResp.TaskID, analyzeResp2.TaskID, "Must return existing active task ID on duplicate submit")

	// 7. Verify Redis Queue Payload
	rawJob, err := rClient.RPop(ctx, "pdfnest:analyzer:jobs").Result()
	require.NoError(t, err)
	var job worker.AnalyzerJob
	require.NoError(t, json.Unmarshal([]byte(rawJob), &job))
	assert.Equal(t, analyzeResp.TaskID, job.TaskID)
	assert.Equal(t, sessionA.SessionID, job.SessionID)
	assert.Equal(t, "https://github.com/facebook/react.git", job.GitURL)
	assert.Equal(t, []string{"*.log", "dist/**"}, job.Scope.CustomPatterns)

	// 8. Verify Task Status
	statusResp, err := svc.GetTaskStatus(ctx, userA, analyzeResp.TaskID)
	require.NoError(t, err)
	assert.Equal(t, worker.StatusQueued, statusResp.Status)

	// User B cannot check User A's task status
	_, err = svc.GetTaskStatus(ctx, userB, analyzeResp.TaskID)
	assert.ErrorIs(t, err, ErrTaskNotFound)

	// 9. Publish Mock Result and Retrieve
	canon := engine.NewEmptyCanonicalResult(sessionA.SessionID, "react", engine.SourceTypeGit)
	canon.Provenance.ComplexityTier = string(engine.Tier1Instant)
	canonJSON, err := engine.ToCanonicalJSON(canon)
	require.NoError(t, err)
	require.NoError(t, rClient.Set(ctx, "pdfnest:result:"+analyzeResp.TaskID, canonJSON, time.Hour).Err())

	res, err := svc.GetResult(ctx, userA, sessionA.SessionID)
	require.NoError(t, err)
	assert.Equal(t, sessionA.SessionID, res.AnalysisID)

	// User B cannot get User A's result
	_, err = svc.GetResult(ctx, userB, sessionA.SessionID)
	assert.ErrorIs(t, err, ErrSessionNotFound)
}

func TestService_StorageKeySecurity(t *testing.T) {
	db, rClient := setupTestDBAndRedis(t)
	svc := NewService(db, rClient, "pdfnest:analyzer:jobs")
	ctx := context.Background()

	user := "user:test-" + uuid.NewString()

	// 1. Path traversal rejected
	_, err := svc.CreateSession(ctx, user, CreateSessionRequest{
		SourceType: engine.SourceTypeZip,
		StorageKey: "../../../etc/passwd",
	})
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStorageKey)

	// 2. Leading slash rejected
	_, err = svc.CreateSession(ctx, user, CreateSessionRequest{
		SourceType: engine.SourceTypeZip,
		StorageKey: "/var/log/secret.zip",
	})
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidStorageKey)

	// 3. Valid key accepted
	validSession, err := svc.CreateSession(ctx, user, CreateSessionRequest{
		SourceType: engine.SourceTypeZip,
		StorageKey: "repositories/raw/123-session.zip",
	})
	require.NoError(t, err)
	assert.Equal(t, "123-session", validSession.RepositoryName)
}
