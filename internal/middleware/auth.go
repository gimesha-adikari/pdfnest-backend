package middleware

import (
	"os"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
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
			log.Fatal("CRITICAL SECURITY ERROR: JWT_SECRET is missing during auth check.")
			return c.Status(500).JSON(fiber.Map{"error": "Internal server configuration error"})
		}

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			return []byte(secret), nil
		})

		if err != nil || !token.Valid {
			return c.Status(401).JSON(fiber.Map{"error": "Invalid signature authorization tracking tokens payload metrics"})
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			return c.Status(401).JSON(fiber.Map{"error": "Invalid token claims structure"})
		}

		userID, ok := claims["user_id"].(string)
		if !ok || strings.TrimSpace(userID) == "" {
			return c.Status(401).JSON(fiber.Map{"error": "Invalid or missing user identity in token"})
		}

		role, ok := claims["role"].(string)
		if !ok || strings.TrimSpace(role) == "" {
			return c.Status(401).JSON(fiber.Map{"error": "Invalid or missing role claim in token"})
		}

		c.Locals("user_id", userID)
		c.Locals("role", role)

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
