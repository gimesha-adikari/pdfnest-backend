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

	"pdfnest-backend/internal/edit"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/studio/vdm"
)

func TestStudio001_Qualification_SameSessionReuseAndCompiledVersionState(t *testing.T) {
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

	ident := identity.Identity{ID: "studio001-qual-" + uuid.NewString(), Type: identity.TypeGuest}
	assetID := "studio001-qual-source-" + uuid.NewString()
	pages := make([]vdm.PageDescriptor, pageCount)
	for index := range pages {
		pages[index] = vdm.PageDescriptor{
			PageID:           "studio001-qual-page-" + uuid.NewString(),
			SourceAssetID:    &assetID,
			SourcePageNumber: index + 1,
			Dimensions:       &vdm.Dimensions{Width: 595, Height: 842},
			Overlays:         []vdm.Overlay{},
		}
	}
	baseModel := vdm.DocumentModel{DocumentID: "studio001-qual-doc", PageCount: pageCount, Pages: pages}
	document, session, version, err := service.CreateDocument(
		context.Background(), ident, "studio001-qual.pdf", int64(len(fixtureBytes)), pageCount,
		assetID, "test-only/studio001-qual-source.pdf", baseModel,
	)
	require.NoError(t, err)

	gateway := newDurableEditorGateway(fixtureBytes)
	materializer := &durableEditorMaterializer{
		session: session, document: document, version: version, model: &baseModel, path: fixturePath,
	}
	coordinator := newJobCoordinatorForGateway(repository, materializer, gateway)
	ctx := context.Background()

	// -------------------------------------------------------------
	// SCENARIO 1: First extraction with AUTO mode (default on upload)
	// -------------------------------------------------------------
	extractParamsAuto := EditExtractJobParameters{LanguageMode: "AUTO", Languages: []string{"eng", "sin", "tam"}}
	extractReqAuto := StudioJobRequest{
		BaseVersionID:  version.ID,
		IdempotencyKey: "extract-v1-auto-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, extractParamsAuto),
	}
	extractResAuto, err := coordinator.Submit(ctx, session.ID, ident, extractReqAuto)
	require.NoError(t, err)
	require.False(t, extractResAuto.IsIdempotentReplay)
	require.Equal(t, 1, gateway.editSubmits, "Initial extraction must submit worker job")

	// Complete initial extraction
	gateway.statuses[extractResAuto.Job.WorkerJobID] = workerJobStatus{
		ID:       extractResAuto.Job.WorkerJobID,
		Status:   "succeeded",
		Progress: 100,
		Result:   layoutResult(t, durableEditorLayout("AUTO", []string{"eng", "sin", "tam"})),
	}
	reconciledExtract, err := coordinator.Get(ctx, session.ID, extractResAuto.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, reconciledExtract.EditorStateID)
	initialStateID := *reconciledExtract.EditorStateID

	baseState, err := coordinator.GetEditorState(ctx, session.ID, initialStateID, ident)
	require.NoError(t, err)
	require.NotNil(t, baseState)

	// -------------------------------------------------------------
	// SCENARIO 2: Reopening same session on version 1 (AUTO mode)
	// Must REUSE existing state immediately without submitting worker job!
	// -------------------------------------------------------------
	submitsBeforeReopen := gateway.editSubmits
	reopenReqAuto := StudioJobRequest{
		BaseVersionID:  version.ID,
		IdempotencyKey: "reopen-v1-auto-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "AUTO", Languages: []string{"eng", "sin", "tam"}}),
	}
	reopenResAuto, err := coordinator.Submit(ctx, session.ID, ident, reopenReqAuto)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", reopenResAuto.Job.Status, "Reopen must return immediate succeeded status")
	assert.Equal(t, 100, reopenResAuto.Job.Progress)
	require.NotNil(t, reopenResAuto.Job.EditorStateID)
	assert.Equal(t, initialStateID, *reopenResAuto.Job.EditorStateID, "Reopen must reference existing EditorStateID")
	assert.Equal(t, submitsBeforeReopen, gateway.editSubmits, "Worker submit count must NOT increment on reopen")

	// -------------------------------------------------------------
	// SCENARIO 3: User explicitly selects English (after uncertain auto)
	// Submits worker job for English
	// -------------------------------------------------------------
	extractParamsEng := EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"eng"}}
	extractReqEng := StudioJobRequest{
		BaseVersionID:  version.ID,
		IdempotencyKey: "extract-v1-eng-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, extractParamsEng),
	}
	extractResEng, err := coordinator.Submit(ctx, session.ID, ident, extractReqEng)
	require.NoError(t, err)
	assert.Equal(t, "queued", extractResEng.Job.Status)
	assert.Equal(t, submitsBeforeReopen+1, gateway.editSubmits)

	gateway.statuses[extractResEng.Job.WorkerJobID] = workerJobStatus{
		ID:       extractResEng.Job.WorkerJobID,
		Status:   "succeeded",
		Progress: 100,
		Result:   layoutResult(t, durableEditorLayout("EXPLICIT", []string{"eng"})),
	}
	reconciledEng, err := coordinator.Get(ctx, session.ID, extractResEng.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, reconciledEng.EditorStateID)
	engStateID := *reconciledEng.EditorStateID

	// Reopening with English: MUST REUSE immediately!
	reopenReqEng := StudioJobRequest{
		BaseVersionID:  version.ID,
		IdempotencyKey: "reopen-v1-eng-again-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, extractParamsEng),
	}
	reopenResEng, err := coordinator.Submit(ctx, session.ID, ident, reopenReqEng)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", reopenResEng.Job.Status)
	assert.Equal(t, engStateID, *reopenResEng.Job.EditorStateID)
	assert.Equal(t, submitsBeforeReopen+1, gateway.editSubmits, "No new worker submission on English reopen")

	// -------------------------------------------------------------
	// SCENARIO 4: User explicitly changes language to Sinhala
	// Must NOT reuse English extraction; must submit new extraction to worker!
	// -------------------------------------------------------------
	changeLangReq := StudioJobRequest{
		BaseVersionID:  version.ID,
		IdempotencyKey: "extract-v1-sin-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"sin"}}),
	}
	changeLangRes, err := coordinator.Submit(ctx, session.ID, ident, changeLangReq)
	require.NoError(t, err)
	assert.Equal(t, "queued", changeLangRes.Job.Status, "Language change must queue new worker extraction")
	assert.Equal(t, submitsBeforeReopen+2, gateway.editSubmits, "Worker submits must increment on explicit language change")
	assert.Equal(t, edit.EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"sin"}}, gateway.languages[changeLangRes.Job.WorkerJobID])

	// -------------------------------------------------------------
	// SCENARIO 5: Compile version 1 to create version 2
	// Finalization MUST create a new StudioEditorState for version 2!
	// -------------------------------------------------------------
	var edited EditorLayout
	require.NoError(t, json.Unmarshal(baseState.Layout, &edited))
	edited.Pages[0].Elements[0].Text = "Compiled Text QA"
	editedRaw := rawJSON(t, edited)
	compileKey := "compile-v1-" + uuid.NewString()
	compile, err := coordinator.Submit(ctx, session.ID, ident, compileRequest(t, version.ID, baseState.ID, compileKey, editedRaw))
	require.NoError(t, err)

	gateway.statuses[compile.Job.WorkerJobID] = workerJobStatus{ID: compile.Job.WorkerJobID, Status: "succeeded", Progress: 100}
	reconciledCompile, err := coordinator.Get(ctx, session.ID, compile.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, reconciledCompile.ResultVersionID)
	v2VersionID := *reconciledCompile.ResultVersionID

	// Verify StudioEditorState was created for v2VersionID
	v2State, err := repository.GetLatestEditorStateForVersion(ctx, session.ID, v2VersionID)
	require.NoError(t, err)
	require.NotNil(t, v2State, "Compiled version must have StudioEditorState persisted upon finalization")
	assert.Equal(t, v2VersionID, v2State.BaseVersionID)
	assert.Equal(t, compile.Job.ID, v2State.ExtractJobID)
	assert.JSONEq(t, string(editedRaw), string(v2State.Layout), "Persisted layout must match compiled layout")

	// -------------------------------------------------------------
	// SCENARIO 6: Reopening Editor V2 on active compiled version (v2)
	// Must REUSE v2's persisted compiled layout IMMEDIATELY with ZERO worker jobs!
	// -------------------------------------------------------------
	submitsBeforeV2Extract := gateway.editSubmits
	reopenV2Req := StudioJobRequest{
		BaseVersionID:  v2VersionID,
		IdempotencyKey: "reopen-v2-auto-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "AUTO", Languages: []string{"eng", "sin", "tam"}}),
	}
	reopenV2Res, err := coordinator.Submit(ctx, session.ID, ident, reopenV2Req)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", reopenV2Res.Job.Status, "Reopen on compiled version must return immediate succeeded status")
	assert.Equal(t, 100, reopenV2Res.Job.Progress)
	require.NotNil(t, reopenV2Res.Job.EditorStateID)
	assert.Equal(t, v2State.ID, *reopenV2Res.Job.EditorStateID, "Must reuse compiled version's EditorStateID")
	assert.Equal(t, submitsBeforeV2Extract, gateway.editSubmits, "No worker jobs submitted for compiled version reopen!")

	// Load editor state via coordinator to verify end-to-end API accessibility
	loadedV2State, err := coordinator.GetEditorState(ctx, session.ID, *reopenV2Res.Job.EditorStateID, ident)
	require.NoError(t, err)
	require.NotNil(t, loadedV2State)
	assert.JSONEq(t, string(editedRaw), string(loadedV2State.Layout))
}
