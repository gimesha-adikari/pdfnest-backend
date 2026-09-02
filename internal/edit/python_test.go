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

	require.Len(t, payloads, 3)
	require.NotContains(t, payloads[0], "ocr_v2")
	require.Equal(t, "legacy_editor", payloads[0]["consumer"])
	require.Equal(t, true, payloads[1]["ocr_v2"])
	require.Equal(t, "studio", payloads[1]["consumer"])
	require.Equal(t, true, payloads[2]["ocr_v2"])
	require.Equal(t, "general_editor", payloads[2]["consumer"])
}

func TestServiceExposesBothEditorV2Seams(t *testing.T) {
	var _ OCRV2Service = (*service)(nil)
	var _ GeneralEditorOCRV2Service = (*service)(nil)
	var _ LegacyEditorService = (*service)(nil)
}
