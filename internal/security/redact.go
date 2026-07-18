package security

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"pdfnest-backend/internal/worker"

	"github.com/google/uuid"
)

func (s *securityService) RedactPageText(
	inputPath string,
	outputDir string,
	keywords []string,
	boxesStr string,
) (string, error) {

	outFileName := fmt.Sprintf("redacted_%s.pdf", uuid.New().String())
	finalOutPath := filepath.Join(outputDir, outFileName)

	if boxesStr == "" {
		boxesStr = "[]"
	}

	keywordsStr := strings.Join(keywords, "|||")

	body, contentType, err := worker.CreateMultipartRequest(
		inputPath,
		func(w *multipart.Writer) error {

			if err := w.WriteField("keywords", keywordsStr); err != nil {
				return err
			}

			if err := w.WriteField("boxes", boxesStr); err != nil {
				return err
			}

			return nil
		},
	)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		worker.GetWorkerURL()+"/api/v1/redact",
		body,
	)
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := worker.Client.Do(req)
	if err != nil {
		return "", fmt.Errorf("redaction worker unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("redaction worker failed: %s", string(b))
	}

	out, err := os.Create(finalOutPath)
	if err != nil {
		return "", err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", err
	}

	if _, err := os.Stat(finalOutPath); err != nil {
		return "", fmt.Errorf("worker returned successfully but output PDF is missing")
	}

	return outFileName, nil
}
