package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/worker"
)

func TestFailure_AnalysisFailedHandling(t *testing.T) {
	db, rClient := setupTestDBAndRedis(t)
	svc := NewService(db, rClient, "pdfnest:analyzer:jobs")
	ctx := context.Background()

	user := "user:fail-test-" + uuid.NewString()

	// 1. Create Session
	session, err := svc.CreateSession(ctx, user, CreateSessionRequest{
		SourceType: engine.SourceTypeGit,
		GitURL:     "https://github.com/facebook/react.git",
	})
	require.NoError(t, err)

	// 2. Submit Analyze
	analyzeResp, err := svc.Analyze(ctx, user, session.SessionID, AnalyzeRequest{})
	require.NoError(t, err)

	// 3. Simulate Worker Marking Task as Failed
	failProgress := worker.TaskProgress{
		TaskID:          analyzeResp.TaskID,
		SessionID:       session.SessionID,
		Status:          worker.StatusFailed,
		ProgressPercent: 100,
		StageMessage:    "Analysis failed",
		ErrorMessage:    "disk space exceeded during clone",
		UpdatedAt:       time.Now().UTC(),
	}
	progJSON, _ := json.Marshal(failProgress)
	require.NoError(t, rClient.Set(ctx, "pdfnest:task:"+analyzeResp.TaskID, progJSON, time.Hour).Err())

	// 4. GetResult should return formatted ErrAnalysisFailed
	_, err = svc.GetResult(ctx, user, session.SessionID)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrAnalysisFailed)
	assert.Contains(t, err.Error(), "disk space exceeded")
}

func TestFailure_NonExistentSession(t *testing.T) {
	db, rClient := setupTestDBAndRedis(t)
	svc := NewService(db, rClient, "pdfnest:analyzer:jobs")
	ctx := context.Background()

	_, err := svc.GetSession(ctx, "user:someone", uuid.NewString())
	assert.ErrorIs(t, err, ErrSessionNotFound)

	_, err = svc.GetResult(ctx, "user:someone", uuid.NewString())
	assert.ErrorIs(t, err, ErrSessionNotFound)

	_, err = svc.GetTaskStatus(ctx, "user:someone", uuid.NewString())
	assert.ErrorIs(t, err, ErrTaskNotFound)
}
