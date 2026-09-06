package edit

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/worker"
)

func TestEditorV2RequestsCarryConsumerBoundary(t *testing.T) {
	var payloads []map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		payloads = append(payloads, payload)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"job_id":"job-1","status":"queued","queue_name":"editor"}`))
	}))
	defer server.Close()

	originalClient := worker.Client
	worker.Client = server.Client()
	defer func() { worker.Client = originalClient }()
	t.Setenv("PDFNEST_WORKER_URL", server.URL)

	svc := &service{}
	_, err := svc.ExtractLayoutForLegacyEditor("jobs/legacy-editor/source.pdf", "", "source.pdf")
	require.NoError(t, err)
	_, err = svc.ExtractLayoutV2("jobs/studio/source.pdf", "", "source.pdf")
	require.NoError(t, err)
	_, err = svc.ExtractLayoutV2ForGeneralEditor("jobs/editor/source.pdf", "", "source.pdf")
	require.NoError(t, err)
	_, err = svc.ExtractLayoutV2ForGeneralEditorWithLanguage("jobs/editor/sinhala.pdf", "", "sinhala.pdf", EditorLanguageRequest{Mode: "EXPLICIT", Languages: []string{"sin"}})
	require.NoError(t, err)
	_, err = svc.ExtractLayoutV2WithLanguage("jobs/studio/auto.pdf", "", "auto.pdf", EditorLanguageRequest{Mode: "AUTO", Languages: []string{"eng", "sin", "tam"}})
	require.NoError(t, err)

	require.Len(t, payloads, 5)
	require.NotContains(t, payloads[0], "ocr_v2")
	require.Equal(t, "legacy_editor", payloads[0]["consumer"])
	require.Equal(t, true, payloads[1]["ocr_v2"])
	require.Equal(t, "studio", payloads[1]["consumer"])
	require.Equal(t, true, payloads[2]["ocr_v2"])
	require.Equal(t, "general_editor", payloads[2]["consumer"])
	require.Equal(t, "EXPLICIT", payloads[3]["language_mode"])
	require.Equal(t, []any{"sin"}, payloads[3]["languages"])
	require.Equal(t, "AUTO", payloads[4]["language_mode"])
	require.Equal(t, []any{"eng", "sin", "tam"}, payloads[4]["languages"])
}

func TestServiceExposesBothEditorV2Seams(t *testing.T) {
	var _ OCRV2Service = (*service)(nil)
	var _ GeneralEditorOCRV2Service = (*service)(nil)
	var _ GeneralEditorOCRV2LanguageService = (*service)(nil)
	var _ StudioEditorLanguageService = (*service)(nil)
	var _ LegacyEditorService = (*service)(nil)
}
