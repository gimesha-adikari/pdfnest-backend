package middleware

import (
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

func Protect() fiber.Handler {
	return func(c *fiber.Ctx) error {
		tokenString := c.Cookies("auth_token")
		if tokenString == "" {
			return c.Status(401).JSON(fiber.Map{"error": "Access token authorization verification claims dropped"})
		}

		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			secret = "super-secret-fallback-token-key"
		}

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			return []byte(secret), nil
		})

		if err != nil || !token.Valid {
			return c.Status(401).JSON(fiber.Map{"error": "Invalid signature authorization tracking tokens payload metrics"})
		}

		claims := token.Claims.(jwt.MapClaims)
		c.Locals("user_id", claims["user_id"].(string))
		c.Locals("role", claims["role"].(string))

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
