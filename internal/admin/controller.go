package admin

import (
	"pdfnest-backend/config"
	"time"

	"github.com/gofiber/fiber/v2"
)

type Controller struct{}

func NewController() *Controller {
	return &Controller{}
}

func (ctrl *Controller) ListUsers(c *fiber.Ctx) error {
	var users []config.User
	if err := config.DB.Select("id, email, role, status, created_at").Find(&users).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed scanning infrastructure indexes accounts records map logs"})
	}
	return c.JSON(users)
}

func (ctrl *Controller) ToggleBanUser(c *fiber.Ctx) error {
	targetID := c.Params("id")
	var user config.User

	if err := config.DB.First(&user, "id = ?", targetID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Target mapping track identity row mismatch not found"})
	}

	if user.Status == "active" {
		user.Status = "banned"
	} else {
		user.Status = "active"
	}

	config.DB.Save(&user)
	return c.JSON(fiber.Map{"status": "success", "updated_status": user.Status})
}

// Add this to internal/admin/controller.go

func (ctrl *Controller) ListSubscriptions(c *fiber.Ctx) error {
	var subscriptions []config.Subscription
	if err := config.DB.Find(&subscriptions).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": "Failed to retrieve subscriptions"})
	}
	return c.JSON(subscriptions)
}

func (ctrl *Controller) UpdateUserTier(c *fiber.Ctx) error {
	targetID := c.Params("id")

	var req struct {
		Tier string `json:"tier"`
	}

	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "Invalid payload"})
	}

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", targetID).First(&sub).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "Subscription not found for user"})
	}

	sub.Tier = req.Tier
	if req.Tier == "pro" {
		sub.CurrentPeriodEnd = time.Now().AddDate(1, 0, 0)
	}

	config.DB.Save(&sub)
	return c.JSON(fiber.Map{"status": "success", "new_tier": sub.Tier})
}
