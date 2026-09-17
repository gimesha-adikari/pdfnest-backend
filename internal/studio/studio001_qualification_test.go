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

	// Verify querying the fast-path job via coordinator.Get (used by frontend polling / status checks)
	queriedJob, err := coordinator.Get(ctx, session.ID, reopenV2Res.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, queriedJob)
	assert.Equal(t, "editor_extract", queriedJob.JobType, "Fastpath job type must be editor_extract")
	assert.Equal(t, "succeeded", queriedJob.Status)
	assert.Equal(t, 100, queriedJob.Progress)
	require.NotNil(t, queriedJob.EditorStateID)
	assert.Equal(t, v2State.ID, *queriedJob.EditorStateID)

	// -----------------------------------------------------------------------
	// SCENARIO 7: Explicit different language on the AUTO-compiled version (v2 from Scenario 5)
	// User explicitly selects "sin" (Sinhala).
	// Must NOT return cached compiled state; must submit a fresh worker extraction job!
	// -----------------------------------------------------------------------
	// Update materializer to point to the active compiled version (v2)
	resultVersion, err := repository.GetVersion(ctx, v2VersionID)
	require.NoError(t, err)
	materializer.version = resultVersion

	submitsBeforeExplicitLangV2 := gateway.editSubmits
	explicitV2Req := StudioJobRequest{
		BaseVersionID:  v2VersionID,
		IdempotencyKey: "extract-v2-sin-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"sin"}}),
	}
	explicitV2Res, err := coordinator.Submit(ctx, session.ID, ident, explicitV2Req)
	require.NoError(t, err)
	assert.Equal(t, "queued", explicitV2Res.Job.Status, "Explicit different language on compiled version must queue fresh worker job")
	assert.Equal(t, "editor_extract", explicitV2Res.Job.JobType)
	assert.Equal(t, submitsBeforeExplicitLangV2+1, gateway.editSubmits, "Worker submit count must increment exactly once")
	assert.Equal(t, edit.EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"sin"}}, gateway.languages[explicitV2Res.Job.WorkerJobID])
}

