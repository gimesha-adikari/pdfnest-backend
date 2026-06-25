package structure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
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
	scriptPath := "./scripts/pdf_analyzer.py"
	pythonExecutable := "./venv/bin/python"

	args := []string{scriptPath, inputPath}
	if strings.TrimSpace(filePassword) != "" {
		args = append(args, filePassword)
	}

	cmd := exec.Command(pythonExecutable, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf(
			"python analyzer failed: %w; stderr: %s; stdout: %s",
			err,
			strings.TrimSpace(stderr.String()),
			strings.TrimSpace(stdout.String()),
		)
	}

	var analysis PDFAnalysis
	if err := json.Unmarshal(stdout.Bytes(), &analysis); err != nil {
		return nil, fmt.Errorf("failed to parse analyzer output: %w", err)
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
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "DISK_WRITE_FAILURE",
			"message": "Failed to store input PDF in temporary storage.",
		})
	}
	defer func() {
		_ = os.Remove(inputPath)
	}()

	analysis, err := ctrl.service.AnalyzePDF(inputPath, filePassword)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "ANALYZE_ENGINE_FAILED",
			"message": "PDF analysis failed: " + err.Error(),
		})
	}

	return c.JSON(analysis)
}
