package conversion

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/identity"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestReserveAsyncBillingFreeGuestDoesNotRequireGuestQuotaStore(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	previousQuota := billing.GuestQuota
	billing.GuestQuota = nil
	t.Cleanup(func() { billing.GuestQuota = previousQuota })

	var lease *asyncBillingLease
	var reserveErr error
	var settleErr error
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityType, string(identity.TypeGuest))
		c.Locals(identity.LocalIdentityIDKey, "async-conversion-free-guest")
		return c.Next()
	})
	app.Post("/async-allocation", func(c *fiber.Ctx) error {
		lease, reserveErr = reserveAsyncBilling(
			c, string(identity.TypeGuest), "async-conversion-free-guest",
			billing.ConvertURLToPDF, 0, 0, c.Path(), "task-free-conversion",
		)
		if reserveErr != nil {
			return c.SendStatus(fiber.StatusInternalServerError)
		}
		lease.settle()
		settleErr = lease.commit()
		return c.SendStatus(fiber.StatusAccepted)
	})

	response, err := app.Test(httptest.NewRequest("POST", "/async-allocation", nil))
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusAccepted, response.StatusCode)
	require.NoError(t, reserveErr)
	require.NoError(t, settleErr)
	require.NotNil(t, lease)
	require.Empty(t, lease.reservationID)
}

func TestPDFToMarkdownGuestAllocationUsesFreePolicyWithoutGuestQuotaStore(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	previousQuota := billing.GuestQuota
	billing.GuestQuota = nil
	t.Cleanup(func() { billing.GuestQuota = previousQuota })

	reservation, err := reservePDFToMarkdownGuest(context.Background(), "pdf-markdown-free-guest", 9, 0, "/pdf-to-markdown")
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.Empty(t, reservation.ID)
	require.Zero(t, reservation.Units)
}

func TestPDFToMarkdownGuestAllocationKeepsNormalOwnerQuota(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	previousQuota := billing.GuestQuota
	billing.GuestQuota = billing.NewGuestQuotaStore(client, time.Hour)
	t.Cleanup(func() { billing.GuestQuota = previousQuota })

	ownerIdentity := "pdf-markdown-owner"
	reservation, err := reservePDFToMarkdownGuest(context.Background(), ownerIdentity, 0, 0, "/pdf-to-markdown")
	require.NoError(t, err)
	require.NotEmpty(t, reservation.ID)
	require.Equal(t, 3, reservation.Units)
	state, err := client.HGetAll(context.Background(), "platen:guestquota:state:"+ownerIdentity).Result()
	require.NoError(t, err)
	require.Equal(t, "3", state["pending_3h"])
	require.NoError(t, billing.GuestQuota.Release(context.Background(), reservation.ID))
}
