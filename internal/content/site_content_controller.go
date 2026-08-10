package content

import (
	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"time"

	"github.com/gofiber/fiber/v2"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

func (ctrl *Controller) GetHomePageContent(c *fiber.Ctx) error {
	var content models.HomePageContent
	if err := config.DB.First(&content, 1).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "home page content records missing"})
	}
	return c.JSON(content)
}

func (ctrl *Controller) UpdateHomePageContent(c *fiber.Ctx) error {
	var payload models.HomePageContent
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "malformed structural payload data"})
	}

	// Home content is a singleton; updates must not create another record.
	payload.ID = 1
	payload.UpdatedAt = time.Now()

	if err := config.DB.Save(&payload).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "failed to write home configuration override updates"})
	}
	return c.JSON(payload)
}

func (ctrl *Controller) GetSubscribePageContent(c *fiber.Ctx) error {
	var content models.SubscribePageContent
	if err := config.DB.First(&content, 1).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "subscription template definitions missing"})
	}
	return c.JSON(content)
}

func (ctrl *Controller) UpdateSubscribePageContent(c *fiber.Ctx) error {
	var payload models.SubscribePageContent
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "malformed structural payload data"})
	}

	payload.ID = 1
	payload.UpdatedAt = time.Now()

	if err := config.DB.Save(&payload).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "failed to write subscription matrix configuration updates"})
	}
	return c.JSON(payload)
}

func (ctrl *Controller) GetAboutPageContent(c *fiber.Ctx) error {
	var content models.AboutPageContent
	if err := config.DB.First(&content, 1).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "about page content records missing"})
	}
	return c.JSON(content)
}

func (ctrl *Controller) UpdateAboutPageContent(c *fiber.Ctx) error {
	var payload models.AboutPageContent
	if err := c.BodyParser(&payload); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "malformed structural payload data"})
	}

	payload.ID = 1
	payload.UpdatedAt = time.Now()

	if err := config.DB.Save(&payload).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "failed to write about configuration override updates"})
	}
	return c.JSON(payload)
}
