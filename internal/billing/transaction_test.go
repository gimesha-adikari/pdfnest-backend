package billing

import (
	"errors"
	"os"
	"testing"
	"time"

	"pdfnest-backend/config"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func strictObserver(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(postgres.Open(os.Getenv("GIM6_TEST_DATABASE_URL")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(2)
	pool.SetMaxIdleConns(1)
	t.Cleanup(func() { _ = pool.Close() })
	return db
}

func testBinding(res *config.BillingReservation) ReservationBinding {
	return ReservationBinding{ID: res.ID, OwnerID: res.UserID, TaskID: res.TaskID, Kind: ReservationKindDatabase,
		ToolName: res.ToolName, Units: res.Units, PlanUnits: res.PlanUnits, CreditUnits: res.CreditUnits}
}

func TestStrictSettlementInCallerTransaction(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		used, plan, credits, wantUsed, wantCredits int
	}{
		{"plan", 0, 7, 0, 7, 50}, {"credits", 8, 0, 7, 8, 43}, {"mixed", 5, 3, 4, 8, 46},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db := setupGIM6IsolatedTestDB(t)
			observer := strictObserver(t)
			owner := createGIM6AccountWithState(t, db, tc.used, 50)
			res, err := Default.ReserveWithTaskID(owner, ImageToTextPDF, 0, 1, "/foundation", uuid.NewString())
			require.NoError(t, err)
			require.Equal(t, tc.plan, res.PlanUnits)
			require.Equal(t, tc.credits, res.CreditUnits)
			binding := testBinding(res)
			previous := config.DB
			config.DB = nil // A supplied transaction must be the only database dependency.
			err = db.Transaction(func(tx *gorm.DB) error { return Default.SettleReservationInTx(tx, binding, time.Now()) })
			config.DB = previous
			require.NoError(t, err)
			var sub config.Subscription
			require.NoError(t, observer.Where("user_id = ?", owner).First(&sub).Error)
			require.Equal(t, tc.wantUsed, sub.UsedUnits3h)
			require.Equal(t, tc.wantUsed, sub.UsedUnitsDaily)
			require.Equal(t, tc.wantUsed, sub.UsedUnitsMonthly)
			require.Equal(t, tc.wantCredits, sub.CustomCredits)
			var logs []config.UsageLog
			require.NoError(t, observer.Where("user_id = ?", owner).Find(&logs).Error)
			require.Len(t, logs, 1)
			require.Equal(t, 1, logs[0].PagesCount)
			require.Equal(t, tc.credits > 0, logs[0].IsCredit)
			require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return Default.SettleReservationInTx(tx, binding, time.Now()) }), ErrReservationCommitted)
			require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return Default.ReleaseReservationInTx(tx, binding, time.Now()) }), ErrReservationCommitted)
		})
	}
}

func TestStrictReservationRejections(t *testing.T) {
	for _, name := range []string{"missing", "released", "committed", "owner", "task", "kind", "tool", "allocation", "negative", "expired", "unknown_state", "zero_time"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db := setupGIM6IsolatedTestDB(t)
			observer := strictObserver(t)
			owner := createGIM6AccountWithState(t, db, 0, 50)
			res, err := Default.ReserveWithTaskID(owner, ImageToTextPDF, 0, 1, "/foundation", uuid.NewString())
			require.NoError(t, err)
			b := testBinding(res)
			now := time.Now()
			expected := ErrReservationMismatch
			switch name {
			case "missing":
				b.ID = uuid.NewString()
				expected = gorm.ErrRecordNotFound
			case "released":
				require.NoError(t, Default.Release(res.ID))
				expected = ErrReservationReleased
			case "committed":
				require.NoError(t, Default.Commit(res.ID))
				expected = ErrReservationCommitted
			case "owner":
				b.OwnerID = uuid.NewString()
			case "task":
				b.TaskID = uuid.NewString()
			case "kind":
				b.Kind = ReservationKindGuest
			case "tool":
				b.ToolName = "different_tool"
			case "allocation":
				b.PlanUnits--
			case "negative":
				require.NoError(t, db.Model(res).Update("plan_units", -1).Error)
			case "expired":
				now = res.ExpiresAt.Add(time.Second)
				expected = ErrReservationExpired
			case "unknown_state":
				require.NoError(t, db.Model(res).Update("status", "unknown").Error)
				expected = ErrReservationState
			case "zero_time":
				now = time.Time{}
				expected = ErrReservationMismatch
			}
			require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return Default.SettleReservationInTx(tx, b, now) }), expected)
			var logs int64
			require.NoError(t, observer.Model(&config.UsageLog{}).Where("user_id = ?", owner).Count(&logs).Error)
			if name == "committed" {
				require.EqualValues(t, 1, logs)
			} else {
				require.Zero(t, logs)
			}
		})
	}
}

