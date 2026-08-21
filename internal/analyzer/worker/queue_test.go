package worker

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/analyzer/engine"
)

func TestMemoryJobQueueOperations(t *testing.T) {
	q := NewMemoryJobQueue(10)
	defer q.Close()

	ctx := context.Background()

	job := &AnalyzerJob{
		TaskID:     "task-123",
		SessionID:  "session-456",
		SourceType: engine.SourceTypeGit,
		GitURL:     "https://github.com/torvalds/linux.git",
	}

	require.NoError(t, q.Push(job))

	// Receive Job
	recvJob, handle, err := q.Receive(ctx)
	require.NoError(t, err)
	require.NotNil(t, recvJob)
	assert.Equal(t, "task-123", recvJob.TaskID)
	assert.Equal(t, "task-123", handle)

	// Publish Progress
	progress := TaskProgress{
		TaskID:          "task-123",
		SessionID:       "session-456",
		Status:          StatusAnalyzing,
		ProgressPercent: 50,
	}
	require.NoError(t, q.PublishProgress(ctx, "task-123", progress))

	p, exists := q.GetProgress("task-123")
	assert.True(t, exists)
	assert.Equal(t, StatusAnalyzing, p.Status)

	// Publish Result
	canon := engine.NewEmptyCanonicalResult("session-456", "linux", engine.SourceTypeGit)
	require.NoError(t, q.PublishResult(ctx, "task-123", canon))

	res, exists := q.GetResult("task-123")
	assert.True(t, exists)
	assert.Equal(t, "session-456", res.AnalysisID)

	// Ack & Close
	assert.NoError(t, q.Ack(ctx, handle))
	assert.NoError(t, q.Close())

	// Push on closed queue should error
	assert.ErrorIs(t, q.Push(job), ErrQueueClosed)
}
