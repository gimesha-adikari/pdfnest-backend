package structure

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"pdfnest-backend/internal/worker"
)

func SignPdfMulti(
	inputPdfPath string,
	signatureImgPath string,
	outputPath string,
	stampsJSON string,
) error {

	body, contentType, err := worker.CreateMultipartRequest(
		inputPdfPath,
		func(w *multipart.Writer) error {

			signature, err := os.Open(signatureImgPath)
			if err != nil {
				return err
			}
			defer signature.Close()

			part, err := w.CreateFormFile(
				"signature",
				filepath.Base(signatureImgPath),
			)
			if err != nil {
				return err
			}

			if _, err := io.Copy(part, signature); err != nil {
				return err
			}

			if err := w.WriteField("stamps", stampsJSON); err != nil {
				return err
			}

			return nil
		},
	)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		worker.GetWorkerURL()+"/api/v1/sign",
		body,
	)
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := worker.Client.Do(req)
	if err != nil {
		return fmt.Errorf("sign worker unavailable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("sign worker failed: %s", string(msg))
	}

	out, err := os.Create(outputPath)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}
