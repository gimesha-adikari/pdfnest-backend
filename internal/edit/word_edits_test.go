package edit

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
)

const testBaselineDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type wordEditCompileService struct {
	Service
	extraction *WorkerJobRecord
	forwarded  *WordEditRequest
	wordCalls  int
	lineCalls  int
}

func (s *wordEditCompileService) GetJobStatus(string) (*WorkerJobRecord, error) {
	return s.extraction, nil
}

func (s *wordEditCompileService) CompileWordEdits(request WordEditRequest) (*WorkerJobSubmission, error) {
	s.wordCalls++
	s.forwarded = &request
	return &WorkerJobSubmission{JobID: "word-job-1", Status: "queued", QueueName: "editor"}, nil
}

func (s *wordEditCompileService) CompileLayout(string, string, string) (*WorkerJobSubmission, error) {
	s.lineCalls++
	return &WorkerJobSubmission{JobID: "legacy-job-1", Status: "queued", QueueName: "editor"}, nil
}

func makeWordExtractionJob(owner, source string) *WorkerJobRecord {
	return &WorkerJobRecord{
		ID:      "extract-01",
		JobType: "editor_extract",
		Status:  "succeeded",
		Payload: map[string]any{
			"source_key":    source,
			"source_name":   "scan.pdf",
			"consumer":      "general_editor",
			"ocr_v2":        true,
			"ownerIdentity": owner,
		},
		Result: map[string]any{
			"schema_version": "ocr_v2_editor_layout.v1",
			"word_edit_baseline": map[string]any{
				"extraction_job_id":  "extract-01",
				"digest":             testBaselineDigest,
				"source_sha256":      "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
				"engine_fingerprint": "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
				"algorithm_version":  "general_editor_word_baseline.v1",
			},
			"pages": []any{
				map[string]any{
					"page_num": float64(1),
					"elements": []any{
						map[string]any{
							"id":       "line-1",
							"word_ids": []any{"word-1", "word-2", "word-3"},
							"word_geometry": []any{
								map[string]any{"id": "word-1", "text": "again"},
								map[string]any{"id": "word-2", "text": "and"},
								map[string]any{"id": "word-3", "text": "again"},
							},
						},
					},
				},
			},
		},
	}
}

func newWordCompileApp(service *wordEditCompileService) *fiber.App {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: c.Get("X-Owner"), Type: identity.TypeGuest})
		return c.Next()
	})
	app.Post("/compile", NewController(service).HandleCompilePDF)
	return app
}

