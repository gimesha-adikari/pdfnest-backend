package middleware

import (
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCORSAloneAllowsSimplePostSideEffects(t *testing.T) {
	app := fiber.New()
	app.Use(cors.New(cors.Config{AllowOrigins: "https://platen.example", AllowCredentials: true}))
	called := false
	app.Post("/work", func(c *fiber.Ctx) error { called = true; return c.SendStatus(200) })
	req := httptest.NewRequest("POST", "/work", strings.NewReader("file=test"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://untrusted.example")
	req.Header.Set("Cookie", "auth_token=ambient-browser-cookie")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.True(t, called)
	require.Equal(t, 200, resp.StatusCode)
}
func TestBrowserOriginGuardPreventsUntrustedMutation(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   int
	}{
		{"https://platen.example", 200}, {"http://localhost:53000", 200}, {"", 200},
		{"https://untrusted.example", 403}, {"null", 403}, {"https://platen.example.attacker.test", 403},
		{"https://platen.example@attacker.test", 403}, {"https://platen.example/path", 403}, {"http://platen.example", 403},
	} {
		t.Run(tc.origin, func(t *testing.T) {
			app := fiber.New()
			app.Use(BrowserOriginGuard("https://platen.example,http://localhost:53000"))
			called := false
			app.Post("/work", func(c *fiber.Ctx) error { called = true; return c.SendStatus(200) })
			req := httptest.NewRequest("POST", "/work", strings.NewReader("file=test"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("Origin", tc.origin)
			req.Header.Set("Cookie", "auth_token=ambient-browser-cookie")
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.want, resp.StatusCode)
			require.Equal(t, tc.want == 200, called)
		})
	}
}
