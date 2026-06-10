package structure

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service Service
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

func (ctrl *Controller) Merge(c *fiber.Ctx) error {
	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid multipart form transmission")
	}

	files := form.File["files"]
	if len(files) < 2 {
		return c.Status(fiber.StatusBadRequest).SendString("At least two PDF files are required to merge")
	}

	tempDir := os.TempDir()
	var inputPaths []string

	defer func() {
		for _, path := range inputPaths {
			if err := os.Remove(path); err != nil {
				log.Printf("[CLEANUP WARNING] Merge: Failed to delete temporary input file at %s: %v", path, err)
			}
		}
	}()

	for _, fileHeader := range files {
		inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)
		if err := c.SaveFile(fileHeader, inputPath); err != nil {
			log.Printf("[SERVER ERROR] Merge: Failed to save multipart file to path %s: %v", inputPath, err)
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to initialize file compilation array")
		}
		inputPaths = append(inputPaths, inputPath)
	}

	outputPath, err := ctrl.service.MergePDFs(inputPaths)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Compilation engine processing failure: " + err.Error())
	}

	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Merge: Failed to delete output PDF at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) Split(c *fiber.Ctx) error {
	pagesRaw := c.FormValue("pages")
	if pagesRaw == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Page selection configuration is required")
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing source PDF document file")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Split: Failed to save uploaded file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to process workspace file")
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Split: Failed to delete input file at %s: %v", name, err)
		}
	}(inputPath)

	pageSelection := strings.Split(pagesRaw, ",")
	for i, v := range pageSelection {
		pageSelection[i] = strings.TrimSpace(v)
	}

	outputPath, err := ctrl.service.SplitPDF(inputPath, pageSelection)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Extraction engine processing failure or invalid page index syntax: " + err.Error())
	}

	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Split: Failed to delete output split file at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) Rotate(c *fiber.Ctx) error {
	rotationsRaw := c.FormValue("rotations")
	if rotationsRaw == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Rotation configuration configuration is required")
	}

	var rotations map[string]int
	if err := json.Unmarshal([]byte(rotationsRaw), &rotations); err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid rotation matrix structure mapping")
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing source PDF document file")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Rotate: Failed to save input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to save temporary asset container")
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Rotate: Failed to delete input file at %s: %v", name, err)
		}
	}(inputPath)

	outputPath, err := ctrl.service.RotatePDF(inputPath, rotations)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Rotation engine routine execution failure: " + err.Error())
	}

	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Rotate: Failed to delete output file at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) DeletePages(c *fiber.Ctx) error {
	pagesRaw := c.FormValue("pages")
	if pagesRaw == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Page values targeted for deletion are required")
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing source PDF document file")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] DeletePages: Failed to save input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to isolate temporary structural container")
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] DeletePages: Failed to delete input file at %s: %v", name, err)
		}
	}(inputPath)

	pagesToDelete := strings.Split(pagesRaw, ",")
	for i, v := range pagesToDelete {
		pagesToDelete[i] = strings.TrimSpace(v)
	}

	outputPath, err := ctrl.service.DeletePDFPages(inputPath, pagesToDelete)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Deletion engine routine execution failure or out of bounds page index syntax: " + err.Error())
	}

	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] DeletePages: Failed to delete output file at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) ReorderPages(c *fiber.Ctx) error {
	sequenceRaw := c.FormValue("sequence")
	if sequenceRaw == "" {
		return c.Status(fiber.StatusBadRequest).SendString("Sequence data missing")
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("File missing")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)

	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] ReorderPages: Failed to save input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Could not save file")
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] ReorderPages: Failed to delete input file at %s: %v", name, err)
		}
	}(inputPath)

	sequence := strings.Split(sequenceRaw, ",")

	outputPath, err := ctrl.service.ReorderPDFPages(inputPath, sequence)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Reorder failed: " + err.Error())
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] ReorderPages: Failed to delete output file at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) Watermark(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing target source PDF document container file asset")
	}

	text := c.FormValue("text")
	description := c.FormValue("description")

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] Watermark: Failed to save source file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to allocate workspace source environment blocks")
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Watermark: Failed to delete input file at %s: %v", name, err)
		}
	}(inputPath)

	var imagePath string
	imgHeader, err := c.FormFile("watermarkImage")
	if err == nil && imgHeader != nil {
		imagePath = filepath.Join(tempDir, uuid.New().String()+"-"+imgHeader.Filename)
		if err := c.SaveFile(imgHeader, imagePath); err != nil {
			log.Printf("[SERVER ERROR] Watermark: Failed to save graphic asset %s: %v", imagePath, err)
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to process attached watermark graphic asset")
		}
		defer func(name string) {
			if err := os.Remove(name); err != nil {
				log.Printf("[CLEANUP WARNING] Watermark: Failed to delete watermark image asset at %s: %v", name, err)
			}
		}(imagePath)
	}

	outputPath, err := ctrl.service.WatermarkPDF(inputPath, text, imagePath, description)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Watermark generation engine failure: " + err.Error())
	}

	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] Watermark: Failed to delete output file at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) AddPageNumbers(c *fiber.Ctx) error {
	description := c.FormValue("description")
	if description == "" {
		description = "font:Helvetica, pos:bc, scale:12 abs"
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing source PDF document file container asset")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] AddPageNumbers: Failed to save uploaded input file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to allocate workspace memory environment")
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] AddPageNumbers: Failed to delete input file at %s: %v", name, err)
		}
	}(inputPath)

	outputPath, err := ctrl.service.AddPageNumbersPDF(inputPath, description)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Page number compilation layer routine failure: " + err.Error())
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] AddPageNumbers: Failed to delete output file at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) UpdateMetadata(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing source PDF document file container asset")
	}

	password := c.FormValue("password")

	metadata := make(map[string]string)
	if title := c.FormValue("title"); title != "" {
		metadata["Title"] = title
	}
	if author := c.FormValue("author"); author != "" {
		metadata["Author"] = author
	}
	if subject := c.FormValue("subject"); subject != "" {
		metadata["Subject"] = subject
	}
	if keywords := c.FormValue("keywords"); keywords != "" {
		metadata["Keywords"] = keywords
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] UpdateMetadata: Failed to save uploaded file %s: %v", inputPath, err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to allocate workspace memory environment")
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] UpdateMetadata: Failed to delete input file at %s: %v", name, err)
		}
	}(inputPath)

	outputPath, err := ctrl.service.UpdateMetadataPDF(inputPath, metadata, password)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Metadata compilation failure: " + err.Error())
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] UpdateMetadata: Failed to delete output file at %s: %v", name, err)
		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) FetchMetadata(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing source PDF document")
	}

	password := c.FormValue("password")

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		log.Printf("[SERVER ERROR] FetchMetadata: Failed to save file: %v", err)
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to allocate environment")
	}
	defer func(name string) {
		if err := os.Remove(name); err != nil {
			log.Printf("[CLEANUP WARNING] FetchMetadata: Failed to delete input file: %v", err)
		}
	}(inputPath)

	properties, err := ctrl.service.GetMetadataPDF(inputPath, password)
	if err != nil {
		return c.Status(fiber.StatusUnauthorized).SendString("Decryption error or unreadable metadata table: " + err.Error())
	}

	return c.JSON(properties)
}
