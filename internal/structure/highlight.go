package structure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/helper"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type HighlightBox struct {
	ID     string  `json:"id"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Page   int     `json:"page"`
	Color  string  `json:"color"`
}

func (s *structureService) HighlightPDF(inputPath string, boxes []HighlightBox, filePassword string) (string, error) {
	tempDir := os.TempDir()
	outputPath := filepath.Join(tempDir, "highlighted-"+uuid.New().String()+".pdf")
	boxesPath := filepath.Join(tempDir, "highlight-boxes-"+uuid.New().String()+".json")

	payload, err := json.Marshal(boxes)
	if err != nil {
		return "", fmt.Errorf("failed to encode highlight boxes: %w", err)
	}

	if err := os.WriteFile(boxesPath, payload, 0600); err != nil {
		return "", fmt.Errorf("failed to write highlight boxes temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(boxesPath)
	}()

	scriptPath := "./scripts/pdf_highlight.py"
	pythonExecutable := "./venv/bin/python"

	args := []string{
		scriptPath,
		inputPath,
		outputPath,
		boxesPath,
	}

	if strings.TrimSpace(filePassword) != "" {
		args = append(args, filePassword)
	}

	cmd := exec.Command(pythonExecutable, args...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf(
			"python highlight failed: %w; stderr: %s; stdout: %s",
			err,
			strings.TrimSpace(stderr.String()),
			strings.TrimSpace(stdout.String()),
		)
	}

	if _, err := os.Stat(outputPath); err != nil {
		return "", fmt.Errorf("python script did not create output file: %w", err)
	}

	return outputPath, nil
}

func (ctrl *Controller) Highlight(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)
	boxesStr := c.FormValue("boxes")
	filePassword := c.FormValue("file_password")

	var boxes []HighlightBox
	if err := json.Unmarshal([]byte(boxesStr), &boxes); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "INVALID_HIGHLIGHT_DATA",
			"message": "Failed to parse canvas selection bounds.",
		})
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_UPLOAD_FILE",
			"message": "Missing source PDF element.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "DISK_WRITE_FAILURE",
			"message": "Failed to isolate document file parameter configurations.",
		})
	}
	defer func() {
		_ = os.Remove(inputPath)
	}()

	outputPath, err := ctrl.service.HighlightPDF(inputPath, boxes, filePassword)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "HIGHLIGHT_ENGINE_FAILED",
			"message": "Highlighter processing failure: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment(fmt.Sprintf("%s-highlighted.pdf", strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))))

	sendErr := c.SendFile(outputPath)

	defer func() {
		_ = os.Remove(outputPath)
	}()

	if sendErr == nil {
		config.LogToolUsage(userID, "duplicate", helper.CheckCreditUsage(c))
	}

	return sendErr
}
