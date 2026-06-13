// file: internal/edit/controller.go
package edit

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
	return &Controller{
		service: s,
	}
}

func (cr *Controller) HandleExtractHTML(c *fiber.Ctx) error {
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return fiber.NewError(
			fiber.StatusBadRequest,
			"PDF file is required",
		)
	}

	tempPdfPath := filepath.Join(
		os.TempDir(),
		uuid.New().String()+".pdf",
	)

	if err := c.SaveFile(fileHeader, tempPdfPath); err != nil {
		return fiber.NewError(
			fiber.StatusInternalServerError,
			"Failed to save uploaded file",
		)
	}

	defer os.Remove(tempPdfPath)

	html, err := cr.service.ExtractHTML(tempPdfPath)
	if err != nil {
		return fiber.NewError(
			fiber.StatusInternalServerError,
			err.Error(),
		)
	}

	return c.JSON(ExtractResponse{
		HTML: html,
	})
}

func (cr *Controller) HandleCompilePDF(c *fiber.Ctx) error {
	var req CompileRequest

	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(
			fiber.StatusBadRequest,
			"Invalid request body",
		)
	}

	if req.HTML == "" {
		return fiber.NewError(
			fiber.StatusBadRequest,
			"HTML content is required",
		)
	}

	pdfPath, err := cr.service.CompilePDF(req.HTML)
	if err != nil {
		return fiber.NewError(
			fiber.StatusInternalServerError,
			err.Error(),
		)
	}

	defer os.Remove(pdfPath)

	return c.Download(pdfPath)
}
