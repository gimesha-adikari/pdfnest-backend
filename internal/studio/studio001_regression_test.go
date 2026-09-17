package studio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/vdm"
)

func TestStudio001_Regression_ExtractionReusedOnReopenAndAfterCompile(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())

	service, repository := getTestServiceAndRepository(t)
	fixturePath := filepath.Join("..", "..", "..", "benchmarks", "fixtures", "small_text.pdf")
	fixtureBytes, err := os.ReadFile(fixturePath)
	require.NoError(t, err)
	pageCount, err := validateMaterializedOutput(fixturePath)
	require.NoError(t, err)

	ident := identity.Identity{ID: "durable-editor-" + uuid.NewString(), Type: identity.TypeGuest}
	assetID := "durable-editor-source-" + uuid.NewString()
	pages := make([]vdm.PageDescriptor, pageCount)
	for index := range pages {
		pages[index] = vdm.PageDescriptor{
			PageID:           "durable-editor-page-" + uuid.NewString(),
			SourceAssetID:    &assetID,
			SourcePageNumber: index + 1,
			Dimensions:       &vdm.Dimensions{Width: 595, Height: 842},
			Overlays:         []vdm.Overlay{},
		}
	}
	baseModel := vdm.DocumentModel{DocumentID: "durable-editor-document", PageCount: pageCount, Pages: pages}
	document, session, version, err := service.CreateDocument(
		context.Background(), ident, "durable-editor.pdf", int64(len(fixtureBytes)), pageCount,
		assetID, "test-only/durable-editor-source.pdf", baseModel,
	)
	require.NoError(t, err)

	gateway := newDurableEditorGateway(fixtureBytes)
	materializer := &durableEditorMaterializer{
		session: session, document: document, version: version, model: &baseModel, path: fixturePath,
	}
	coordinator := newJobCoordinatorForGateway(repository, materializer, gateway)
	ctx := context.Background()

	// 1. Initial extraction on baseVersion with AUTO mode
	extractParams := EditExtractJobParameters{LanguageMode: "AUTO", Languages: []string{"eng", "sin", "tam"}}
	extractReq := StudioJobRequest{
		BaseVersionID:  version.ID,
		IdempotencyKey: "extract-v1-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, extractParams),
	}
	extractRes, err := coordinator.Submit(ctx, session.ID, ident, extractReq)
	require.NoError(t, err)
	require.False(t, extractRes.IsIdempotentReplay)
	require.Equal(t, 1, gateway.editSubmits)

	// Simulate worker completing extraction
	gateway.statuses[extractRes.Job.WorkerJobID] = workerJobStatus{
		ID:       extractRes.Job.WorkerJobID,
		Status:   "succeeded",
		Progress: 100,
		Result:   layoutResult(t, durableEditorLayout("AUTO", []string{"eng", "sin", "tam"})),
	}
	reconciledExtract, err := coordinator.Get(ctx, session.ID, extractRes.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, reconciledExtract.EditorStateID)

	baseState, err := coordinator.GetEditorState(ctx, session.ID, *reconciledExtract.EditorStateID, ident)
	require.NoError(t, err)
	require.NotNil(t, baseState)

	// 2. Reopening editor on baseVersion: must reuse existing state immediately
	reopenReq := StudioJobRequest{
		BaseVersionID:  version.ID,
		IdempotencyKey: "reopen-v1-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "AUTO", Languages: []string{"eng", "sin", "tam"}}),
	}
	reopenRes, err := coordinator.Submit(ctx, session.ID, ident, reopenReq)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", reopenRes.Job.Status, "Reopen must reuse existing state immediately")
	assert.Equal(t, 1, gateway.editSubmits, "No new worker submission on reopen")

	// 3. Compile v1 to create v2
	var edited EditorLayout
	require.NoError(t, json.Unmarshal(baseState.Layout, &edited))
	edited.Pages[0].Elements[0].Text = "Compiled Edit"
	editedRaw := rawJSON(t, edited)
	compileKey := "compile-v1-" + uuid.NewString()
	compile, err := coordinator.Submit(ctx, session.ID, ident, compileRequest(t, version.ID, baseState.ID, compileKey, editedRaw))
	require.NoError(t, err)

	gateway.statuses[compile.Job.WorkerJobID] = workerJobStatus{ID: compile.Job.WorkerJobID, Status: "succeeded", Progress: 100}
	reconciledCompile, err := coordinator.Get(ctx, session.ID, compile.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, reconciledCompile.ResultVersionID)
	resultVersionID := *reconciledCompile.ResultVersionID

	// 4. Verify v2 has StudioEditorState created by compile reconciliation
	v2State, err := repository.GetLatestEditorStateForVersion(ctx, session.ID, resultVersionID)
	require.NoError(t, err)
	require.NotNil(t, v2State, "Compile reconciliation must create EditorState for resultVersionID")
	assert.Equal(t, compile.Job.ID, v2State.ExtractJobID)

	// Update materializer to point to the active compiled version
	resultVersion, err := repository.GetVersion(ctx, resultVersionID)
	require.NoError(t, err)
	materializer.version = resultVersion

	// 5. Reopening editor on active compiled version (v2): must reuse v2's state immediately!
	extractV2Req := StudioJobRequest{
		BaseVersionID:  resultVersionID,
		IdempotencyKey: "extract-v2-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "AUTO", Languages: []string{"eng", "sin", "tam"}}),
	}
	extractV2Res, err := coordinator.Submit(ctx, session.ID, ident, extractV2Req)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", extractV2Res.Job.Status, "Compiled version reopen must reuse state immediately")
	assert.Equal(t, v2State.ID, *extractV2Res.Job.EditorStateID, "Must point to v2 state")
	assert.Equal(t, 2, gateway.editSubmits, "Total worker submits must remain 2 (1 extract + 1 compile)")
}
