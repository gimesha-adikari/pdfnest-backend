package structure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"pdfnest-backend/internal/billing"

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

func countUniqueHighlightPages(boxes []HighlightBox) int {
	seen := make(map[int]struct{})
	for _, box := range boxes {
		if box.Page > 0 {
			seen[box.Page] = struct{}{}
		}
	}
	return len(seen)
}

func (s *structureService) HighlightPDF(inputPath string, boxes []HighlightBox, mode, filePassword string) (string, int, error) {
	tempDir := os.TempDir()
	outputPath := filepath.Join(tempDir, "highlighted-"+uuid.New().String()+".pdf")
	boxesPath := filepath.Join(tempDir, "highlight-boxes-"+uuid.New().String()+".json")

	payload, err := json.Marshal(boxes)
	if err != nil {
		return "", 0, fmt.Errorf("failed to encode highlight boxes: %w", err)
	}

	if err := os.WriteFile(boxesPath, payload, 0600); err != nil {
		return "", 0, fmt.Errorf("failed to write highlight boxes temp file: %w", err)
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
		strings.TrimSpace(mode),
	}

	if strings.TrimSpace(mode) == "" {
		args[4] = "smart"
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
		return "", 0, fmt.Errorf(
			"python highlight failed: %w; stderr: %s; stdout: %s",
			err,
			strings.TrimSpace(stderr.String()),
			strings.TrimSpace(stdout.String()),
		)
	}

	if _, err := os.Stat(outputPath); err != nil {
		return "", 0, fmt.Errorf("python script did not create output file: %w", err)
	}

	ocrPages := 0
	if strings.TrimSpace(mode) == "ocr" {
		ocrPages = countUniqueHighlightPages(boxes)
	}

	return outputPath, ocrPages, nil
}

func (ctrl *Controller) Highlight(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)

	boxesStr := c.FormValue("boxes")
	filePassword := c.FormValue("file_password")
	mode := strings.TrimSpace(strings.ToLower(c.FormValue("mode")))
	if mode == "" {
		mode = "smart"
	}

	var boxes []HighlightBox
	if err := json.Unmarshal([]byte(boxesStr), &boxes); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "INVALID_HIGHLIGHT_DATA",
			"message": "Failed to parse highlight selection data.",
		})
	}

	ocrPages := 0
	if mode == "ocr" {
		ocrPages = countUniqueHighlightPages(boxes)
	}

	billTool, billPages := billing.SelectHighlightBilling(mode, ocrPages)

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
			"message": "Failed to store uploaded PDF in temporary storage.",
		})
	}
	defer func() {
		_ = os.Remove(inputPath)
	}()

	// Reserve before processing so this route still respects quotas.
	reservation, err := billing.Default.Reserve(userID, billTool, billPages, 0, c.Path())
	if err != nil {
		return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
			"code":    "BILLING_BLOCKED",
			"message": err.Error(),
		})
	}

	committed := false
	defer func() {
		if !committed {
			_ = billing.Default.Release(reservation.ID)
		}
	}()

	outputPath, _, err := ctrl.service.HighlightPDF(inputPath, boxes, mode, filePassword)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "HIGHLIGHT_ENGINE_FAILED",
			"message": "Highlighter processing failure: " + err.Error(),
		})
	}
	defer func() {
		_ = os.Remove(outputPath)
	}()

	c.Set("Content-Type", "application/pdf")
	c.Attachment(fmt.Sprintf("%s-highlighted.pdf", strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))))

	sendErr := c.SendFile(outputPath)
	if sendErr != nil {
		return sendErr
	}

	if err := billing.Default.Commit(reservation.ID); err != nil {
		log.Printf("[BILLING] highlight commit failed: %v", err)
		return nil
	}

	committed = true
	return nil
}
