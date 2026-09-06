package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/edit"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

// durableEditorGateway keeps the worker boundary deterministic while the test
// exercises the real PostgreSQL repository, transactions, and durable records.
type durableEditorGateway struct {
	statuses     map[string]workerJobStatus
	languages    map[string]edit.EditorLanguageRequest
	download     []byte
	editSubmits  int
	cancelledIDs []string
}

func newDurableEditorGateway(download []byte) *durableEditorGateway {
	return &durableEditorGateway{
		statuses:  map[string]workerJobStatus{},
		languages: map[string]edit.EditorLanguageRequest{},
		download:  download,
	}
}

func (g *durableEditorGateway) workerID() string {
	return "durable-editor-worker-" + uuid.NewString()
}

func (g *durableEditorGateway) SubmitMarkup(context.Context, StudioJobName, string, string, string) (workerJobStatus, error) {
	return workerJobStatus{}, ErrInvalidJob
}

func (g *durableEditorGateway) SubmitEdit(_ context.Context, _ StudioJobName, _, _, _ string) (workerJobStatus, error) {
	id := g.workerID()
	g.editSubmits++
	g.statuses[id] = workerJobStatus{ID: id, Status: "queued"}
	return g.statuses[id], nil
}

func (g *durableEditorGateway) SubmitEditWithLanguage(_ context.Context, _ StudioJobName, _, _, _ string, language edit.EditorLanguageRequest) (workerJobStatus, error) {
	id := g.workerID()
	g.editSubmits++
	g.languages[id] = language
	g.statuses[id] = workerJobStatus{ID: id, Status: "queued"}
	return g.statuses[id], nil
}

func (g *durableEditorGateway) Status(_ context.Context, _ StudioJobName, id string) (workerJobStatus, error) {
	return g.statuses[id], nil
}

func (g *durableEditorGateway) Cancel(_ context.Context, _ StudioJobName, id string) (workerJobStatus, error) {
	g.cancelledIDs = append(g.cancelledIDs, id)
	status := workerJobStatus{ID: id, Status: "cancelled", Progress: 100, Message: "cancelled"}
	g.statuses[id] = status
	return status, nil
}

func (g *durableEditorGateway) Download(context.Context, StudioJobName, string) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(g.download))}, nil
}

type durableEditorMaterializer struct {
	session  *models.StudioSession
	document *models.StudioDocument
	version  *models.StudioVersion
	model    *vdm.DocumentModel
	path     string
}

func (m *durableEditorMaterializer) materialized(versionID uuid.UUID) (*MaterializedVersion, error) {
	if versionID != m.version.ID {
		return nil, ErrInvalidBaseVersion
	}
	return &MaterializedVersion{
		SessionExpiresAt: m.session.ExpiresAt,
		Session:          m.session,
		Document:         m.document,
		Version:          m.version,
		Model:            m.model,
		Path:             m.path,
		Cleanup:          func() {},
	}, nil
}

func (m *durableEditorMaterializer) MaterializeVersion(context.Context, uuid.UUID, identity.Identity) (*MaterializedVersion, error) {
	return m.materialized(m.version.ID)
}

func (m *durableEditorMaterializer) MaterializeVersionByID(_ context.Context, _ uuid.UUID, versionID uuid.UUID, _ identity.Identity) (*MaterializedVersion, error) {
	return m.materialized(versionID)
}

type durableEditorFixture struct {
	repository  Repository
	coordinator StudioJobCoordinator
	gateway     *durableEditorGateway
	identity    identity.Identity
	document    *models.StudioDocument
	session     *models.StudioSession
	baseVersion *models.StudioVersion
}

func newDurableEditorFixture(t *testing.T) durableEditorFixture {
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
	require.Equal(t, 5, pageCount)

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
	return durableEditorFixture{
		repository:  repository,
		coordinator: newJobCoordinatorForGateway(repository, materializer, gateway), gateway: gateway,
		identity: ident, document: document, session: session, baseVersion: version,
	}
}

