package billing

import (
	"errors"
	"log"
	"pdfnest-backend/config"
	"strconv"
	"strings"
	"time"

	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/pdfcpu/pdfcpu/pkg/api"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Service struct{}

var Default = NewService()

func NewService() *Service {
	return &Service{}
}

var (
	ErrBillingBlocked = errors.New("billing quota exceeded")
	ErrBillingMissing = errors.New("subscription data not found")
)

type reservationTotals struct {
	Units       int
	PlanUnits   int
	CreditUnits int
}

func (s *Service) Reserve(userID string, tool Tool, pages, images int, requestPath string) (*config.BillingReservation, error) {
	return s.ReserveWithTaskID(userID, tool, pages, images, requestPath, "")
}

func (s *Service) ReserveWithTaskID(userID string, tool Tool, pages, images int, requestPath string, taskID string) (*config.BillingReservation, error) {
	now := time.Now()
	units := tool.Units(pages, images)

	var reservation *config.BillingReservation

	err := config.DB.Transaction(func(tx *gorm.DB) error {
		var sub config.Subscription
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", userID).
			First(&sub).Error; err != nil {
			return ErrBillingMissing
		}

		syncWindows(&sub, now)
		if err := tx.Save(&sub).Error; err != nil {
			return err
		}

		totals, err := activeReservationTotals(tx, userID, now)
		if err != nil {
			return err
		}

		limits := GetTierLimits(sub.Tier)

		available3H := limits.Units3H - (sub.UsedUnits3h + totals.PlanUnits)
		if available3H < 0 {
			available3H = 0
		}

		availableDaily := limits.UnitsDay - (sub.UsedUnitsDaily + totals.PlanUnits)
		if availableDaily < 0 {
			availableDaily = 0
		}

		availableMonthly := limits.UnitsMonth - (sub.UsedUnitsMonthly + totals.PlanUnits)
		if availableMonthly < 0 {
			availableMonthly = 0
		}

		planUnits := units
		if planUnits > available3H {
			planUnits = available3H
		}
		if planUnits > availableDaily {
			planUnits = availableDaily
		}
		if planUnits > availableMonthly {
			planUnits = availableMonthly
		}

		creditUnits := units - planUnits

		availableCredits := sub.CustomCredits - totals.CreditUnits
		if availableCredits < 0 {
			availableCredits = 0
		}

		log.Print(available3H)

		if creditUnits > availableCredits {
			if availableCredits > 0 {
				return CreditsExhaustedError(units)
			}

			if available3H < units && planUnits == available3H {
				log.Print(available3H)
				return HourlyLimitError(units)
			}
			if availableDaily < units && planUnits == availableDaily {
				return DailyLimitError(units)
			}
			if availableMonthly < units && planUnits == availableMonthly {
				return MonthlyLimitError(units)
			}
			return CreditsExhaustedError(units)
		}

		expiresAt := now.Add(6 * time.Hour)
		if taskID != "" {
			expiresAt = now.Add(30 * time.Minute)
		}

		reservation = &config.BillingReservation{
			ID:          uuid.New().String(),
			UserID:      userID,
			TaskID:      taskID,
			ToolName:    tool.Name,
			PagesCount:  pages,
			ImagesCount: images,
			Units:       units,
			PlanUnits:   planUnits,
			CreditUnits: creditUnits,
			Status:      "reserved",
			RequestPath: requestPath,
			ExpiresAt:   expiresAt,
			CreatedAt:   now,
			UpdatedAt:   now,
		}

		return tx.Create(reservation).Error
	})

	return reservation, err
}

func (s *Service) Commit(reservationID string) error {
	if strings.TrimSpace(reservationID) == "" {
		return nil
	}

	now := time.Now()

	return config.DB.Transaction(func(tx *gorm.DB) error {
		var res config.BillingReservation
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", reservationID).
			First(&res).Error; err != nil {
			return err
		}

		if res.Status != "reserved" {
			return nil
		}

		// Conditional SQL update: only update if status is still 'reserved'
		result := tx.Model(&config.BillingReservation{}).
			Where("id = ? AND status = ?", reservationID, "reserved").
			Updates(map[string]interface{}{
				"status":     "committed",
				"updated_at": now,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return nil
		}

		var sub config.Subscription
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("user_id = ?", res.UserID).
			First(&sub).Error; err != nil {
			return err
		}

		syncWindows(&sub, now)

		sub.UsedUnits3h += res.PlanUnits
		sub.UsedUnitsDaily += res.PlanUnits
		sub.UsedUnitsMonthly += res.PlanUnits

		sub.CustomCredits -= res.CreditUnits
		if sub.CustomCredits < 0 {
			sub.CustomCredits = 0
		}
		sub.UpdatedAt = now

		if err := tx.Save(&sub).Error; err != nil {
			return err
		}

		workCount := res.PagesCount
		if workCount == 0 {
			workCount = res.ImagesCount
		}

		usage := config.UsageLog{
			ID:         uuid.New().String(),
			UserID:     res.UserID,
			ToolName:   res.ToolName,
			IsCredit:   res.CreditUnits > 0,
			PagesCount: workCount,
			CreatedAt:  now,
		}
		return tx.Create(&usage).Error
	})
}

func (s *Service) Release(reservationID string) error {
	if strings.TrimSpace(reservationID) == "" {
		return nil
	}

	now := time.Now()

	// Conditional SQL update: transition status to 'released' only if currently 'reserved'
	return config.DB.Model(&config.BillingReservation{}).
		Where("id = ? AND status = ?", reservationID, "reserved").
		Updates(map[string]interface{}{
			"status":     "released",
			"updated_at": now,
		}).Error
}

func activeReservationTotals(tx *gorm.DB, userID string, now time.Time) (reservationTotals, error) {
	var totals reservationTotals
	err := tx.Model(&config.BillingReservation{}).
		Select(
			"COALESCE(SUM(units), 0) AS units, "+
				"COALESCE(SUM(plan_units), 0) AS plan_units, "+
				"COALESCE(SUM(credit_units), 0) AS credit_units",
		).
		Where("user_id = ? AND status = ? AND expires_at > ?", userID, "reserved", now).
		Scan(&totals).Error
	return totals, err
}

func syncWindows(sub *config.Subscription, now time.Time) {
	if sub.Tier == "" {
		sub.Tier = "free"
	}

	if (sub.Tier == "pro" || sub.Tier == "plus") && !sub.CurrentPeriodEnd.IsZero() && now.After(sub.CurrentPeriodEnd) {
		sub.Tier = "free"
		sub.Status = "expired"
		sub.UpdateURL = ""
		sub.CancelURL = ""
	}

	if sub.Window3HResetAt.IsZero() || !now.Before(sub.Window3HResetAt) {
		sub.UsedUnits3h = 0
		sub.Window3HResetAt = now.Truncate(3 * time.Hour).Add(3 * time.Hour)
	}

	if sub.WindowDailyResetAt.IsZero() || !now.Before(sub.WindowDailyResetAt) {
		sub.UsedUnitsDaily = 0
		y, m, d := now.Date()
		sub.WindowDailyResetAt = time.Date(y, m, d, 23, 59, 59, 0, now.Location()).Add(time.Second)
	}

	if sub.WindowMonthlyResetAt.IsZero() || !now.Before(sub.WindowMonthlyResetAt) {
		sub.UsedUnitsMonthly = 0
		sub.WindowMonthlyResetAt = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, 1, 0)
	}
}

func CountUploadedPDFPages(c *fiber.Ctx, formField string) int {
	ctx := uploads.FromCtx(c)
	if ctx == nil {
		return 0
	}

	file, ok := ctx.First(formField)
	if !ok || file == nil || file.Path == "" {
		return 0
	}

	pages, err := api.PageCountFile(file.Path)
	if err != nil || pages <= 0 {
		return 0
	}
	return pages
}

func CountUploadedImages(c *fiber.Ctx, formField string) int {
	ctx := uploads.FromCtx(c)
	if ctx == nil {
		return 0
	}
	return len(ctx.All(formField))
}

func CountSelectedPages(selection string) int {
	selection = strings.TrimSpace(selection)
	if selection == "" {
		return 0
	}

	total := 0
	parts := strings.Split(selection, ",")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			r := strings.Split(part, "-")
			if len(r) != 2 {
				continue
			}
			start, err1 := strconv.Atoi(strings.TrimSpace(r[0]))
			end, err2 := strconv.Atoi(strings.TrimSpace(r[1]))
			if err1 != nil || err2 != nil || end < start {
				continue
			}
			total += end - start + 1
			continue
		}
		if _, err := strconv.Atoi(part); err == nil {
			total++
		}
	}
	return total
}

func ReserveFromRequest(c *fiber.Ctx, userID string, tool Tool) (*config.BillingReservation, error) {
	pages, images, err := EstimateFromRequest(c, tool)
	if err != nil {
		return nil, err
	}
	return Default.Reserve(userID, tool, pages, images, c.Path())
}

func EstimateFromRequest(c *fiber.Ctx, tool Tool) (pages, images int, err error) {
	if tool.Estimate == nil {
		return 0, 0, nil
	}
	return tool.Estimate(c)
}

func Finalize(reservationID string, success bool) error {
	if success {
		return Default.Commit(reservationID)
	}
	return Default.Release(reservationID)
}
