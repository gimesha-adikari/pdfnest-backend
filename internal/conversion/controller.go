package conversion

import (
	"os"
	"path/filepath"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

type Controller struct {
	service Service
}

func NewController(s Service) *Controller {
	return &Controller{service: s}
}

func (ctrl *Controller) ConvertImagesToPDF(c *fiber.Ctx) error {
	form, err := c.MultipartForm()
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Invalid multipart form transmission asset array")
	}

	files := form.File["images"]
	if len(files) == 0 {
		return c.Status(fiber.StatusBadRequest).SendString("At least one image file is required for compilation")
	}

	tempDir := os.TempDir()
	var temporaryImagePaths []string

	for _, fileHeader := range files {
		uniquePath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)
		if err := c.SaveFile(fileHeader, uniquePath); err != nil {
			for _, path := range temporaryImagePaths {
				err := os.Remove(path)
				if err != nil {
					return err
				}
			}
			return c.Status(fiber.StatusInternalServerError).SendString("Failed to initialize system allocation paths")
		}
		temporaryImagePaths = append(temporaryImagePaths, uniquePath)
	}

	defer func() {
		for _, path := range temporaryImagePaths {
			err := os.Remove(path)
			if err != nil {
				return
			}
		}
	}()

	outputPath, err := ctrl.service.ImagesToPDF(temporaryImagePaths)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Image matrix processing pipeline failure: " + err.Error())
	}
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {

		}
	}(outputPath)

	c.Set("Content-Type", "application/pdf")
	return c.Download(outputPath)
}

func (ctrl *Controller) RasterizePdfUniversal(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(fiber.StatusBadRequest).SendString("Missing source PDF file upload parameter")
	}

	tempDir := os.TempDir()
	inputPath := filepath.Join(tempDir, uuid.New().String()+"-"+fileHeader.Filename)
	if err := c.SaveFile(fileHeader, inputPath); err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("Failed to allocate workspace memory environment")
	}
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {

		}
	}(inputPath)

	zipOutputPath, err := ctrl.service.PdfToImagesBackend(inputPath)
	if err != nil {
		return c.Status(fiber.StatusInternalServerError).SendString("PDF extraction routine failure: " + err.Error())
	}
	defer func(name string) {
		err := os.Remove(name)
		if err != nil {

		}
	}(zipOutputPath)

	c.Set("Content-Type", "application/zip")
	return c.Download(zipOutputPath)
}
