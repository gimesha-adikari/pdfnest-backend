package api

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/identity"
)

func TestAdmissionIdentityCannotBeSelectedByRequest(t *testing.T) {
	for _, tc := range []struct {
		name        string
		trusted     *identity.Identity
		owner, want string
	}{
		{name: "spoofed headers and JSON", owner: "alice"},
		{name: "guest", trusted: &identity.Identity{ID: "alice", Type: identity.TypeGuest}, owner: "alice"},
		{name: "different authenticated owner", trusted: &identity.Identity{ID: "bob", Type: identity.TypeUser}, owner: "alice"},
		{name: "verified account owner", trusted: &identity.Identity{ID: "alice", Type: identity.TypeUser}, owner: "alice", want: "alice"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := fiber.New()
			app.Post("/", func(c *fiber.Ctx) error {
				if tc.trusted != nil {
					c.Locals(identity.LocalIdentityKey, *tc.trusted)
				}
				var req AnalyzeRequest
				require.NoError(t, c.BodyParser(&req))
				require.Empty(t, req.admissionIdentity, "JSON must not set the server-only field")
				req.admissionIdentity = authenticatedAdmissionIdentity(c, tc.owner)
				require.Equal(t, tc.want, req.admissionIdentity)
				return c.SendStatus(204)
			})
			req := httptest.NewRequest("POST", "/", bytes.NewBufferString(`{"admissionIdentity":"alice","AdmissionIdentity":"alice","admissionIdentity":"user:alice"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(identity.HeaderFingerprint, "alice")
			req.Header.Set("Cookie", identity.CookieGuestID+"=alice")
			resp, err := app.Test(req)
			require.NoError(t, err)
			require.Equal(t, 204, resp.StatusCode)
			resp.Body.Close()
		})
	}
}
