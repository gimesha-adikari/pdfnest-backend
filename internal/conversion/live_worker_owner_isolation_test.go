package conversion

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLiveWorkerOwnerIsolation_EndToEnd(t *testing.T) {
	pythonPath := "/home/gimesha/My_Projects/platen/pdfnest-worker/.venv/bin/python3"
	if _, err := os.Stat(pythonPath); os.IsNotExist(err) {
		t.Skip("worker python virtualenv not found, skipping live worker test")
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())

	portStr := strconv.Itoa(port)
	workerURL := fmt.Sprintf("http://127.0.0.1:%s", portStr)
	sessionDir := t.TempDir()
	const sharedSecret = "live-integration-worker-secret-42"

	cmd := exec.Command(
		pythonPath,
		"-m",
		"uvicorn",
		"app.main:app",
		"--host",
		"127.0.0.1",
		"--port",
		portStr,
		"--log-level",
		"warning",
	)
	cmd.Dir = "/home/gimesha/My_Projects/platen/pdfnest-worker"
	cmd.Env = append(
		os.Environ(),
		"PYTHONPATH=/home/gimesha/My_Projects/platen/pdfnest-worker",
		"WORKER_SHARED_SECRET="+sharedSecret,
		"PDFNEST_RENDER_SESSION_DIR="+sessionDir,
		"APP_ENV=test",
	)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Signal(syscall.SIGTERM)
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
			}
		}
	})

	// Wait for worker readiness
	ready := false
	for i := 0; i < 50; i++ {
		time.Sleep(200 * time.Millisecond)
		resp, err := http.Get(workerURL + "/health/live")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
	}
	require.True(t, ready, "local worker server failed to become ready")

	t.Setenv("PDFNEST_WORKER_URL", workerURL)
	t.Setenv("WORKER_SHARED_SECRET", sharedSecret)

	previousCache := globalPreviewSessions
	globalPreviewSessions = &previewSessionCache{
		sessions: make(map[string]*previewWorkerSession),
		byHash:   make(map[string]string),
	}
	t.Cleanup(func() { globalPreviewSessions = previousCache })

	svc := &ConversionService{}

	// Create deterministic sample PDF
	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog /Pages 2 0 R >>\nendobj\n" +
		"2 0 obj\n<< /Type /Pages /Kids [3 0 R] /Count 1 >>\nendobj\n" +
		"3 0 obj\n<< /Type /Page /Parent 2 0 R /Resources <<>> /MediaBox [0 0 612 792] >>\nendobj\n" +
		"xref\n0 4\n0000000000 65535 f \n0000000009 00000 n \n0000000058 00000 n \n0000000115 00000 n \n" +
		"trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n206\n%%EOF\n")

	makeHeader := func(name string) *multipart.FileHeader {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", name)
		require.NoError(t, err)
		_, err = part.Write(pdfBytes)
		require.NoError(t, err)
		require.NoError(t, form.Close())

		req := httptest.NewRequest(http.MethodPost, "/upload", &body)
		req.Header.Set("Content-Type", form.FormDataContentType())
		require.NoError(t, req.ParseMultipartForm(1<<20))
		return req.MultipartForm.File["file"][0]
	}

	const ownerAlpha = "guest-owner-alpha-111"
	const ownerBeta = "guest-owner-beta-222"

	ctx := context.Background()

	// 1. Owner Alpha creates preview session
	resA, err := svc.CreatePreviewSession(ctx, makeHeader("docA.pdf"), ownerAlpha)
	require.NoError(t, err)
	sessionIDA := resA["session_id"].(string)
	require.NotEmpty(t, sessionIDA)

	// 2. Owner Beta creates preview session with identical bytes
	resB, err := svc.CreatePreviewSession(ctx, makeHeader("docB.pdf"), ownerBeta)
	require.NoError(t, err)
	sessionIDB := resB["session_id"].(string)
	require.NotEmpty(t, sessionIDB)

	// Isolation assertion: Guest A and Guest B receive DIFFERENT session IDs!
	require.NotEqual(t, sessionIDA, sessionIDB, "identical PDF uploads from different owners must receive isolated sessions")

	// 3. Owner Alpha creates preview session again with identical bytes
	resA2, err := svc.CreatePreviewSession(ctx, makeHeader("docA2.pdf"), ownerAlpha)
	require.NoError(t, err)
	sessionIDA2 := resA2["session_id"].(string)
	require.Equal(t, sessionIDA, sessionIDA2, "identical upload from same owner must reuse existing session")

	// 4. Owner Alpha renders page 1
	imgA, err := svc.GetPreviewSessionPage(ctx, sessionIDA, 1, 1.0, ownerAlpha)
	require.NoError(t, err)
	require.NotEmpty(t, imgA)
	require.True(t, len(imgA) > 100, "rendered page image must contain valid image bytes")

	// 5. Cross-owner access denial: Owner Beta cannot access Owner Alpha's session
	_, err = svc.GetPreviewSessionPage(ctx, sessionIDA, 1, 1.0, ownerBeta)
	require.Error(t, err, "cross-owner session access must be rejected")

	// 6. Deletion independence: Owner Alpha deletes session A
	err = svc.DeletePreviewSession(ctx, sessionIDA, ownerAlpha)
	require.NoError(t, err)

	// 7. Verify Owner Beta's session B is completely intact and renderable
	imgB, err := svc.GetPreviewSessionPage(ctx, sessionIDB, 1, 1.0, ownerBeta)
	require.NoError(t, err)
	require.NotEmpty(t, imgB)

	// 8. Delete session B
	err = svc.DeletePreviewSession(ctx, sessionIDB, ownerBeta)
	require.NoError(t, err)
}
