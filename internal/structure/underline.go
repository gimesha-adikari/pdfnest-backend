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

type UnderlineBox struct {
	ID     string  `json:"id"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Page   int     `json:"page"`
	Color  string  `json:"color"`
}

func countUniquePages(boxes []UnderlineBox) int {
	seen := make(map[int]struct{})
	for _, box := range boxes {
		if box.Page > 0 {
			seen[box.Page] = struct{}{}
		}
	}
	return len(seen)
}

func (s *structureService) UnderlinePDF(inputPath string, boxes []UnderlineBox, mode, filePassword string) (string, int, error) {
	tempDir := os.TempDir()
	outputPath := filepath.Join(tempDir, "underlined-"+uuid.New().String()+".pdf")
	boxesPath := filepath.Join(tempDir, "underline-boxes-"+uuid.New().String()+".json")

	payload, err := json.Marshal(boxes)
	if err != nil {
		return "", 0, fmt.Errorf("failed to encode underline boxes: %w", err)
	}

	if err := os.WriteFile(boxesPath, payload, 0600); err != nil {
		return "", 0, fmt.Errorf("failed to write underline boxes temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(boxesPath)
	}()

	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "smart"
	}

	ocrPages := 0
	if mode == "ocr" {
		ocrPages = countUniquePages(boxes)
	}

	scriptPath := "./scripts/pdf_underline.py"
	pythonExecutable := "./venv/bin/python"

	args := []string{
		scriptPath,
		inputPath,
		outputPath,
		boxesPath,
		mode,
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
			"python underline failed: %w; stderr: %s; stdout: %s",
			err,
			strings.TrimSpace(stderr.String()),
			strings.TrimSpace(stdout.String()),
		)
	}

	if _, err := os.Stat(outputPath); err != nil {
		return "", 0, fmt.Errorf("python script did not create output file: %w", err)
	}

	return outputPath, ocrPages, nil
}

func (ctrl *Controller) Underline(c *fiber.Ctx) error {
	userID, _ := c.Locals("user_id").(string)

	boxesStr := c.FormValue("boxes")
	filePassword := c.FormValue("file_password")
	mode := strings.ToLower(strings.TrimSpace(c.FormValue("mode")))
	if mode == "" {
		mode = "smart"
	}

	var boxes []UnderlineBox
	if err := json.Unmarshal([]byte(boxesStr), &boxes); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "INVALID_UNDERLINE_DATA",
			"message": "Failed to parse underline selection data.",
		})
	}

	ocrPages := 0
	if mode == "ocr" {
		ocrPages = countUniquePages(boxes)
	}

	billTool, billPages := billing.SelectUnderlineBilling(mode, ocrPages)

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

	outputPath, _, err := ctrl.service.UnderlinePDF(inputPath, boxes, mode, filePassword)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "UNDERLINE_ENGINE_FAILED",
			"message": "Underline processing failed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment(fmt.Sprintf("%s-underlined.pdf", strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))))

	sendErr := c.SendFile(outputPath)
	if sendErr != nil {
		return sendErr
	}

	defer func() {
		_ = os.Remove(outputPath)
	}()

	if err := billing.Default.Commit(reservation.ID); err != nil {
		log.Printf("[BILLING] underline commit failed: %v", err)
		return nil
	}

	committed = true
	return nil
}
