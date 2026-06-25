package structure

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"pdfnest-backend/config"
	"pdfnest-backend/helper"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type TextElement struct {
	ID       string  `json:"id"`
	Text     string  `json:"text"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Page     int     `json:"page"`
	FontSize int     `json:"fontSize"`
	Color    string  `json:"color"`
}

func (s *structureService) AddTextToPDF(inputPath string, elements []TextElement) (string, error) {
	tempDir := os.TempDir()
	outputFile := "text-added-" + uuid.New().String() + ".pdf"
	outputPath := filepath.Join(tempDir, outputFile)

	config := model.NewDefaultConfiguration()

	if len(elements) == 0 {
		err := api.OptimizeFile(inputPath, outputPath, config)
		return outputPath, err
	}

	currentIn := inputPath

	for i, el := range elements {
		if strings.TrimSpace(el.Text) == "" {
			continue
		}

		currentOut := outputPath

		if i < len(elements)-1 {
			currentOut = filepath.Join(tempDir, fmt.Sprintf("step-%d-%s.pdf", i, uuid.New().String()))
		}

		colorHex := el.Color
		if !strings.HasPrefix(colorHex, "#") {
			colorHex = "#" + colorHex
		}

		desc := fmt.Sprintf(
			"font:Helvetica, points:%d, pos:tl, offset:%f %f, scale:1 abs, rot:0, fillcol:%s",
			el.FontSize,
			el.X,
			-el.Y-13,
			colorHex,
		)

		fmt.Printf("FontSize = %d\n", el.FontSize)

		wm, err := api.TextWatermark(el.Text, desc, true, false, types.POINTS)

		fmt.Printf("%+v\n", wm)
		if err != nil {
			return "", fmt.Errorf("failed to build watermark config for element %s: %w", el.ID, err)
		}

		selectedPages := []string{strconv.Itoa(el.Page)}

		err = api.AddWatermarksFile(currentIn, currentOut, selectedPages, wm, config)
		if err != nil {
			return "", fmt.Errorf("failed to stamp text onto page %d: %w", el.Page, err)
		}

		if currentIn != inputPath {
			_ = os.Remove(currentIn)
		}
		currentIn = currentOut
	}

	return outputPath, nil
}

func (ctrl *Controller) AddText(c *fiber.Ctx) error {
	userID := c.Locals("user_id").(string)
	elementsStr := c.FormValue("elements")

	var elements []TextElement
	if err := json.Unmarshal([]byte(elementsStr), &elements); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "INVALID_ELEMENTS",
			"message": "Failed to parse text box positioning data.",
		})
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{
			"code":    "MISSING_UPLOAD_FILE",
			"message": "Missing input context PDF source vector.",
		})
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+filepath.Base(fileHeader.Filename))

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "DISK_WRITE_FAILURE",
			"message": "Failed to store asset into temporary scratch bounds.",
		})
	}
	defer func() {
		_ = os.Remove(inputPath)
	}()

	outputPath, err := ctrl.service.AddTextToPDF(inputPath, elements)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
			"code":    "TEXT_ENGINE_FAILED",
			"message": "Text rendering transaction failed: " + err.Error(),
		})
	}

	c.Set("Content-Type", "application/pdf")
	c.Attachment(fmt.Sprintf("%s-text-added.pdf", strings.TrimSuffix(fileHeader.Filename, filepath.Ext(fileHeader.Filename))))

	sendErr := c.SendFile(outputPath)

	defer func() {
		_ = os.Remove(outputPath)
	}()

	if sendErr == nil {
		config.LogToolUsage(userID, "duplicate", helper.CheckCreditUsage(c))
	}

	return sendErr
}
