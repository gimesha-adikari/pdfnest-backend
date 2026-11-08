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

func TestResolveDoesNotUpgradeGuestWhenAppliedTwice(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewStore(client, 0)

	app := fiber.New()
	apiGroup := app.Group("/api")
	// main.go creates the tool group with identity middleware, then registers
	// OCR V2 routes on apiGroup with their own identity middleware.
	toolGroup := apiGroup.Group("", Resolve(store))
	toolGroup.Get("/tools/example", func(c *fiber.Ctx) error { return c.SendStatus(http.StatusNoContent) })
	apiGroup.Get("/v2/ocr/text/jobs", Resolve(store), func(c *fiber.Ctx) error {
		ident, ok := FromContext(c)
		if !ok {
			return c.SendStatus(http.StatusInternalServerError)
		}
		return c.JSON(fiber.Map{"identity_type": ident.Type, "identity_id": ident.ID})
	})

	request := httptest.NewRequest(http.MethodGet, "/api/v2/ocr/text/jobs", nil)
	request.Header.Set("User-Agent", "synthetic-identity-test")
	response, err := app.Test(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)

	var result struct {
		IdentityType Type   `json:"identity_type"`
		IdentityID   string `json:"identity_id"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.Equal(t, TypeGuest, result.IdentityType, "a repeated middleware pass must preserve the guest identity")
	require.NotEmpty(t, result.IdentityID)
}
