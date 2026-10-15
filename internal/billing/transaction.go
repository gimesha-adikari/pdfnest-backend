package billing

import (
	"errors"
	"time"

	"pdfnest-backend/config"

	"github.com/google/uuid"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrTransactionRequired  = errors.New("caller-owned PostgreSQL transaction required")
	ErrReservationMismatch  = errors.New("reservation binding does not match")
	ErrReservationCommitted = errors.New("reservation already committed")
	ErrReservationReleased  = errors.New("reservation already released")
	ErrReservationState     = errors.New("unexpected reservation state")
	ErrReservationExpired   = errors.New("reservation expired")
)

// ReservationBinding is the exact captured allocation, not a request to price
// work again. Strict methods are dormant: legacy handlers do not call them.
type ReservationBinding struct {
	ID, OwnerID, TaskID, ToolName string
	Kind                          ReservationKind
	Units, PlanUnits, CreditUnits int
}

// RequirePostgresTransaction prevents accidentally settling through an
// autocommit handle. It neither begins nor commits the caller's transaction.
func RequirePostgresTransaction(tx *gorm.DB) error {
	if tx == nil || tx.Error != nil || tx.Statement == nil || tx.Dialector == nil || tx.Dialector.Name() != "postgres" {
		return ErrTransactionRequired
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrTransactionRequired
	}
	return nil
}

// LockReservationInTx checks identity/allocation before exposing its state to
// the caller. Lock order at the OCR boundary is task -> reservation -> subscription.
func (s *Service) LockReservationInTx(tx *gorm.DB, b ReservationBinding) (*config.BillingReservation, error) {
	if err := RequirePostgresTransaction(tx); err != nil {
		return nil, err
	}
	if b.Kind != ReservationKindDatabase || b.ID == "" || b.OwnerID == "" || b.TaskID == "" || b.ToolName == "" || b.Units < 0 || b.PlanUnits < 0 || b.CreditUnits < 0 || b.PlanUnits > b.Units || b.CreditUnits != b.Units-b.PlanUnits {
		return nil, ErrReservationMismatch
	}
	var res config.BillingReservation
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", b.ID).First(&res).Error; err != nil {
		return nil, err
	}
	if res.UserID != b.OwnerID || res.TaskID != b.TaskID || res.ToolName != b.ToolName || res.Units != b.Units || res.PlanUnits != b.PlanUnits || res.CreditUnits != b.CreditUnits {
		return nil, ErrReservationMismatch
	}
	switch res.Status {
	case "reserved", "committed", "released":
		return &res, nil
	default:
		return nil, ErrReservationState
	}
}

func strictReserved(res *config.BillingReservation) error {
	switch res.Status {
	case "committed":
		return ErrReservationCommitted
	case "released":
		return ErrReservationReleased
	case "reserved":
		return nil
	default:
		return ErrReservationState
	}
}

// SettleReservationInTx applies the same arithmetic as legacy Commit, but
// released/missing/mismatched reservations cannot authorize a new success.
func (s *Service) SettleReservationInTx(tx *gorm.DB, b ReservationBinding, now time.Time) error {
	res, err := s.LockReservationInTx(tx, b)
	if err != nil {
		return err
	}
	if err = strictReserved(res); err != nil {
		return err
	}
	if now.IsZero() {
		return ErrReservationMismatch
	}
	if !now.Before(res.ExpiresAt) {
		return ErrReservationExpired
	}
	changed, err := commitLockedReservation(tx, res, now)
	if err != nil {
		return err
	}
	if !changed {
		return ErrReservationState
	}
	return nil
}

// ReleaseReservationInTx releases only the exact reserved allocation. It is
// not compensation for a committed charge. The caller owns rollback/commit.
func (s *Service) ReleaseReservationInTx(tx *gorm.DB, b ReservationBinding, now time.Time) error {
	res, err := s.LockReservationInTx(tx, b)
	if err != nil {
		return err
	}
	if err = strictReserved(res); err != nil {
		return err
	}
	if now.IsZero() {
		return ErrReservationMismatch
	}
	result := tx.Model(&config.BillingReservation{}).Where("id = ? AND status = ?", res.ID, "reserved").Updates(map[string]any{"status": "released", "updated_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrReservationState
	}
	return nil
}

// commitLockedReservation is shared with legacy Commit. Its caller must hold
// the reservation row lock; it never opens a separate transaction.
func commitLockedReservation(tx *gorm.DB, res *config.BillingReservation, now time.Time) (bool, error) {
	// Conditional SQL update: only update if status is still 'reserved'
	result := tx.Model(&config.BillingReservation{}).
		Where("id = ? AND status = ?", res.ID, "reserved").
		Updates(map[string]interface{}{
			"status":     "committed",
			"updated_at": now,
		})
	if result.Error != nil {
		return false, result.Error
	}
	if result.RowsAffected == 0 {
		return false, nil
	}

	var sub config.Subscription
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("user_id = ?", res.UserID).
		First(&sub).Error; err != nil {
		return false, err
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
		return false, err
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
	return true, tx.Create(&usage).Error
}
