package identity

import (
	"context"

	"github.com/gofiber/fiber/v2"
)

func FromContext(c *fiber.Ctx) (Identity, bool) {
	raw := c.Locals(LocalIdentityKey)
	if raw == nil {
		return Identity{}, false
	}

	ident, ok := raw.(Identity)
	if !ok {
		return Identity{}, false
	}

	return ident, true
}

func MustFromContext(c *fiber.Ctx) Identity {
	ident, _ := FromContext(c)
	return ident
}

// GuestQuotaKey preserves anonymous usage grouping independently of ownership.
func GuestQuotaKey(c *fiber.Ctx, fallback string) string {
	if ident, ok := FromContext(c); ok && ident.IsGuest() && ident.QuotaID != "" {
		return ident.QuotaID
	}
	return fallback
}

func UserIDFromContext(c *fiber.Ctx) (string, bool) {
	v, ok := c.Locals(LocalUserIDKey).(string)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

func RequestContext(c *fiber.Ctx) context.Context {
	if ctx := c.UserContext(); ctx != nil {
		return ctx
	}
	return context.Background()
}
