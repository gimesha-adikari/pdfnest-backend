package content

import (
	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type ToolController struct{}

func NewToolController() *ToolController {
	return &ToolController{}
}
func (ctrl *ToolController) GetPublicTools(c *fiber.Ctx) error {
	var tools []models.DynamicToolItem
	err := config.DB.Order("id asc").Find(&tools).Error

	if err != nil && err != gorm.ErrRecordNotFound {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to read platform modules matrix layout profiles"})
	}

	if tools == nil {
		tools = []models.DynamicToolItem{}
	}

	return c.JSON(tools)
}

func (ctrl *ToolController) UpdateToolConfiguration(c *fiber.Ctx) error {
	var payload models.DynamicToolItem
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Malformed tool structural data mapping payload"})
	}

	if payload.Href == "" || payload.Title == "" {
		return c.Status(400).JSON(fiber.Map{"error": "Href reference loop links and titles are mandatory fields"})
	}

	var existing models.DynamicToolItem
	err := config.DB.Where("href = ?", payload.Href).First(&existing).Error

	payload.UpdatedAt = time.Now()

	if err == nil {
		payload.ID = existing.ID
		payload.CreatedAt = existing.CreatedAt
		if err := config.DB.Save(&payload).Error; err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Failed updating active tool specifications"})
		}
	} else {
		payload.CreatedAt = time.Now()
		if err := config.DB.Create(&payload).Error; err != nil {
			return c.Status(500).JSON(fiber.Map{"error": "Failed creating custom tool context block"})
		}
	}

	return c.JSON(payload)
}