func durableEditorLayout(languageMode string, languages []string) EditorLayout {
	pages := make([]EditorPage, 5)
	for index := range pages {
		pages[index] = EditorPage{
			PageNum: index + 1, Width: 595, Height: 842, Kind: "text",
			Elements: []EditorElement{},
		}
	}
	pages[0].Elements = []EditorElement{{
		ID: "durable-editor-element-1", Text: "Before", Original: "Before",
		X: 10, Y: 20, Width: 80, Height: 14, Size: 12, Font: "helv",
	}}
	return EditorLayout{
		SchemaVersion: "ocr_v2_editor_layout.v1", OCRV2: true, Success: true,
		Pages: pages, LanguageMode: languageMode, Languages: append([]string(nil), languages...),
	}
}

func rawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func layoutResult(t *testing.T, layout EditorLayout) map[string]any {
	t.Helper()
	var result map[string]any
	require.NoError(t, json.Unmarshal(rawJSON(t, layout), &result))
	return result
}

func compileRequest(t *testing.T, baseVersionID, stateID uuid.UUID, key string, layout json.RawMessage) StudioJobRequest {
	t.Helper()
	return StudioJobRequest{
		BaseVersionID: baseVersionID, IdempotencyKey: key, Operation: StudioJobEditCompile,
		Parameters: rawJSON(t, EditCompileJobParameters{EditorStateID: stateID, Layout: layout}),
	}
}

