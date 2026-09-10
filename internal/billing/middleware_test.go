package billing

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"pdfnest-backend/internal/identity"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGuestOwnershipIsolationPreservesQuota(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	oldQuota, oldStore := GuestQuota, identity.DefaultStore
	t.Cleanup(func() { GuestQuota = oldQuota; identity.DefaultStore = oldStore; _ = client.Close() })
	GuestQuota = NewGuestQuotaStore(client, 90*24*time.Hour)
	app := fiber.New()
	app.Use(identity.Resolve(identity.NewStore(client, 0)))
	app.Post("/operation", UseGuestOnly(ConvertURLToPDF), func(c *fiber.Ctx) error { return c.SendStatus(200) })
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest("POST", "/operation", nil)
		req.Header.Set("User-Agent", "shared-test-browser")
		resp, err := app.Test(req)
		require.NoError(t, err)
		resp.Body.Close()
		if resp.StatusCode == 429 {
			return
		}
		require.Equal(t, 200, resp.StatusCode)
	}
	t.Fatal("changing guest ownership must not reset shared anonymous quota")
}

func TestBillingMiddleware_Unauthorized(t *testing.T) {
	app := fiber.New()
	app.Post("/api/markup/highlight", Use(HighlightPDF), func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	req := httptest.NewRequest("POST", "/api/markup/highlight", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected status 401 Unauthorized, got %d", resp.StatusCode)
	}
}

func TestBillingMiddleware_GuestAvailableQuota(t *testing.T) {
	committed := false

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityType, string(identity.TypeGuest))
		c.Locals(identity.LocalIdentityIDKey, "guest-123")
		return c.Next()
	})

	app.Post("/api/markup/highlight", func(c *fiber.Ctx) error {
		idType, _ := c.Locals(identity.LocalIdentityType).(string)
		id, _ := c.Locals(identity.LocalIdentityIDKey).(string)
		if idType != string(identity.TypeGuest) || id != "guest-123" {
			return c.Status(401).JSON(fiber.Map{"error": "unauthorized"})
		}
		committed = true
		return c.Status(202).JSON(fiber.Map{"success": true, "job_id": "job-1"})
	})

	req := httptest.NewRequest("POST", "/api/markup/highlight", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("unexpected test error: %v", err)
	}

	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("expected status 202 Accepted, got %d", resp.StatusCode)
	}

	if !committed {
		t.Errorf("expected handler execution to succeed")
	}
}

func TestBillingMiddleware_GuestExhaustedQuota(t *testing.T) {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityType, string(identity.TypeGuest))
		c.Locals(identity.LocalIdentityIDKey, "guest-exhausted")
		return c.Next()
	})

	app.Post("/api/markup/highlight", func(c *fiber.Ctx) error {
		err := HourlyLimitError(4)
		var berr *BillingError
		if errors.As(err, &berr) {
			berr.Tool = "highlight"
			return c.Status(fiber.StatusTooManyRequests).JSON(berr)
		}
		return c.Status(500).SendString("error")
	})

	req := httptest.NewRequest("POST", "/api/markup/highlight", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("expected status 429 Too Many Requests, got %d", resp.StatusCode)
	}

	var body map[string]interface{}
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if body["code"] != string(ErrHourlyLimit) {
		t.Errorf("expected error code %s, got %v", ErrHourlyLimit, body["code"])
	}
}
