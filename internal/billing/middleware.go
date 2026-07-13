package billing

import (
	"log"
	"pdfnest-backend/config"
	"strings"

	"github.com/gofiber/fiber/v2"
)

func Use(tool Tool) fiber.Handler {
	return func(c *fiber.Ctx) error {

		path := c.Path()
		if strings.HasSuffix(path, "/structure/highlight") ||
			strings.HasSuffix(path, "/structure/strikeout") ||
			strings.HasSuffix(path, "/structure/underline") {
			return c.Next()
		}

		userID, _ := c.Locals("user_id").(string)
		if strings.TrimSpace(userID) == "" {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "missing authenticated user",
			})
		}

		reservation, err := ReserveFromRequest(c, userID, tool)
		if err != nil {
			log.Printf("[BILLING] reserve failed user=%s tool=%s path=%s err=%v", userID, tool.Name, c.Path(), err)
			return c.Status(fiber.StatusTooManyRequests).JSON(fiber.Map{
				"error":                    err.Error(),
				"tool":                     tool.Name,
				"custom_credits_remaining": currentCreditsForError(userID),
			})
		}

		c.Locals("billing_reservation_id", reservation.ID)
		c.Locals("billing_tool", tool.Name)
		c.Locals("consumed_via_credit", reservation.CreditUnits > 0)

		err = c.Next()
		if err != nil {
			_ = Default.Release(reservation.ID)
			return err
		}

		if c.Response().StatusCode() >= 400 {
			_ = Default.Release(reservation.ID)
			return nil
		}

		if err := Default.Commit(reservation.ID); err != nil {
			_ = Default.Release(reservation.ID)
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "failed to finalize billing",
			})
		}

		return nil
	}
}

func currentCreditsForError(userID string) int {
	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		return 0
	}
	return sub.CustomCredits
}
