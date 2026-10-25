package authn

import (
	"github.com/gofiber/fiber/v2"
	"os"
	"time"
)

// SetSessionCookie keeps issuance and deletion scoped identically.
func SetSessionCookie(c *fiber.Ctx, token string) {
	production := os.Getenv("APP_ENV") == "production"
	expires := time.Now().Add(24 * time.Hour)
	if token == "" {
		expires = time.Now().Add(-24 * time.Hour)
	}
	cookie := &fiber.Cookie{Name: "auth_token", Value: token, Path: "/", Expires: expires, HTTPOnly: true, Secure: production, SameSite: "Lax"}
	if production {
		cookie.SameSite = "None"
	}
	c.Cookie(cookie)
}
