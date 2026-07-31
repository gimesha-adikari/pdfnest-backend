package middleware

import (
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/golang-jwt/jwt/v5"
)

func OptionalAuth() fiber.Handler {
	return func(c *fiber.Ctx) error {
		tokenString := c.Cookies("auth_token")

		if tokenString == "" {
			return c.Next()
		}

		secret := os.Getenv("JWT_SECRET")
		if secret == "" {
			log.Error("JWT_SECRET is missing during optional auth check.")
			return c.Next()
		}

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			return []byte(secret), nil
		})

		if err != nil || !token.Valid {
			return c.Next()
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			return c.Next()
		}

		userID, ok := claims["user_id"].(string)
		if ok {
			c.Locals("user_id", userID)
		}

		role, ok := claims["role"].(string)
		if ok {
			c.Locals("role", role)
		}

		return c.Next()
	}
}
