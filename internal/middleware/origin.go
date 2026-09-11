package middleware

import (
	"github.com/gofiber/fiber/v2"
	"net/url"
	"strings"
)

// BrowserOriginGuard rejects browser-initiated mutations from sites outside the
// configured CORS allowlist. CORS alone does not stop simple POST side effects.
// Server integrations without Origin retain their existing authentication rules.
func BrowserOriginGuard(allowedOrigins string) fiber.Handler {
	allowed := strings.Split(allowedOrigins, ",")
	return func(c *fiber.Ctx) error {
		switch c.Method() {
		case fiber.MethodGet, fiber.MethodHead, fiber.MethodOptions:
			return c.Next()
		}
		origin := c.Get(fiber.HeaderOrigin)
		if origin == "" {
			return c.Next()
		}
		if !browserOriginAllowed(origin, allowed) {
			return c.Status(fiber.StatusForbidden).JSON(fiber.Map{"error": "request origin is not allowed"})
		}
		return c.Next()
	}
}

func browserOriginAllowed(origin string, allowed []string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	for _, raw := range allowed {
		candidate, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || candidate.Scheme != parsed.Scheme || candidate.User != nil || candidate.Path != "" || candidate.RawQuery != "" || candidate.Fragment != "" {
			continue
		}
		got, want := strings.ToLower(parsed.Host), strings.ToLower(candidate.Host)
		if got == want {
			return true
		}
		if strings.HasPrefix(want, "*.") && strings.HasSuffix(got, want[1:]) {
			return true
		}
	}
	return false
}
