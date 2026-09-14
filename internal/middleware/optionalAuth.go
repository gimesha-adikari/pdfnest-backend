package middleware

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"pdfnest-backend/internal/authn"
)

func OptionalAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		tokenString := c.Cookies("auth_token")

		if tokenString == "" {
			return c.Next()
		}

		user, err := authn.Verify(c.UserContext(), tokenString)
		if err != nil {
			// Optional authentication deliberately falls back to guest behavior,
			// but it must not treat a revoked credential as an authenticated user.
			if errors.Is(err, authn.ErrUnavailable) {
				log.Warn("authentication unavailable during optional auth check; continuing as guest")
			}
			return c.Next()
		}

		c.Locals("user_id", user.ID)
		c.Locals("role", user.Role)

		return c.Next()
	}
}
