package middleware

import (
	"pdfnest-backend/config"
	"time"

	"github.com/gofiber/fiber/v2"
)

type LimitConfig struct {
	MaxDailyUses int
}

var PlanLimits = map[string]LimitConfig{
	"free": {MaxDailyUses: 5},
	"pro":  {MaxDailyUses: 500},
}

func EnforceLimits() fiber.Handler {
	return func(c *fiber.Ctx) error {
		userID := c.Locals("user_id").(string)

		var sub config.Subscription
		err := config.DB.Where("user_id = ? AND status = 'active'", userID).First(&sub).Error
		tier := "free"
		if err == nil {
			tier = sub.PlanTier
		}

		limits := PlanLimits[tier]

		now := time.Now()
		startOfToday := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

		var currentUsage int64
		config.DB.Model(&config.UsageLog{}).
			Where("user_id = ? AND created_at >= ?", userID, startOfToday).
			Count(&currentUsage)

		if currentUsage >= int64(limits.MaxDailyUses) {
			return c.Status(429).JSON(fiber.Map{
				"error": "Daily usage limit reached. Please upgrade your subscription plan to continue.",
			})
		}

		return c.Next()
	}
}
