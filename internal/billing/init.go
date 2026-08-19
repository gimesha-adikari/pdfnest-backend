package billing

import (
	"context"
	"pdfnest-backend/internal/tasks"
)

var GuestQuota *GuestQuotaStore

func Initialize(guestQuota *GuestQuotaStore) {
	GuestQuota = guestQuota
	tasks.StaleTaskBillingHandler = func(reservationID string) {
		_ = Default.Release(reservationID)
	}
	tasks.CommitTaskBillingHandler = func(reservationID string) {
		if guestQuota != nil {
			ctx := context.Background()
			_ = guestQuota.Commit(ctx, reservationID)
		}
		_ = Default.Commit(reservationID)
	}
}
