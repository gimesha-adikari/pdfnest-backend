package billing

import (
	"context"
	"pdfnest-backend/internal/tasks"
)

var GuestQuota *GuestQuotaStore

func Initialize(guestQuota *GuestQuotaStore) {
	GuestQuota = guestQuota
	tasks.StaleTaskBillingHandlerWithKind = func(reservationID, reservationKind string) {
		_ = Default.ReleaseAsync(context.Background(), reservationID, ReservationKind(reservationKind))
	}
	tasks.CommitTaskBillingHandlerWithKind = func(reservationID, reservationKind string) {
		_ = Default.CommitAsync(context.Background(), reservationID, ReservationKind(reservationKind))
	}

	// Keep the legacy callbacks for tasks created before reservation kind was
	// persisted. Empty/legacy kinds intentionally retain the old database
	// behavior; newly created async tasks use the kind-aware callbacks above.
	tasks.StaleTaskBillingHandler = func(reservationID string) {
		_ = Default.Release(reservationID)
	}
	tasks.CommitTaskBillingHandler = func(reservationID string) {
		if guestQuota != nil {
			_ = guestQuota.Commit(context.Background(), reservationID)
		}
		_ = Default.Commit(reservationID)
	}
}