func TestStudioEditorDurability_PostgreSQL(t *testing.T) {
	fixture := newDurableEditorFixture(t)
	ctx := context.Background()

	languageCases := []struct {
		name      string
		mode      string
		languages []string
	}{
		{name: "English", mode: "EXPLICIT", languages: []string{"eng"}},
		{name: "Sinhala", mode: "EXPLICIT", languages: []string{"sin"}},
		{name: "Tamil", mode: "EXPLICIT", languages: []string{"tam"}},
		{name: "Auto", mode: "AUTO", languages: []string{"eng", "sin", "tam"}},
	}
	states := map[string]*models.StudioEditorState{}
	for _, testCase := range languageCases {
		t.Run(testCase.name, func(t *testing.T) {
			parameters := EditExtractJobParameters{LanguageMode: testCase.mode, Languages: testCase.languages}
			request := StudioJobRequest{
				BaseVersionID:  fixture.baseVersion.ID,
				IdempotencyKey: "durable-language-" + testCase.name + "-" + uuid.NewString(),
				Operation:      StudioJobEditExtract, Parameters: rawJSON(t, parameters),
			}
			submitted, err := fixture.coordinator.Submit(ctx, fixture.session.ID, fixture.identity, request)
			require.NoError(t, err)
			require.False(t, submitted.IsIdempotentReplay)
			assert.JSONEq(t, string(rawJSON(t, parameters)), string(submitted.Job.Parameters))
			assert.Equal(t, edit.EditorLanguageRequest{Mode: testCase.mode, Languages: testCase.languages}, fixture.gateway.languages[submitted.Job.WorkerJobID])

			fixture.gateway.statuses[submitted.Job.WorkerJobID] = workerJobStatus{
				ID: submitted.Job.WorkerJobID, Status: "succeeded", Progress: 100,
				Result: layoutResult(t, durableEditorLayout(testCase.mode, testCase.languages)),
			}
			reconciled, err := fixture.coordinator.Get(ctx, fixture.session.ID, submitted.Job.ID, fixture.identity)
			require.NoError(t, err)
			require.NotNil(t, reconciled.EditorStateID)
			require.NotNil(t, reconciled.ReconciledAt)
			state, err := fixture.coordinator.GetEditorState(ctx, fixture.session.ID, *reconciled.EditorStateID, fixture.identity)
			require.NoError(t, err)
			assert.Equal(t, fixture.baseVersion.ID, state.BaseVersionID)
			assert.Equal(t, submitted.Job.ID, state.ExtractJobID)
			var persisted EditorLayout
			require.NoError(t, json.Unmarshal(state.Layout, &persisted))
			assert.Equal(t, testCase.mode, persisted.LanguageMode)
			assert.Equal(t, testCase.languages, persisted.Languages)
			states[testCase.name] = state

			beforeReplaySubmits := fixture.gateway.editSubmits
			replayed, err := fixture.coordinator.Submit(ctx, fixture.session.ID, fixture.identity, request)
			require.NoError(t, err)
			assert.True(t, replayed.IsIdempotentReplay)
			assert.Equal(t, submitted.Job.ID, replayed.Job.ID)
			assert.Equal(t, beforeReplaySubmits, fixture.gateway.editSubmits)

			conflicting := request
			conflicting.Parameters = rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"eng"}})
			if testCase.name == "English" {
				conflicting.Parameters = rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"sin"}})
			}
			_, err = fixture.coordinator.Submit(ctx, fixture.session.ID, fixture.identity, conflicting)
			assert.ErrorIs(t, err, ErrIdempotencyConflict)
		})
	}

	// A recovery submission keeps its explicit Sinhala intent and can be
	// cancelled without losing the durable job parameters.
	recoveryParams := EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"sin"}}
	recovery, err := fixture.coordinator.Submit(ctx, fixture.session.ID, fixture.identity, StudioJobRequest{
		BaseVersionID: fixture.baseVersion.ID, IdempotencyKey: "durable-language-retry-" + uuid.NewString(),
		Operation: StudioJobEditExtract, Parameters: rawJSON(t, recoveryParams),
	})
	require.NoError(t, err)
	assert.Equal(t, edit.EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"sin"}}, fixture.gateway.languages[recovery.Job.WorkerJobID])
	cancelled, err := fixture.coordinator.Cancel(ctx, fixture.session.ID, recovery.Job.ID, fixture.identity)
	require.NoError(t, err)
	assert.Equal(t, "cancelled", cancelled.Status)
	assert.JSONEq(t, string(rawJSON(t, recoveryParams)), string(cancelled.Parameters))
	assert.Contains(t, fixture.gateway.cancelledIDs, recovery.Job.WorkerJobID)

	baseState := states["English"]
	require.NotNil(t, baseState)
	var edited EditorLayout
	require.NoError(t, json.Unmarshal(baseState.Layout, &edited))
	fontSize, bold, italic, underline, strike := 14.0, true, true, true, true
	edited.Pages[0].Elements[0].Text = "After"
	edited.Pages[0].Elements[0].TargetSubstring = "Before"
	selectionStart, selectionEnd := 0, 6
	edited.Pages[0].Elements[0].SelectionStart = &selectionStart
	edited.Pages[0].Elements[0].SelectionEnd = &selectionEnd
	edited.Pages[0].Elements[0].Style = &EditorElementStyle{
		FontFamily: "cour", FontSize: &fontSize, Bold: &bold, Italic: &italic,
		Underline: &underline, Strikethrough: &strike, Color: "#112233", Background: "#fef08a",
	}
	editedRaw := rawJSON(t, edited)
	compileKey := "durable-editor-compile-" + uuid.NewString()
	compile, err := fixture.coordinator.Submit(ctx, fixture.session.ID, fixture.identity, compileRequest(t, fixture.baseVersion.ID, baseState.ID, compileKey, editedRaw))
	require.NoError(t, err)
	require.NotNil(t, compile.Job.EditorStateID)
	assert.Equal(t, baseState.ID, *compile.Job.EditorStateID)
	assert.Contains(t, string(compile.Job.Parameters), `"fontFamily":"cour"`)
	assert.Contains(t, string(compile.Job.Parameters), `"background":"#fef08a"`)

	fixture.gateway.statuses[compile.Job.WorkerJobID] = workerJobStatus{ID: compile.Job.WorkerJobID, Status: "succeeded", Progress: 100}
	reconciledCompile, err := fixture.coordinator.Get(ctx, fixture.session.ID, compile.Job.ID, fixture.identity)
	require.NoError(t, err)
	require.NotNil(t, reconciledCompile.ResultVersionID)
	require.NotNil(t, reconciledCompile.ReconciledAt)
	resultVersion, err := fixture.repository.GetVersion(ctx, *reconciledCompile.ResultVersionID)
	require.NoError(t, err)
	assert.Equal(t, fixture.baseVersion.ID, *resultVersion.ParentVersionID)
	assert.Equal(t, StudioJobEditCompile, StudioJobName(resultVersion.OperationType))
	assert.True(t, resultVersion.IsMaterialized)
	require.NotNil(t, resultVersion.SnapshotID)
	snapshot, err := fixture.repository.GetSnapshot(ctx, *resultVersion.SnapshotID)
	require.NoError(t, err)
	assert.Equal(t, 5, snapshot.PageCount)
	asset, err := fixture.repository.GetAsset(ctx, snapshot.AssetID)
	require.NoError(t, err)
	assert.Equal(t, "job_result", asset.AssetType)
	assert.True(t, storage.ObjectExists(ctx, asset.R2Key))
	session, err := fixture.repository.GetSession(ctx, fixture.session.ID)
	require.NoError(t, err)
	assert.Equal(t, resultVersion.ID, session.ActiveVersionID)
	versions, operations, err := fixture.repository.GetVersionHistory(ctx, fixture.document.ID)
	require.NoError(t, err)
	assert.Len(t, versions, 2)
	assert.Len(t, operations, 1)
	assert.Equal(t, compileKey, operations[0].IdempotencyKey)
	assert.Equal(t, resultVersion.ID, operations[0].VersionID)

	beforeCompileReplay := fixture.gateway.editSubmits
	compileReplay, err := fixture.coordinator.Submit(ctx, fixture.session.ID, fixture.identity, compileRequest(t, fixture.baseVersion.ID, baseState.ID, compileKey, editedRaw))
	require.NoError(t, err)
	assert.True(t, compileReplay.IsIdempotentReplay)
	assert.Equal(t, compile.Job.ID, compileReplay.Job.ID)
	assert.Equal(t, beforeCompileReplay, fixture.gateway.editSubmits)

	_, err = fixture.coordinator.Submit(ctx, fixture.session.ID, fixture.identity, StudioJobRequest{
		BaseVersionID: fixture.baseVersion.ID, IdempotencyKey: "stale-editor-extract-" + uuid.NewString(),
		Operation:  StudioJobEditExtract,
		Parameters: rawJSON(t, EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"eng"}}),
	})
	assert.ErrorIs(t, err, ErrInvalidBaseVersion)

	// Every invalid edit is submitted through the real durable coordinator
	// against the persisted immutable baseline and rejected before a job row is created.
	invalidLayouts := map[string]json.RawMessage{}
	invalidLayouts["invalid color"] = bytes.Replace(editedRaw, []byte(`"#112233"`), []byte(`"red"`), 1)
	invalidLayouts["unsupported font"] = bytes.Replace(editedRaw, []byte(`"cour"`), []byte(`"comic"`), 1)
	invalidLayouts["invalid selection"] = bytes.Replace(editedRaw, []byte(`"selection_end":6`), []byte(`"selection_end":99`), 1)
	invalidLayouts["changed element id"] = bytes.Replace(editedRaw, []byte(`"durable-editor-element-1"`), []byte(`"changed-element"`), 1)
	invalidLayouts["changed original text"] = bytes.Replace(editedRaw, []byte(`"original_text":"Before"`), []byte(`"original_text":"Other"`), 1)
	invalidLayouts["changed geometry"] = bytes.Replace(editedRaw, []byte(`"x":10`), []byte(`"x":11`), 1)
	invalidLayouts["changed page dimensions"] = bytes.Replace(editedRaw, []byte(`"width":595`), []byte(`"width":596`), 1)
	invalidLayouts["unknown field"] = bytes.Replace(editedRaw, []byte(`"style":{`), []byte(`"unknown":true,"style":{`), 1)
	dropped := edited
	dropped.Pages[0].Elements = []EditorElement{}
	invalidLayouts["changed element count"] = rawJSON(t, dropped)
	for name, invalidLayout := range invalidLayouts {
		t.Run("rejects "+name, func(t *testing.T) {
			_, submitErr := fixture.coordinator.Submit(ctx, fixture.session.ID, fixture.identity, compileRequest(
				t, fixture.baseVersion.ID, baseState.ID, "invalid-"+uuid.NewString(), invalidLayout,
			))
			assert.ErrorIs(t, submitErr, ErrInvalidJob)
		})
	}

	// The persisted immutable extraction baseline was not mutated by valid or
	// rejected compile attempts.
	unchangedState, err := fixture.repository.GetEditorState(ctx, baseState.ID)
	require.NoError(t, err)
	assert.JSONEq(t, string(baseState.Layout), string(unchangedState.Layout))
	assert.WithinDuration(t, baseState.CreatedAt, unchangedState.CreatedAt, time.Millisecond)
}
