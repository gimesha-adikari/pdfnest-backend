package edit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/worker"
)

func TestCompileWordEditsSendsVersionedEnvelopeToDedicatedWorkerRoute(t *testing.T) {
	var received map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/editor/compile-word-edits", r.URL.Path)
		require.Equal(t, http.MethodPost, r.Method)
		require.NoError(t, json.NewDecoder(r.Body).Decode(&received))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"job_id":"worker-job","status":"queued","queue_name":"editor"}`))
	}))
	defer server.Close()

	t.Setenv("PDFNEST_WORKER_URL", server.URL)
	previousClient := worker.Client
	worker.Client = server.Client()
	t.Cleanup(func() { worker.Client = previousClient })

	request := WordEditRequest{
		EditProtocol:    WordEditProtocolV1,
		SourceTracker:   "owned/source/editor_source/original.pdf",
		ExtractionJobID: "extract-01",
		BaselineDigest:  testBaselineDigest,
		Edits: []WordEdit{{
			PageNum: 1, ElementID: "line-1", WordID: "word-2",
			ExpectedOriginalText: "again", ReplacementText: "🧪𠮷",
			StyleOverride: nil, FitPolicy: "preserve_neighbors",
		}},
	}
	submission, err := NewService().(WordEditCompileService).CompileWordEdits(request)
	require.NoError(t, err)
	require.Equal(t, "worker-job", submission.JobID)
	require.Equal(t, map[string]any{
		"edit_protocol":     WordEditProtocolV1,
		"source_tracker":    request.SourceTracker,
		"extraction_job_id": request.ExtractionJobID,
		"baseline_digest":   request.BaselineDigest,
		"edits": []any{map[string]any{
			"page_num":               float64(1),
			"element_id":             "line-1",
			"word_id":                "word-2",
			"expected_original_text": "again",
			"replacement_text":       "🧪𠮷",
			"style_override":         nil,
			"fit_policy":             "preserve_neighbors",
		}},
	}, received)
}