func TestStrictTransactionRollbackAndRelease(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	observer := strictObserver(t)
	owner := createGIM6AccountWithState(t, db, 0, 50)
	res, err := Default.ReserveWithTaskID(owner, ImageToTextPDF, 0, 1, "/foundation", uuid.NewString())
	require.NoError(t, err)
	b := testBinding(res)
	require.ErrorIs(t, Default.SettleReservationInTx(db, b, time.Now()), ErrTransactionRequired)
	require.ErrorIs(t, Default.ReleaseReservationInTx(nil, b, time.Now()), ErrTransactionRequired)
	abort := errors.New("controlled rollback")
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error {
		if err := Default.SettleReservationInTx(tx, b, time.Now()); err != nil {
			return err
		}
		return abort
	}), abort)
	var persisted config.BillingReservation
	require.NoError(t, observer.First(&persisted, "id = ?", res.ID).Error)
	require.Equal(t, "reserved", persisted.Status)
	var sub config.Subscription
	require.NoError(t, observer.Where("user_id = ?", owner).First(&sub).Error)
	require.Zero(t, sub.UsedUnits3h)
	require.Zero(t, sub.UsedUnitsDaily)
	require.Zero(t, sub.UsedUnitsMonthly)
	var count int64
	require.NoError(t, observer.Model(&config.UsageLog{}).Where("user_id = ?", owner).Count(&count).Error)
	require.Zero(t, count)
	require.Equal(t, 50, sub.CustomCredits)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return Default.ReleaseReservationInTx(tx, b, time.Now()) }))
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return Default.ReleaseReservationInTx(tx, b, time.Now()) }), ErrReservationReleased)
	require.ErrorIs(t, db.Transaction(func(tx *gorm.DB) error { return Default.SettleReservationInTx(tx, b, time.Now()) }), ErrReservationReleased)
}

func TestStrictSettlementSynchronizesAllWindows(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	db := setupGIM6IsolatedTestDB(t)
	observer := strictObserver(t)
	owner := createGIM6AccountWithState(t, db, 0, 50)
	res, err := Default.ReserveWithTaskID(owner, ImageToTextPDF, 0, 1, "/foundation", uuid.NewString())
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Microsecond)
	require.NoError(t, db.Model(&config.Subscription{}).Where("user_id = ?", owner).Updates(map[string]any{
		"used_units3h": 7, "used_units_daily": 19, "used_units_monthly": 79, "window3_h_reset_at": now.Add(-time.Second), "window_daily_reset_at": now.Add(-time.Second), "window_monthly_reset_at": now.Add(-time.Second),
	}).Error)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return Default.SettleReservationInTx(tx, testBinding(res), now) }))
	var sub config.Subscription
	require.NoError(t, observer.Where("user_id = ?", owner).First(&sub).Error)
	require.Equal(t, 7, sub.UsedUnits3h)
	require.Equal(t, 7, sub.UsedUnitsDaily)
	require.Equal(t, 7, sub.UsedUnitsMonthly)
	require.Equal(t, 50, sub.CustomCredits)
	require.True(t, sub.Window3HResetAt.After(now))
	require.True(t, sub.WindowDailyResetAt.After(now))
	require.True(t, sub.WindowMonthlyResetAt.After(now))
	var logs int64
	require.NoError(t, observer.Model(&config.UsageLog{}).Where("user_id = ?", owner).Count(&logs).Error)
	require.EqualValues(t, 1, logs)
}