// TestStudio001_Qualification_HistoricalExplicitEnglishPath verifies the EXACT historical
// failure sequence from production acceptance 2026-09-13 (STUDIO-001):
//
//	AUTO uncertain OCR → manual ENGLISH selection → compile → reopen with ENGLISH
//
// Expected: reopen must reuse the compiled editor state (zero new OCR worker jobs),
// NOT re-run OCR from scratch as was observed in production.
//
// Also verifies multi-generation compile provenance:
//
//	EXPLICIT ENG (V1) → compile V2 → compile V3 → reopen V3 EXPLICIT ENG → REUSE
//	EXPLICIT ENG (V1) → compile V2 → compile V3 → reopen V3 EXPLICIT SIN → FRESH OCR
func TestStudio001_Qualification_HistoricalExplicitEnglishPath(t *testing.T) {
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

	ident := identity.Identity{ID: "studio001-hist-" + uuid.NewString(), Type: identity.TypeGuest}
	assetID := "studio001-hist-source-" + uuid.NewString()
	pages := make([]vdm.PageDescriptor, pageCount)
	for index := range pages {
		pages[index] = vdm.PageDescriptor{
			PageID:           "studio001-hist-page-" + uuid.NewString(),
			SourceAssetID:    &assetID,
			SourcePageNumber: index + 1,
			Dimensions:       &vdm.Dimensions{Width: 595, Height: 842},
			Overlays:         []vdm.Overlay{},
		}
	}
	baseModel := vdm.DocumentModel{DocumentID: "studio001-hist-doc", PageCount: pageCount, Pages: pages}
	document, session, version, err := service.CreateDocument(
		context.Background(), ident, "studio001-hist.pdf", int64(len(fixtureBytes)), pageCount,
		assetID, "test-only/studio001-hist-source.pdf", baseModel,
	)
	require.NoError(t, err)

	gateway := newDurableEditorGateway(fixtureBytes)
	materializer := &durableEditorMaterializer{
		session: session, document: document, version: version, model: &baseModel, path: fixturePath,
	}
	coordinator := newJobCoordinatorForGateway(repository, materializer, gateway)
	ctx := context.Background()

	// -----------------------------------------------------------------------
	// STEP 1: Extract with EXPLICIT ENGLISH (the manual selection step in production)
	// -----------------------------------------------------------------------
	extractEngReq := StudioJobRequest{
		BaseVersionID:  version.ID,
		IdempotencyKey: "hist-extract-eng-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"eng"}}),
	}
	extractEngRes, err := coordinator.Submit(ctx, session.ID, ident, extractEngReq)
	require.NoError(t, err)
	assert.Equal(t, "queued", extractEngRes.Job.Status)
	assert.Equal(t, 1, gateway.editSubmits, "Initial English extraction must submit worker job")

	gateway.statuses[extractEngRes.Job.WorkerJobID] = workerJobStatus{
		ID: extractEngRes.Job.WorkerJobID, Status: "succeeded", Progress: 100,
		Result: layoutResult(t, durableEditorLayout("EXPLICIT", []string{"eng"})),
	}
	reconciledEng, err := coordinator.Get(ctx, session.ID, extractEngRes.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, reconciledEng.EditorStateID)
	engStateID := *reconciledEng.EditorStateID

	engState, err := repository.GetEditorState(ctx, engStateID)
	require.NoError(t, err)

	// -----------------------------------------------------------------------
	// STEP 2: Compile V1 from the English extraction state (as occurred in production)
	// -----------------------------------------------------------------------
	var engLayout EditorLayout
	require.NoError(t, json.Unmarshal(engState.Layout, &engLayout))
	engLayout.Pages[0].Elements[0].Text = "Compiled English QA"
	engCompiledLayout := rawJSON(t, engLayout)

	compile, err := coordinator.Submit(ctx, session.ID, ident, compileRequest(t, version.ID, engStateID, "hist-compile-"+uuid.NewString(), engCompiledLayout))
	require.NoError(t, err)
	gateway.statuses[compile.Job.WorkerJobID] = workerJobStatus{ID: compile.Job.WorkerJobID, Status: "succeeded", Progress: 100}
	reconciledCompile, err := coordinator.Get(ctx, session.ID, compile.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, reconciledCompile.ResultVersionID)
	v2VersionID := *reconciledCompile.ResultVersionID

	// Update materializer to the compiled version
	v2Version, err := repository.GetVersion(ctx, v2VersionID)
	require.NoError(t, err)
	materializer.version = v2Version
	session.ActiveVersionID = v2VersionID // advance session active for extract check

	v2State, err := repository.GetLatestEditorStateForVersion(ctx, session.ID, v2VersionID)
	require.NoError(t, err)
	require.NotNil(t, v2State, "Compiled version must have StudioEditorState persisted")

	// -----------------------------------------------------------------------
	// STEP 3: HISTORICAL PATH — Reopen compiled version with EXPLICIT ENGLISH
	// Must REUSE immediately with ZERO new OCR worker submissions.
	// This is the exact failure observed in production on 2026-09-13.
	// -----------------------------------------------------------------------
	submitsBeforeReopen := gateway.editSubmits
	reopenEngReq := StudioJobRequest{
		BaseVersionID:  v2VersionID,
		IdempotencyKey: "hist-reopen-eng-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"eng"}}),
	}
	reopenEngRes, err := coordinator.Submit(ctx, session.ID, ident, reopenEngReq)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", reopenEngRes.Job.Status,
		"HISTORICAL PATH: reopen compiled (ENG source) version with EXPLICIT ENG must reuse immediately — this was the production defect")
	assert.Equal(t, 100, reopenEngRes.Job.Progress)
	require.NotNil(t, reopenEngRes.Job.EditorStateID)
	assert.Equal(t, v2State.ID, *reopenEngRes.Job.EditorStateID,
		"Must reuse the compiled English editor state, not create a new one")
	assert.Equal(t, submitsBeforeReopen, gateway.editSubmits,
		"HISTORICAL PATH: zero new worker OCR submissions — was re-extracting from scratch in production")

	// Verify end-to-end API: coordinator.Get and GetEditorState must resolve cleanly
	queriedJob, err := coordinator.Get(ctx, session.ID, reopenEngRes.Job.ID, ident)
	require.NoError(t, err)
	assert.Equal(t, "editor_extract", queriedJob.JobType)
	assert.Equal(t, "succeeded", queriedJob.Status)

	loadedState, err := coordinator.GetEditorState(ctx, session.ID, *reopenEngRes.Job.EditorStateID, ident)
	require.NoError(t, err)
	require.NotNil(t, loadedState)
	assert.JSONEq(t, string(engCompiledLayout), string(loadedState.Layout))

	// -----------------------------------------------------------------------
	// STEP 3b: Reopen same compiled version with EXPLICIT SINHALA — must trigger fresh OCR
	// -----------------------------------------------------------------------
	submitsBeforeReOpenSin := gateway.editSubmits
	reopenSinReq := StudioJobRequest{
		BaseVersionID:  v2VersionID,
		IdempotencyKey: "hist-reopen-sin-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"sin"}}),
	}
	reopenSinRes, err := coordinator.Submit(ctx, session.ID, ident, reopenSinReq)
	require.NoError(t, err)
	assert.Equal(t, "queued", reopenSinRes.Job.Status,
		"Different explicit language on ENG-compiled version must queue fresh OCR")
	assert.Equal(t, submitsBeforeReOpenSin+1, gateway.editSubmits,
		"Exactly one new worker submission on explicit language change to SIN")
	assert.Equal(t, edit.EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"sin"}},
		gateway.languages[reopenSinRes.Job.WorkerJobID])

	// -----------------------------------------------------------------------
	// STEP 4: MULTI-GENERATION — Compile V2 → V3, reopen V3 with ENGLISH
	// Provenance must survive through 2 compile generations.
	// -----------------------------------------------------------------------
	v3Layout := rawJSON(t, engLayout) // reuse same layout
	v3Compile, err := coordinator.Submit(ctx, session.ID, ident, compileRequest(t, v2VersionID, v2State.ID, "hist-compile-v3-"+uuid.NewString(), v3Layout))
	require.NoError(t, err)
	gateway.statuses[v3Compile.Job.WorkerJobID] = workerJobStatus{ID: v3Compile.Job.WorkerJobID, Status: "succeeded", Progress: 100}
	reconciledV3, err := coordinator.Get(ctx, session.ID, v3Compile.Job.ID, ident)
	require.NoError(t, err)
	require.NotNil(t, reconciledV3.ResultVersionID)
	v3VersionID := *reconciledV3.ResultVersionID

	v3Version, err := repository.GetVersion(ctx, v3VersionID)
	require.NoError(t, err)
	materializer.version = v3Version
	session.ActiveVersionID = v3VersionID

	v3State, err := repository.GetLatestEditorStateForVersion(ctx, session.ID, v3VersionID)
	require.NoError(t, err)
	require.NotNil(t, v3State, "Multi-gen V3 must have StudioEditorState")

	// Reopen V3 with EXPLICIT ENG — provenance traced through 2 compile generations → REUSE
	submitsBeforeV3EngReopen := gateway.editSubmits
	reopenV3EngReq := StudioJobRequest{
		BaseVersionID:  v3VersionID,
		IdempotencyKey: "hist-reopen-v3-eng-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"eng"}}),
	}
	reopenV3EngRes, err := coordinator.Submit(ctx, session.ID, ident, reopenV3EngReq)
	require.NoError(t, err)
	assert.Equal(t, "succeeded", reopenV3EngRes.Job.Status,
		"Multi-gen: reopen V3 (ENG→V2 compile→V3 compile) with EXPLICIT ENG must reuse (provenance preserved)")
	assert.Equal(t, submitsBeforeV3EngReopen, gateway.editSubmits,
		"Multi-gen: zero new OCR worker submissions for same-language V3 reopen")
	require.NotNil(t, reopenV3EngRes.Job.EditorStateID)
	assert.Equal(t, v3State.ID, *reopenV3EngRes.Job.EditorStateID)

	// Reopen V3 with EXPLICIT SIN — fresh OCR
	submitsBeforeV3SinReopen := gateway.editSubmits
	reopenV3SinReq := StudioJobRequest{
		BaseVersionID:  v3VersionID,
		IdempotencyKey: "hist-reopen-v3-sin-" + uuid.NewString(),
		Operation:      StudioJobEditExtract,
		Parameters:     rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"sin"}}),
	}
	reopenV3SinRes, err := coordinator.Submit(ctx, session.ID, ident, reopenV3SinReq)
	require.NoError(t, err)
	assert.Equal(t, "queued", reopenV3SinRes.Job.Status,
		"Multi-gen: different explicit language on V3 must trigger fresh OCR")
	assert.Equal(t, submitsBeforeV3SinReopen+1, gateway.editSubmits,
		"Multi-gen: exactly one new worker submission on explicit language change for V3")
	assert.Equal(t, edit.EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"sin"}},
		gateway.languages[reopenV3SinRes.Job.WorkerJobID])
}
