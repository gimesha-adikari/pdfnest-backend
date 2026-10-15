package billing

import (
	"errors"
	"testing"
	"time"

	"pdfnest-backend/config"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// Characterize the legacy wrapper before extracting its settlement arithmetic.
// These literal expectations also run unchanged after that extraction.
func TestLegacyTransactionFoundationBaseline(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		used, plan, credits, wantUsed, wantCredits int
	}{
		{"plan", 0, 7, 0, 7, 50},
		{"credits", 8, 0, 7, 8, 43},
		{"mixed", 5, 3, 4, 8, 46},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			db := setupGIM6IsolatedTestDB(t)
			owner := createGIM6AccountWithState(t, db, tc.used, 50)
			task := uuid.NewString()
			res, err := Default.ReserveWithTaskID(owner, ImageToTextPDF, 0, 1, "/foundation-baseline", task)
			require.NoError(t, err)
			require.Equal(t, tc.plan, res.PlanUnits)
			require.Equal(t, tc.credits, res.CreditUnits)
			require.WithinDuration(t, res.CreatedAt.Add(30*time.Minute), res.ExpiresAt, time.Second)
			require.NoError(t, Default.Commit(res.ID))
			require.NoError(t, Default.Commit(res.ID))
			require.NoError(t, Default.Release(res.ID))
			var sub config.Subscription
			require.NoError(t, db.Where("user_id = ?", owner).First(&sub).Error)
			require.Equal(t, tc.wantUsed, sub.UsedUnits3h)
			require.Equal(t, tc.wantUsed, sub.UsedUnitsDaily)
			require.Equal(t, tc.wantUsed, sub.UsedUnitsMonthly)
			require.Equal(t, tc.wantCredits, sub.CustomCredits)
			var logs []config.UsageLog
			require.NoError(t, db.Where("user_id = ?", owner).Find(&logs).Error)
			require.Len(t, logs, 1)
			require.Equal(t, 1, logs[0].PagesCount)
			require.Equal(t, tc.credits > 0, logs[0].IsCredit)
			res2, err := Default.ReserveWithTaskID(owner, Tool{Name: "baseline-small", BaseUnits: 1}, 0, 0, "/baseline", uuid.NewString())
			require.NoError(t, err)
			require.NoError(t, Default.Release(res2.ID))
			require.NoError(t, Default.Commit(res2.ID), "legacy released commit is a no-op")
			require.NoError(t, Default.Release(uuid.NewString()), "legacy missing release is a no-op")
			require.True(t, errors.Is(Default.Commit(uuid.NewString()), gorm.ErrRecordNotFound))
		})
	}
}
