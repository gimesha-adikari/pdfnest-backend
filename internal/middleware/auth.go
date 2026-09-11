package middleware

import (
	"errors"
	"pdfnest-backend/internal/authn"

	"github.com/gofiber/fiber/v2"
)

func Protect() fiber.Handler {
	return func(c *fiber.Ctx) error {
		tokenString := c.Cookies("auth_token")
		if tokenString == "" {
			return c.Status(401).JSON(fiber.Map{"error": "Access token authorization verification claims dropped"})
		}

		user, err := authn.Verify(c.UserContext(), tokenString)
		if err != nil {
			if errors.Is(err, authn.ErrUnavailable) {
				return c.Status(503).JSON(fiber.Map{"error": "Authentication is temporarily unavailable"})
			}
			return c.Status(401).JSON(fiber.Map{"error": "Invalid or unavailable account session"})
		}
		c.Locals("user_id", user.ID)
		c.Locals("role", user.Role)

		return c.Next()
	}
}

func RequireAdmin() fiber.Handler {
	return func(c *fiber.Ctx) error {
		role := c.Locals("role")
		if role != "admin" {
			return c.Status(403).JSON(fiber.Map{"error": "Administrative elevated access authorization parameters required"})
		}
		return c.Next()
	}
}
