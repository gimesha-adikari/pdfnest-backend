package structure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

var workerURL = os.Getenv("PDFNEST_WORKER_URL")

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
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	fileWriter, err := writer.CreateFormFile("file", filepath.Base(inputPath))
	if err != nil {
		return nil, err
	}

	file, err := os.Open(inputPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	if _, err := io.Copy(fileWriter, file); err != nil {
		return nil, err
	}

	if filePassword != "" {
		if err := writer.WriteField("file_password", filePassword); err != nil {
			return nil, err
		}
	}

	if err := writer.Close(); err != nil {
		return nil, err
	}

	if workerURL == "" {
		workerURL = "http://0.0.0.0:8000"
	}

	req, err := http.NewRequest(
		http.MethodPost,
		workerURL+"/api/v1/analyzer/analyze",
		body,
	)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
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

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_UPLOAD_FILE",
			"message": "Missing source PDF file.",
		})
	}

	tempDir := os.TempDir()

	inputPath := filepath.Join(
		tempDir,
		uuid.New().String()+"-"+filepath.Base(fileHeader.Filename),
	)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "DISK_WRITE_FAILURE",
			"message": "Failed to store input PDF in temporary storage.",
		})
	}
	defer os.Remove(inputPath)

	analysis, err := ctrl.service.AnalyzePDF(inputPath, filePassword)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "ANALYZE_ENGINE_FAILED",
			"message": err.Error(),
		})
	}

	return c.JSON(analysis)
}
