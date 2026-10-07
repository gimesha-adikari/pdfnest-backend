package conversion

import (
	"net/http/httptest"
	"reflect"
	"testing"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/identity"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
)

func TestAsyncBillingLeaseRetainsReservationKindForTaskMetadata(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	previousDB := config.DB
	previousQuota := billing.GuestQuota
	config.DB = nil
	billing.GuestQuota = nil
	t.Cleanup(func() {
		config.DB = previousDB
		billing.GuestQuota = previousQuota
	})

	var guestKind, accountKind string
	var missingKindField string
	app := fiber.New()
	app.Post("/test-lease", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: "gim7-guest", QuotaID: "gim7-guest", Type: identity.TypeGuest})
		c.Locals(identity.LocalIdentityIDKey, "gim7-guest")
		tool := billing.Tool{Name: "gim7-lease", BaseUnits: 1, Estimate: billing.EstimateNone()}

		guestLease, err := reserveAsyncBilling(c, string(identity.TypeGuest), "gim7-guest", tool, 0, 0, "/gim7/guest", "task-guest")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		guestKind, missingKindField = asyncLeaseReservationKind(guestLease)

		accountLease, err := reserveAsyncBilling(c, "account", "gim7-account", tool, 0, 0, "/gim7/account", "task-account")
		if err != nil {
			return c.Status(fiber.StatusInternalServerError).SendString(err.Error())
		}
		accountKindValue, accountMissingKindField := asyncLeaseReservationKind(accountLease)
		accountKind = accountKindValue
		if missingKindField == "" {
			missingKindField = accountMissingKindField
		}
		return c.SendStatus(fiber.StatusNoContent)
	})

	resp, err := app.Test(httptest.NewRequest("POST", "/test-lease", nil))
	require.NoError(t, err)
	require.Equal(t, fiber.StatusNoContent, resp.StatusCode)
	require.Empty(t, missingKindField)
	require.Equal(t, string(billing.ReservationKindGuest), guestKind)
	require.Equal(t, string(billing.ReservationKindDatabase), accountKind)
}

func asyncLeaseReservationKind(lease *asyncBillingLease) (string, string) {
	field := reflect.ValueOf(lease).Elem().FieldByName("reservationKind")
	if !field.IsValid() || field.Kind() != reflect.String {
		return "", "async billing lease does not retain reservationKind"
	}
	return field.String(), ""
}
