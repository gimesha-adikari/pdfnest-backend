package middleware

import (
	"pdfnest-backend/config"
	"time"

	"github.com/gofiber/fiber/v2"
)

func EnforceLimits() fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := c.Locals("user_id").(string)

		var sub config.Subscription
		if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
			return c.Status(403).JSON(fiber.Map{"error": "Account subscription missing"})
		}

		if sub.Tier == "pro" && time.Now().After(sub.CurrentPeriodEnd) {
			sub.Tier = "free"
			sub.Status = "expired"
			config.DB.Save(&sub)
		}

		limit := 5
		if sub.Tier == "pro" {
			limit = 500
		}

		var usageCount int64
		today := time.Now().Truncate(24 * time.Hour)

		config.DB.Model(&config.UsageLog{}).
			Where("user_id = ? AND created_at >= ?", userID, today).
			Count(&usageCount)

		if int(usageCount) >= limit {
			return c.Status(429).JSON(fiber.Map{
				"error": "Daily limit reached. Please upgrade to continue.",
				"tier":  sub.Tier,
				"limit": limit,
			})
		}

		return c.Next()
	}
}
