package billing

import "pdfnest-backend/internal/tasks"

var GuestQuota *GuestQuotaStore

func Initialize(guestQuota *GuestQuotaStore) {
	GuestQuota = guestQuota
	tasks.StaleTaskBillingHandler = func(reservationID string) {
		_ = Default.Release(reservationID)
	}
}
