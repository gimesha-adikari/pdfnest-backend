// file: internal/middleware/usage.go
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

		if (sub.Tier == "pro" || sub.Tier == "plus") && time.Now().After(sub.CurrentPeriodEnd) {
			sub.Tier = "free"
			sub.Status = "expired"
			config.DB.Save(&sub)
		}

		limit := 5
		switch sub.Tier {
		case "pro":
			limit = 500
		case "plus":
			limit = 50
		default:
			limit = 5
		}

		var usageCount int64
		today := time.Now().Truncate(24 * time.Hour)

		config.DB.Model(&config.UsageLog{}).
			Where("user_id = ? AND created_at >= ? AND is_credit = false", userID, today).
			Count(&usageCount)

		if int(usageCount) < limit {
			c.Locals("consumed_via_credit", false)
			return c.Next()
		}

		if sub.CustomCredits > 0 {
			sub.CustomCredits -= 1
			if err := config.DB.Save(&sub).Error; err != nil {
				return c.Status(500).JSON(fiber.Map{"error": "Failed adjusting credit asset reserves"})
			}

			c.Locals("consumed_via_credit", true)
			return c.Next()
		}

		return c.Status(429).JSON(fiber.Map{
			"error":                    "Daily tier processed thresholds completely reached. Consume standard credits or purchase add-on bundles.",
			"tier":                     sub.Tier,
			"limit":                    limit,
			"custom_credits_remaining": sub.CustomCredits,
		})
	}
}
