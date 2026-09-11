package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"pdfnest-backend/internal/authn"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/google/uuid"
)

func Resolve(store *Store) fiber.Handler {
	return func(c *fiber.Ctx) error {
		id, role, ok, authErr := resolveAuthenticatedUser(c)
		if authErr != nil {
			if errors.Is(authErr, authn.ErrUnavailable) {
				return c.Status(503).JSON(fiber.Map{"error": "Authentication is temporarily unavailable"})
			}
			return c.Status(401).JSON(fiber.Map{"error": "Account session is unavailable"})
		}
		if ok {
			ident := Identity{
				ID:         id,
				Type:       TypeUser,
				Role:       role,
				Trust:      100,
				CreatedAt:  time.Now(),
				LastSeenAt: time.Now(),
			}

			c.Locals(LocalIdentityKey, ident)
			c.Locals(LocalIdentityIDKey, ident.ID)
			c.Locals(LocalIdentityType, string(ident.Type))
			c.Locals(LocalUserIDKey, ident.ID)
			c.Locals(LocalUserRoleKey, ident.Role)
			return c.Next()
		}

		if store == nil {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "identity store not configured",
			})
		}

		ctx := RequestContext(c)
		now := time.Now()

		fpHash := fingerprintHash(c)
		uaHash := HashString(c.Get("User-Agent"))
		ipHash := HashString(c.IP())

		guestID := strings.TrimSpace(c.Cookies(CookieGuestID))
		if guestID == "" {
			guestID = strings.TrimSpace(c.Get("X-Platen-Guest"))
		}
		var guest *GuestRecord

		if guestID != "" {
			if g, err := store.LoadByID(ctx, guestID); err == nil {
				guest = g
			}
		}

		// Browser/network properties may group quota usage, but must never
		// recover a document owner's identity without its opaque credential.
		quotaID := ""
		if guest == nil && fpHash != "" {
			if g, err := store.LoadByFingerprint(ctx, fpHash); err == nil {
				quotaID = g.QuotaID
				if quotaID == "" {
					quotaID = g.ID
				}
			}
		}

		if guest == nil {
			guest = &GuestRecord{
				ID:              uuid.NewString(),
				QuotaID:         quotaID,
				FingerprintHash: fpHash,
				UserAgentHash:   uaHash,
				IPHash:          ipHash,
				Trust:           1,
				CreatedAt:       now,
				LastSeenAt:      now,
			}
		} else {
			if guest.FingerprintHash == "" && fpHash != "" {
				guest.FingerprintHash = fpHash
			}
			if guest.UserAgentHash == "" && uaHash != "" {
				guest.UserAgentHash = uaHash
			}
			if guest.IPHash == "" && ipHash != "" {
				guest.IPHash = ipHash
			}
			if guest.Trust <= 0 {
				guest.Trust = 1
			}
		}

		if guest.QuotaID == "" {
			guest.QuotaID = guest.ID
		}
		if err := store.Touch(ctx, guest); err != nil {
			log.Warnf("guest touch failed: %v", err)
		}

		setGuestCookie(c, guest.ID)

		ident := Identity{
			ID:              guest.ID,
			QuotaID:         guest.QuotaID,
			Type:            TypeGuest,
			GuestCookie:     guest.ID,
			FingerprintHash: guest.FingerprintHash,
			UserAgentHash:   guest.UserAgentHash,
			IPHash:          guest.IPHash,
			Trust:           guest.Trust,
			CreatedAt:       guest.CreatedAt,
			LastSeenAt:      guest.LastSeenAt,
		}

		c.Locals(LocalIdentityKey, ident)
		c.Locals(LocalIdentityIDKey, ident.ID)
		c.Locals(LocalIdentityType, string(ident.Type))
		c.Locals(LocalUserIDKey, ident.ID)
		return c.Next()
	}
}

func resolveAuthenticatedUser(c *fiber.Ctx) (userID, role string, ok bool, err error) {
	if id, ok := c.Locals(LocalUserIDKey).(string); ok && strings.TrimSpace(id) != "" {
		role, _ = c.Locals(LocalUserRoleKey).(string)
		return id, role, true, nil
	}

	tokenString := strings.TrimSpace(c.Cookies("auth_token"))
	if tokenString == "" {
		authHeader := strings.TrimSpace(c.Get("Authorization"))
		if strings.HasPrefix(authHeader, "Bearer ") {
			tokenString = strings.TrimPrefix(authHeader, "Bearer ")
		}
	}
	if tokenString == "" {
		return "", "", false, nil
	}

	user, verifyErr := authn.Verify(c.UserContext(), tokenString)
	if errors.Is(verifyErr, authn.ErrInvalid) {
		return "", "", false, nil
	}
	if verifyErr != nil {
		return "", "", false, verifyErr
	}
	return user.ID, user.Role, true, nil
}

func fingerprintHash(c *fiber.Ctx) string {
	fp := strings.TrimSpace(c.Get(HeaderFingerprint))
	ua := strings.TrimSpace(c.Get("User-Agent"))
	ip := strings.TrimSpace(c.IP())

	base := fp + "|" + ua + "|" + ip
	return HashString(base)
}

func HashString(s string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(s)))
	return hex.EncodeToString(sum[:])
}

func setGuestCookie(c *fiber.Ctx, guestID string) {
	isProduction := os.Getenv("APP_ENV") == "production"

	cookie := &fiber.Cookie{
		Name:     CookieGuestID,
		Value:    guestID,
		Path:     "/",
		Expires:  time.Now().Add(90 * 24 * time.Hour),
		HTTPOnly: true,
		Secure:   isProduction,
	}

	if isProduction {
		cookie.SameSite = "None"
	} else {
		cookie.SameSite = "Lax"
	}

	c.Cookie(cookie)
}
