package structure

import (
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"pdfnest-backend/internal/uploads"
	"pdfnest-backend/internal/worker"

	"github.com/gofiber/fiber/v2"
)

type PageAnalysis struct {
	Page              int     `json:"page"`
	Kind              string  `json:"kind"`
	HasSelectableText bool    `json:"hasSelectableText"`
	WordCount         int     `json:"wordCount"`
	TextBlockCount    int     `json:"textBlockCount"`
	ImageBlockCount   int     `json:"imageBlockCount"`
	TextAreaRatio     float64 `json:"textAreaRatio"`
	ImageAreaRatio    float64 `json:"imageAreaRatio"`
}

type PDFAnalysis struct {
	PageCount int            `json:"pageCount"`
	Pages     []PageAnalysis `json:"pages"`
}

func (s *structureService) AnalyzePDF(inputPath, filePassword string) (*PDFAnalysis, error) {
	body, contentType, err := worker.CreateMultipartRequest(
		inputPath,
		func(w *multipart.Writer) error {
			if filePassword != "" {
				return w.WriteField("file_password", filePassword)
			}
			return nil
		},
	)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(
		http.MethodPost,
		worker.GetWorkerURL()+"/api/v1/analyzer/analyze",
		body,
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", contentType)

	resp, err := worker.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("worker request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"worker returned %d: %s",
			resp.StatusCode,
			string(respBody),
		)
	}

	var analysis PDFAnalysis
	if err := json.Unmarshal(respBody, &analysis); err != nil {
		return nil, fmt.Errorf("invalid worker response: %w", err)
	}

	return &analysis, nil
}

func (ctrl *Controller) Analyze(c *fiber.Ctx) error {
	filePassword := c.FormValue("file_password")

	upload, err := uploads.MustPDFFile(c, "file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_UPLOAD_FILE",
			"message": "Missing source PDF file.",
		})
	}

	analysis, err := ctrl.service.AnalyzePDF(upload.Path, filePassword)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "ANALYZE_ENGINE_FAILED",
			"message": err.Error(),
		})
	}

	return c.JSON(analysis)
}
