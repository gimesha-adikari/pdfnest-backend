package structure

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSignPdfMultiUsesSignedWorkerRouteAndMultipartContract(t *testing.T) {
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/sign" {
			t.Fatalf("expected worker sign route, got %s", r.URL.Path)
		}
		if r.Header.Get("X-Worker-Signature") == "" || r.Header.Get("X-Worker-Timestamp") == "" || r.Header.Get("X-Worker-Nonce") == "" {
			t.Fatal("expected signed worker request headers")
		}
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			t.Fatalf("parse multipart request: %v", err)
		}
		stamps := r.FormValue("stamps")
		if !strings.Contains(stamps, `"page":1`) || !strings.Contains(stamps, `"x":72`) || !strings.Contains(stamps, `"y":711.89`) || !strings.Contains(stamps, `"width":180`) || !strings.Contains(stamps, `"height":60`) {
			t.Fatalf("unexpected stamps payload: %s", stamps)
		}
		file, _, err := r.FormFile("signature")
		if err != nil {
			t.Fatalf("signature part missing: %v", err)
		}
		defer file.Close()
		if _, err := io.Copy(io.Discard, file); err != nil {
			t.Fatalf("read signature part: %v", err)
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = io.WriteString(w, "%PDF-1.4\nworker-signed")
	}))
	defer worker.Close()
	t.Setenv("PDFNEST_WORKER_URL", worker.URL)

	dir := t.TempDir()
	input := filepath.Join(dir, "input.pdf")
	signature := filepath.Join(dir, "signature.png")
	output := filepath.Join(dir, "output.pdf")
	if err := os.WriteFile(input, []byte("%PDF-1.4\nsource"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(signature, []byte("png-bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := SignPdfMulti(input, signature, output, `[{"page":1,"x":72,"y":711.89,"width":180,"height":60}]`); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bytes), "worker-signed") {
		t.Fatalf("expected worker response to be written, got %q", string(bytes))
	}
}
