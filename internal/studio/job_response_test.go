package studio

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/studio/models"
)

func TestPublicStudioJobResponseOmitsInternalAndLargeFields(t *testing.T) {
	resultVersion := uuid.New()
	stateID := uuid.New()
	now := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	job := &models.StudioJob{
		ID: uuid.New(), DocumentID: uuid.New(), SessionID: uuid.New(), BaseVersionID: uuid.New(),
		ResultVersionID: &resultVersion, EditorStateID: &stateID, WorkerJobID: "worker-secret",
		JobType: string(StudioJobEditCompile), Status: "succeeded", Progress: 100, Message: "done",
		Error: "", ErrorCode: "", IdempotencyKey: "private-key", Parameters: models.JSON(bytes.Repeat([]byte("x"), 900_000)),
		Result: models.JSON(`{"artifact_key":"private-artifact"}`), SourceKey: "private-source", PayloadKey: "private-payload",
		ReconciledAt: &now, CreatedAt: now, UpdatedAt: now,
	}

	encoded, err := json.Marshal(publicStudioJobResult(&StudioJobResult{Job: job, IsIdempotentReplay: true}))
	require.NoError(t, err)
	assert.Less(t, len(encoded), 10_000, "public job response must stay small when editor parameters are large")
	for _, forbidden := range []string{"parameters", "worker_job_id", "idempotency_key", "artifact_key", "source_key", "payload_key", "private-key", "private-source"} {
		assert.NotContains(t, string(encoded), forbidden)
	}
	for _, required := range []string{"id", "document_id", "session_id", "base_version_id", "result_version_id", "editor_state_id", "job_type", "status", "progress", "message", "reconciled_at", "created_at", "updated_at", "is_idempotent_replay"} {
		assert.Contains(t, string(encoded), `"`+required+`"`)
	}
	assert.True(t, strings.Contains(string(encoded), `"is_idempotent_replay":true`))
}

func TestPublicStudioJobResponsePreservesFrontendContract(t *testing.T) {
	job := &models.StudioJob{ID: uuid.New(), DocumentID: uuid.New(), SessionID: uuid.New(), BaseVersionID: uuid.New(), JobType: string(StudioJobEditCompile), Status: "queued", Message: "queued", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	var decoded struct {
		Job map[string]any `json:"job"`
	}
	require.NoError(t, json.Unmarshal(mustMarshal(t, publicStudioJobResult(&StudioJobResult{Job: job})), &decoded))
	for _, key := range []string{"id", "session_id", "base_version_id", "job_type", "status", "progress", "message", "created_at", "updated_at"} {
		assert.Contains(t, decoded.Job, key)
	}
	assert.NotContains(t, decoded.Job, "parameters")
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
