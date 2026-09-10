package identity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGuestIdentityRequiresCredentialForRecovery(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	previous := DefaultStore
	t.Cleanup(func() { DefaultStore = previous })
	app := fiber.New()
	app.Use(Resolve(NewStore(client, 0)))
	app.Get("/session", func(c *fiber.Ctx) error {
		ident, _ := FromContext(c)
		return c.JSON(ident)
	})
	request := func(cookie *http.Cookie, fingerprint string) (Identity, *http.Cookie) {
		req := httptest.NewRequest(http.MethodGet, "/session", nil)
		req.Header.Set("User-Agent", "Shared browser version")
		req.Header.Set(HeaderFingerprint, fingerprint)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		resp, err := app.Test(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var ident Identity
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&ident))
		return ident, resp.Cookies()[0]
	}
	for _, fingerprint := range []string{"", "shared-fingerprint"} {
		first, cookie := request(nil, fingerprint)
		second, _ := request(nil, fingerprint)
		require.NotEqual(t, first.ID, second.ID, "network/browser properties must not recover document ownership")
		require.Equal(t, first.QuotaID, second.QuotaID, "separate document owners retain existing quota grouping")
		continued, _ := request(cookie, fingerprint)
		require.Equal(t, first.ID, continued.ID, "a valid guest cookie preserves ownership")
	}
}
