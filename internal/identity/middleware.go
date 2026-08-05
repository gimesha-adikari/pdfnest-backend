package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/log"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func Resolve(store *Store) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if id, role, ok := resolveAuthenticatedUser(c); ok {
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
		uaHash := hashString(c.Get("User-Agent"))
		ipHash := hashString(c.IP())

		guestID := strings.TrimSpace(c.Cookies(CookieGuestID))
		var guest *GuestRecord

		if guestID != "" {
			if g, err := store.LoadByID(ctx, guestID); err == nil {
				guest = g
			}
		}

		if guest == nil && fpHash != "" {
			if g, err := store.LoadByFingerprint(ctx, fpHash); err == nil {
				guest = g
			}
		}

		if guest == nil {
			guest = &GuestRecord{
				ID:              uuid.NewString(),
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

		if err := store.Touch(ctx, guest); err != nil {
			log.Warnf("guest touch failed: %v", err)
		}

		setGuestCookie(c, guest.ID)

		ident := Identity{
			ID:              guest.ID,
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
		return c.Next()
	}
}

func resolveAuthenticatedUser(c *fiber.Ctx) (userID, role string, ok bool) {
	if id, ok := c.Locals(LocalUserIDKey).(string); ok && strings.TrimSpace(id) != "" {
		role, _ = c.Locals(LocalUserRoleKey).(string)
		return id, role, true
	}

	tokenString := strings.TrimSpace(c.Cookies("auth_token"))
	if tokenString == "" {
		return "", "", false
	}

	secret := os.Getenv("JWT_SECRET")
	if secret == "" {
		return "", "", false
	}

	token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
		return []byte(secret), nil
	})
	if err != nil || !token.Valid {
		return "", "", false
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", "", false
	}

	id, _ := claims["user_id"].(string)
	role, _ = claims["role"].(string)
	if strings.TrimSpace(id) == "" {
		return "", "", false
	}

	return id, role, true
}

func fingerprintHash(c *fiber.Ctx) string {
	fp := strings.TrimSpace(c.Get(HeaderFingerprint))
	ua := strings.TrimSpace(c.Get("User-Agent"))
	ip := strings.TrimSpace(c.IP())

	base := fp + "|" + ua + "|" + ip
	return hashString(base)
}

func hashString(s string) string {
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
