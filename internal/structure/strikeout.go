package structure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"pdfnest-backend/config"
	"pdfnest-backend/helper"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type StrikeoutBox struct {
	ID     string  `json:"id"`
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Page   int     `json:"page"`
	Color  string  `json:"color"`
}

func countUniqueStrikeoutPages(boxes []StrikeoutBox) int {
	seen := make(map[int]struct{})
	for _, box := range boxes {
		if box.Page > 0 {
			seen[box.Page] = struct{}{}
		}
	}
	return len(seen)
}

func (s *structureService) StrikeoutPDF(inputPath string, boxes []StrikeoutBox, mode, filePassword string) (string, int, error) {

	tempDir := os.TempDir()

	outputPath := filepath.Join(
		tempDir,
		"strikeout-"+uuid.New().String()+".pdf",
	)

	boxesPath := filepath.Join(
		tempDir,
		"strikeout-boxes-"+uuid.New().String()+".json",
	)

	payload, err := json.Marshal(boxes)
	if err != nil {
		return "", 0, fmt.Errorf("failed to encode strikeout boxes: %w", err)
	}

	if err := os.WriteFile(boxesPath, payload, 0600); err != nil {
		return "", 0, fmt.Errorf("failed to write strikeout temp file: %w", err)
	}
	defer func() {
		_ = os.Remove(boxesPath)
	}()

	scriptPath := "./scripts/pdf_strikeout.py"
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

	cmd := exec.Command(
		pythonExecutable,
		args...,
	)

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", 0, fmt.Errorf(
			"python strikeout failed: %w; stderr: %s; stdout: %s",
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
		ocrPages = countUniqueStrikeoutPages(boxes)
	}

	return outputPath, ocrPages, nil
}

func (ctrl *Controller) Strikeout(c *fiber.Ctx) error {

	userID, _ := c.Locals("user_id").(string)

	boxesStr := c.FormValue("boxes")
	filePassword := c.FormValue("file_password")

	mode := strings.TrimSpace(
		strings.ToLower(
			c.FormValue("mode"),
		),
	)

	if mode == "" {
		mode = "smart"
	}

	var boxes []StrikeoutBox

	if err := json.Unmarshal([]byte(boxesStr), &boxes); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "INVALID_STRIKEOUT_DATA",
			"message": "Failed to parse strikeout selection data.",
		})
	}

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
			"message": "Failed to store uploaded PDF in temporary storage.",
		})
	}
	defer func() {
		_ = os.Remove(inputPath)
	}()

	outputPath, ocrPages, err := ctrl.service.StrikeoutPDF(
		inputPath,
		boxes,
		mode,
		filePassword,
	)

	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "STRIKEOUT_ENGINE_FAILED",
			"message": "Strikeout processing failure: " + err.Error(),
		})
	}

	defer func() {
		_ = os.Remove(outputPath)
	}()

	c.Set("Content-Type", "application/pdf")

	c.Attachment(
		fmt.Sprintf(
			"%s-strikeout.pdf",
			strings.TrimSuffix(
				fileHeader.Filename,
				filepath.Ext(fileHeader.Filename),
			),
		),
	)

	sendErr := c.SendFile(outputPath)

	if sendErr == nil && strings.TrimSpace(userID) != "" {

		config.LogToolUsage(
			userID,
			"strikeout",
			helper.CheckCreditUsage(c),
		)

		for i := 0; i < ocrPages; i++ {
			config.LogToolUsage(
				userID,
				"ocr",
				helper.CheckCreditUsage(c),
			)
		}
	}

	if sendErr == nil {
		logUsageTimes(c, userID, "strikeout", 1)

		logUsageTimes(c, userID, "ocr", ocrPages)
	}

	return sendErr
}
