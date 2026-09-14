package conversion

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/worker"
)

func TestOfficeToPdfUsesWorkerOfficeRuntime(t *testing.T) {
	var requestPath string
	var requestBody []byte
	previousTransport := worker.Client.Transport
	worker.Client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requestPath = req.URL.Path
		requestBody, _ = io.ReadAll(req.Body)
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("%PDF-1.7\nworker-artifact")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() { worker.Client.Transport = previousTransport })
	t.Setenv("PDFNEST_WORKER_URL", "http://worker.test")
	input := filepath.Join(t.TempDir(), "fixture.docx")
	require.NoError(t, os.WriteFile(input, []byte("docx fixture"), 0600))

	output, err := (&ConversionService{}).OfficeToPdf(t.Context(), input)
	require.NoError(t, err)
	defer os.Remove(output)
	require.Equal(t, "/api/v1/office-to-pdf/convert", requestPath)
	require.Contains(t, string(requestBody), "name=\"format\"")
	require.Contains(t, string(requestBody), "docx")
	require.Contains(t, string(requestBody), "fixture.docx")
	content, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, "%PDF-", string(content[:5]))
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return fn(req) }

func TestOfficeFormatFromPathRejectsUnsupportedExtension(t *testing.T) {
	_, err := officeFormatFromPath("/tmp/document.txt")
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported office document extension")
}
