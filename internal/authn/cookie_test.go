package authn

import (
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionCookieClearingMatchesIssuance(t *testing.T) {
	for _, environment := range []string{"development", "production"} {
		t.Run(environment, func(t *testing.T) {
			t.Setenv("APP_ENV", environment)
			app := fiber.New()
			app.Get("/issue", func(c *fiber.Ctx) error { SetSessionCookie(c, "synthetic-token"); return c.SendStatus(200) })
			app.Get("/clear", func(c *fiber.Ctx) error { SetSessionCookie(c, ""); return c.SendStatus(200) })
			issued, err := app.Test(httptest.NewRequest("GET", "/issue", nil))
			require.NoError(t, err)
			issued.Body.Close()
			cleared, err := app.Test(httptest.NewRequest("GET", "/clear", nil))
			require.NoError(t, err)
			cleared.Body.Close()
			require.Len(t, issued.Cookies(), 1)
			require.Len(t, cleared.Cookies(), 1)
			a, b := issued.Cookies()[0], cleared.Cookies()[0]
			require.Equal(t, a.Name, b.Name)
			require.Equal(t, a.Domain, b.Domain)
			require.Equal(t, a.Path, b.Path)
			require.Equal(t, a.HttpOnly, b.HttpOnly)
			require.Equal(t, a.Secure, b.Secure)
			require.Equal(t, a.SameSite, b.SameSite)
			require.Empty(t, b.Value)
			require.True(t, b.Expires.Before(time.Now()))
			if environment == "production" {
				require.True(t, b.Secure)
				require.Equal(t, http.SameSiteNoneMode, b.SameSite)
			} else {
				require.False(t, b.Secure)
				require.Equal(t, http.SameSiteLaxMode, b.SameSite)
			}
		})
	}
}
