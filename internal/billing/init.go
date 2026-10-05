package billing

import (
	"context"
	"log"
	"pdfnest-backend/internal/tasks"
)

var GuestQuota *GuestQuotaStore

func Initialize(guestQuota *GuestQuotaStore) {
	GuestQuota = guestQuota
	tasks.StaleTaskBillingHandlerWithKind = func(reservationID, reservationKind string) {
		if err := Default.ReleaseAsync(context.Background(), reservationID, ReservationKind(reservationKind)); err != nil {
			log.Printf("[BILLING TASK FINALIZATION] release reservation %q failed: %v", reservationID, err)
		}
	}
	tasks.CommitTaskBillingHandlerWithKind = func(reservationID, reservationKind string) {
		if err := Default.CommitAsync(context.Background(), reservationID, ReservationKind(reservationKind)); err != nil {
			log.Printf("[BILLING TASK FINALIZATION] commit reservation %q failed: %v", reservationID, err)
		}
	}
	tasks.CancelTaskBillingHandlerWithKind = func(reservationID, reservationKind string) {
		if err := Default.ReleaseAsync(context.Background(), reservationID, ReservationKind(reservationKind)); err != nil {
			log.Printf("[BILLING TASK FINALIZATION] cancel reservation %q failed: %v", reservationID, err)
		}
	}

	// Keep the legacy callbacks for integrations that still pass only a
	// reservation ID. The async service resolves blank kinds against both stores.
	tasks.StaleTaskBillingHandler = func(reservationID string) {
		if err := Default.ReleaseAsync(context.Background(), reservationID, ""); err != nil {
			log.Printf("[BILLING TASK FINALIZATION] legacy release reservation %q failed: %v", reservationID, err)
		}
	}
	tasks.CommitTaskBillingHandler = func(reservationID string) {
		if err := Default.CommitAsync(context.Background(), reservationID, ""); err != nil {
			log.Printf("[BILLING TASK FINALIZATION] legacy commit reservation %q failed: %v", reservationID, err)
		}
	}
	tasks.CancelTaskBillingHandler = func(reservationID string) {
		if err := Default.ReleaseAsync(context.Background(), reservationID, ""); err != nil {
			log.Printf("[BILLING TASK FINALIZATION] legacy cancel reservation %q failed: %v", reservationID, err)
		}
	}
}
