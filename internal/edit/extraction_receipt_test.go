package edit

import (
	"encoding/json"
	"testing"

	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestExtractionReceiptRequiresOwnedSource(t *testing.T) {
	const owner = "user:editor"
	key := storage.NewOwnedKey(owner, "editor_source", ".pdf")
	sources, err := json.Marshal([]string{key})
	require.NoError(t, err)
	row := models.WorkerResultOwnership{WorkerJobID: uuid.NewString(), SourceKeys: string(sources), State: "active"}

	reply, err := extractionReceipt(row, owner)
	require.NoError(t, err)
	require.Equal(t, "accepted", reply.State)
	require.Equal(t, row.WorkerJobID, reply.JobID)
	require.Equal(t, key, reply.SourceTracker)

	_, err = extractionReceipt(row, "user:someone-else")
	require.Error(t, err)
	row.WorkerJobID = ""
	reply, err = extractionReceipt(row, owner)
	require.NoError(t, err)
	require.Equal(t, "pending", reply.State)
	require.Empty(t, reply.JobID)
}

func TestExtractionDispatchIDIsScopedToOwner(t *testing.T) {
	attempt := uuid.New()
	require.Equal(t, extractionDispatchID("one", attempt), extractionDispatchID("one", attempt))
	require.NotEqual(t, extractionDispatchID("one", attempt), extractionDispatchID("two", attempt))
}
