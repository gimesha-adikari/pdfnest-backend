package auth

import (
	"errors"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"
)

type SessionGuest struct {
	ID         string    `json:"id"`
	Trust      int       `json:"trust"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
}

type SessionUser struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	Role          string    `json:"role"`
	Status        string    `json:"status,omitempty"`
	GoogleID      *string   `json:"google_id,omitempty"`
	EmailVerified bool      `json:"email_verified,omitempty"`
	CreatedAt     time.Time `json:"created_at,omitempty"`
	UpdatedAt     time.Time `json:"updated_at,omitempty"`
}

type SessionSubscription struct {
	Tier                 string    `json:"tier"`
	Status               string    `json:"status"`
	BillingInterval      string    `json:"billing_interval"`
	CurrentPeriodEnd     time.Time `json:"current_period_end"`
	CustomCredits        int       `json:"custom_credits"`
	UsedUnits3h          int       `json:"used_units_3h"`
	UsedUnitsDaily       int       `json:"used_units_daily"`
	UsedUnitsMonthly     int       `json:"used_units_monthly"`
	UpdateURL            string    `json:"update_url,omitempty"`
	CancelURL            string    `json:"cancel_url,omitempty"`
	Window3HResetAt      time.Time `json:"window_3h_reset_at"`
	WindowDailyResetAt   time.Time `json:"window_daily_reset_at"`
	WindowMonthlyResetAt time.Time `json:"window_monthly_reset_at"`
}

type SessionResponse struct {
	Authenticated bool                 `json:"authenticated"`
	Type          string               `json:"type"`
	User          *SessionUser         `json:"user,omitempty"`
	Guest         *SessionGuest        `json:"guest,omitempty"`
	Subscription  *SessionSubscription `json:"subscription,omitempty"`
}

func (ctrl *Controller) Session(c *fiber.Ctx) error {
	ident, ok := identity.FromContext(c)
	if !ok {
		return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "session identity not available",
		})
	}

	if ident.IsGuest() {
		return c.JSON(SessionResponse{
			Authenticated: true,
			Type:          "guest",
			Guest: &SessionGuest{
				ID:         ident.ID,
				Trust:      ident.Trust,
				CreatedAt:  ident.CreatedAt,
				LastSeenAt: ident.LastSeenAt,
			},
		})
	}

	userID := ident.ID

	var user config.User
	if err := config.DB.First(&user, "id = ?", userID).Error; err != nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{
			"error": "user not found",
		})
	}

	var sub config.Subscription
	if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := ensureFreeSubscription(config.DB, userID); err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
					"error": "failed to prepare subscription record",
				})
			}

			if err := config.DB.Where("user_id = ?", userID).First(&sub).Error; err != nil {
				return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
					"error": "failed to load subscription record",
				})
			}
		} else {
			return c.Status(fiber.StatusInternalServerError).JSON(fiber.Map{
				"error": "failed to load subscription record",
			})
		}
	}

	return c.JSON(SessionResponse{
		Authenticated: true,
		Type:          "user",
		User: &SessionUser{
			ID:            user.ID,
			Email:         user.Email,
			Role:          user.Role,
			Status:        user.Status,
			GoogleID:      user.GoogleID,
			EmailVerified: user.EmailVerified,
			CreatedAt:     user.CreatedAt,
			UpdatedAt:     user.UpdatedAt,
		},
		Subscription: &SessionSubscription{
			Tier:                 sub.Tier,
			Status:               sub.Status,
			BillingInterval:      sub.BillingInterval,
			CurrentPeriodEnd:     sub.CurrentPeriodEnd,
			CustomCredits:        sub.CustomCredits,
			UsedUnits3h:          sub.UsedUnits3h,
			UsedUnitsDaily:       sub.UsedUnitsDaily,
			UsedUnitsMonthly:     sub.UsedUnitsMonthly,
			UpdateURL:            sub.UpdateURL,
			CancelURL:            sub.CancelURL,
			Window3HResetAt:      sub.Window3HResetAt,
			WindowDailyResetAt:   sub.WindowDailyResetAt,
			WindowMonthlyResetAt: sub.WindowMonthlyResetAt,
		},
	})
}