func TestWordCompileValidatesOwnedBaselineAndForwardsExactUnicodeSnapshot(t *testing.T) {
	const owner = "editor-owner"
	source := storage.NewOwnedKey(owner, "editor_source", ".pdf")
	service := &wordEditCompileService{extraction: makeWordExtractionJob(owner, source)}
	app := newWordCompileApp(service)
	body := []byte(`{"edit_protocol":"platen_word_edits.v1","source_tracker":"` + source + `","extraction_job_id":"extract-01","baseline_digest":"` + testBaselineDigest + `","edits":[{"page_num":1,"element_id":"line-1","word_id":"word-3","expected_original_text":"again","replacement_text":"🧪𠮷","style_override":null,"fit_policy":"preserve_neighbors"},{"page_num":1,"element_id":"line-1","word_id":"word-2","expected_original_text":"and","replacement_text":"","style_override":null,"fit_policy":"preserve_neighbors"}]}`)
	req := httptest.NewRequest("POST", "/compile", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Owner", owner)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 202, resp.StatusCode)
	require.Equal(t, 1, service.wordCalls)
	require.Zero(t, service.lineCalls)
	require.Equal(t, "platen_word_edits.v1", service.forwarded.EditProtocol)
	require.Equal(t, "extract-01", service.forwarded.ExtractionJobID)
	require.Equal(t, source, service.forwarded.SourceTracker)
	require.Equal(t, "🧪𠮷", service.forwarded.Edits[0].ReplacementText)
	require.Equal(t, "", service.forwarded.Edits[1].ReplacementText)
	require.Equal(t, "word-3", service.forwarded.Edits[0].WordID, "repeated words remain anchored by their distinct IDs")
}

func TestWordCompileRejectsUnknownProtocolDuplicatesStaleTextAndForeignBaseline(t *testing.T) {
	const owner = "editor-owner"
	source := storage.NewOwnedKey(owner, "editor_source", ".pdf")
	base := map[string]any{
		"edit_protocol":     "platen_word_edits.v1",
		"source_tracker":    source,
		"extraction_job_id": "extract-01",
		"baseline_digest":   testBaselineDigest,
		"edits":             []any{map[string]any{"page_num": 1, "element_id": "line-1", "word_id": "word-1", "expected_original_text": "again", "replacement_text": "once", "style_override": nil, "fit_policy": "preserve_neighbors"}},
	}
	cases := []struct {
		name       string
		mutateBody func(map[string]any)
		mutateJob  func(*WorkerJobRecord)
		owner      string
		wantStatus int
	}{
		{name: "unknown version", mutateBody: func(body map[string]any) { body["edit_protocol"] = "platen_word_edits.v99" }, owner: owner, wantStatus: 400},
		{name: "missing discriminator with word fields", mutateBody: func(body map[string]any) { delete(body, "edit_protocol") }, owner: owner, wantStatus: 400},
		{name: "duplicate target", mutateBody: func(body map[string]any) { body["edits"] = append(body["edits"].([]any), body["edits"].([]any)[0]) }, owner: owner, wantStatus: 400},
		{name: "stale expected text", mutateBody: func(body map[string]any) {
			body["edits"].([]any)[0].(map[string]any)["expected_original_text"] = "stale"
		}, owner: owner, wantStatus: 409},
		{name: "wrong page", mutateBody: func(body map[string]any) {
			body["edits"].([]any)[0].(map[string]any)["page_num"] = float64(2)
		}, owner: owner, wantStatus: 409},
		{name: "wrong element", mutateBody: func(body map[string]any) {
			body["edits"].([]any)[0].(map[string]any)["element_id"] = "line-other"
		}, owner: owner, wantStatus: 409},
		{name: "wrong word", mutateBody: func(body map[string]any) {
			body["edits"].([]any)[0].(map[string]any)["word_id"] = "word-missing"
		}, owner: owner, wantStatus: 409},
		{name: "unsupported fit policy", mutateBody: func(body map[string]any) {
			body["edits"].([]any)[0].(map[string]any)["fit_policy"] = "replace_line"
		}, owner: owner, wantStatus: 400},
		{name: "foreign source", mutateBody: func(body map[string]any) {
			body["source_tracker"] = storage.NewOwnedKey("foreign", "editor_source", ".pdf")
		}, owner: owner, wantStatus: 403},
		{name: "foreign extraction source", mutateJob: func(job *WorkerJobRecord) {
			job.Payload["source_key"] = storage.NewOwnedKey("other-source", "editor_source", ".pdf")
		}, owner: owner, wantStatus: 409},
		{name: "stale digest", mutateBody: func(body map[string]any) { body["baseline_digest"] = "sha256:" + string(bytes.Repeat([]byte("d"), 64)) }, owner: owner, wantStatus: 409},
		{name: "wrong extraction job type", mutateJob: func(job *WorkerJobRecord) { job.JobType = "editor_compile" }, owner: owner, wantStatus: 409},
		{name: "expired extraction baseline", mutateJob: func(job *WorkerJobRecord) { job.Status = "failed" }, owner: owner, wantStatus: 409},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bodyMap := map[string]any{}
			require.NoError(t, json.Unmarshal(mustJSON(t, base), &bodyMap))
			if tc.mutateBody != nil {
				tc.mutateBody(bodyMap)
			}
			job := makeWordExtractionJob(owner, source)
			if tc.mutateJob != nil {
				tc.mutateJob(job)
			}
			service := &wordEditCompileService{extraction: job}
			app := newWordCompileApp(service)
			req := httptest.NewRequest("POST", "/compile", bytes.NewReader(mustJSON(t, bodyMap)))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Owner", tc.owner)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.wantStatus, resp.StatusCode)
			require.Zero(t, service.wordCalls)
			require.Zero(t, service.lineCalls)
		})
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
