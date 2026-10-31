package conversion_test

import (
	"bytes"
	"encoding/json"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdfnest-backend/internal/edit"
	"pdfnest-backend/internal/ocrv2"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/worker"

	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/stretchr/testify/require"
)

// Called only inside the public harness's isolated services and ownership DB.
// These are real backend clients and native operations, using legacy execution;
// they do not qualify the dormant independent controller or customer billing.
func qualifyLocalNativeClients(t *testing.T, root string) {
	input := filepath.Join(root, "synthetic.pdf")
	t.Run("signed_multipart_render", func(t *testing.T) {
		body, contentType, err := worker.CreateMultipartRequest(input, func(w *multipart.Writer) error {
			if err := w.WriteField("page", "1"); err != nil {
				return err
			}
			return w.WriteField("dpi", "72")
		})
		require.NoError(t, err)
		defer body.Close()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, worker.GetWorkerURL()+"/api/v1/render/page", body)
		require.NoError(t, err)
		req.Header.Set("Content-Type", contentType)
		response, err := worker.Do(req)
		require.NoError(t, err)
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, response.StatusCode, string(data))
		image, err := jpeg.Decode(bytes.NewReader(data))
		require.NoError(t, err)
		require.Equal(t, 300, image.Bounds().Dx())
		require.Equal(t, 150, image.Bounds().Dy())
		t.Log(`LOCAL_NATIVE_E2E {"operation":"render","transport":"signed_multipart","width":300,"height":150}`)
	})
	t.Run("queued_ocr_text", func(t *testing.T) {
		key := "jobs/ocr_v2/input/" + uuid.NewString() + ".pdf"
		require.NoError(t, storage.SaveObjectFile(t.Context(), key, input, "application/pdf"))
		client := ocrv2.NewWorkerClient()
		job, err := client.SubmitJob(t.Context(), ocrv2.JobSubmitRequest{RequestID: uuid.NewString(), Profile: ocrv2.ProfileOCRTextV2, Language: "eng", RoutingPolicy: ocrv2.RoutingFast, SourceKey: key, SourceName: "synthetic.pdf", OwnerIdentity: uuid.NewString(), TotalPages: 2})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			status, err := client.GetJob(t.Context(), job.JobID)
			return err == nil && status.Status == "succeeded"
		}, 45*time.Second, 100*time.Millisecond)
		result, err := client.GetResult(t.Context(), job.JobID)
		require.NoError(t, err)
		require.Contains(t, result.Text, "PLATEN PUBLIC ALPHA")
		require.Contains(t, result.Text, "PLATEN PUBLIC BETA")
		require.Len(t, result.Pages, 2)
		t.Log(`LOCAL_NATIVE_E2E {"operation":"ocr_text","transport":"signed_json_actor","pages":2}`)
	})
	t.Run("queued_editor_extract_compile_download", func(t *testing.T) {
		key := "jobs/editor/source/" + uuid.NewString() + ".pdf"
		require.NoError(t, storage.SaveObjectFile(t.Context(), key, input, "application/pdf"))
		service := edit.NewService()
		extracted, err := service.ExtractLayout(key, "", "synthetic.pdf")
		require.NoError(t, err)
		var layout *edit.WorkerJobRecord
		require.Eventually(t, func() bool {
			layout, err = service.GetJobStatus(extracted.JobID)
			return err == nil && layout.Status == "succeeded"
		}, 45*time.Second, 100*time.Millisecond)
		payload, err := json.Marshal(layout.Result)
		require.NoError(t, err)
		require.Contains(t, string(payload), "PLATEN PUBLIC ALPHA")
		layoutKey := "jobs/editor/payload/" + uuid.NewString() + ".json"
		require.NoError(t, storage.SaveObjectBytes(t.Context(), layoutKey, payload, "application/json"))
		compiled, err := service.CompileLayout(key, layoutKey, "synthetic.pdf")
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			status, err := service.GetJobStatus(compiled.JobID)
			return err == nil && status.Status == "succeeded"
		}, 45*time.Second, 100*time.Millisecond)
		response, err := service.GetJobDownload(compiled.JobID)
		require.NoError(t, err)
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
		require.NoError(t, err)
		require.True(t, bytes.HasPrefix(data, []byte("%PDF-")))
		output := filepath.Join(t.TempDir(), "compiled.pdf")
		require.NoError(t, os.WriteFile(output, data, 0600))
		pages, err := api.PageCountFile(output)
		require.NoError(t, err)
		require.Equal(t, 2, pages)
		t.Log(`LOCAL_NATIVE_E2E {"operation":"editor_extract_compile","transport":"signed_json_actor_download","pages":2}`)
	})
}
