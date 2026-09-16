package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"pdfnest-backend/config"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

func TestAuthenticatedAsyncReservationStillUsesDatabaseBilling(t *testing.T) {
	db := setupTestDB(t)
	if db == nil {
		t.Skip("isolated PostgreSQL is required")
	}
	userID, _ := createTestUserAndSub(db)
	reservation, err := Default.ReserveAsync(context.Background(), userID, "", ExtractTextPDF, 1, 0, "/api/ocr/extract-text-async", uuid.NewString())
	if err != nil {
		t.Fatalf("reserve authenticated async usage: %v", err)
	}
	if reservation.Kind != ReservationKindDatabase {
		t.Fatalf("expected database reservation kind, got %q", reservation.Kind)
	}

	var stored config.BillingReservation
	if err := db.Where("id = ?", reservation.ID).First(&stored).Error; err != nil {
		t.Fatalf("database reservation was not persisted: %v", err)
	}
	if err := Default.CommitAsync(context.Background(), reservation.ID, reservation.Kind); err != nil {
		t.Fatalf("commit authenticated async usage: %v", err)
	}
	if err := db.Where("id = ?", reservation.ID).First(&stored).Error; err != nil {
		t.Fatalf("reload committed reservation: %v", err)
	}
	if stored.Status != "committed" {
		t.Fatalf("expected committed database reservation, got %q", stored.Status)
	}
}

func TestGuestAsyncReservationFinalizationIsExactOnce(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	oldGuestQuota := GuestQuota
	GuestQuota = NewGuestQuotaStore(client, time.Hour)
	t.Cleanup(func() { GuestQuota = oldGuestQuota })

	ctx := context.Background()
	guestID := uuid.NewString()
	reservation, err := Default.ReserveAsync(ctx, guestID, guestID, ExtractTextPDF, 1, 0, "/api/ocr/extract-text-async", uuid.NewString())
	if err != nil {
		t.Fatalf("reserve guest async usage: %v", err)
	}
	if reservation.Kind != ReservationKindGuest {
		t.Fatalf("expected guest reservation kind, got %q", reservation.Kind)
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := Default.CommitAsync(ctx, reservation.ID, reservation.Kind); err != nil {
				t.Errorf("concurrent guest commit failed: %v", err)
			}
		}()
	}
	wg.Wait()

	state, err := client.HGetAll(ctx, GuestQuota.stateKey(guestID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if state["pending_3h"] != "0" || state["pending_day"] != "0" || state["pending_month"] != "0" {
		t.Fatalf("reservation remained pending: %#v", state)
	}
	if state["used_3h"] != "6" || state["used_day"] != "6" || state["used_month"] != "6" {
		t.Fatalf("expected one six-unit guest charge, got %#v", state)
	}
	if exists, err := client.Exists(ctx, GuestQuota.resKey(reservation.ID)).Result(); err != nil || exists != 0 {
		t.Fatalf("expected committed reservation to be deleted, exists=%d err=%v", exists, err)
	}

	// Repeated finalization and release are no-ops and cannot add or remove a
	// second charge after the reservation key has been deleted.
	if err := Default.CommitAsync(ctx, reservation.ID, reservation.Kind); err != nil {
		t.Fatalf("repeated guest commit failed: %v", err)
	}
	if err := Default.ReleaseAsync(ctx, reservation.ID, reservation.Kind); err != nil {
		t.Fatalf("release after commit failed: %v", err)
	}
	stateAfterRetry, err := client.HGetAll(ctx, GuestQuota.stateKey(guestID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if stateAfterRetry["used_3h"] != "6" {
		t.Fatalf("retries changed usage: %#v", stateAfterRetry)
	}
}

func TestGuestAsyncReservationFailureReleasesAndPreservesQuota(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	oldGuestQuota := GuestQuota
	GuestQuota = NewGuestQuotaStore(client, time.Hour)
	t.Cleanup(func() { GuestQuota = oldGuestQuota })

	ctx := context.Background()
	guestID := uuid.NewString()
	reservation, err := Default.ReserveAsync(ctx, guestID, guestID, ExtractTextPDF, 1, 0, "/api/ocr/extract-text-async", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if err := Default.ReleaseAsync(ctx, reservation.ID, reservation.Kind); err != nil {
		t.Fatal(err)
	}
	if err := Default.ReleaseAsync(ctx, reservation.ID, reservation.Kind); err != nil {
		t.Fatal(err)
	}

	state, err := client.HGetAll(ctx, GuestQuota.stateKey(guestID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	if state["pending_3h"] != "0" || state["used_3h"] != "0" {
		t.Fatalf("failed guest task leaked or consumed quota: %#v", state)
	}
}

func TestGuestAsyncReservationPreservesQuotaExhaustion(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	oldGuestQuota := GuestQuota
	GuestQuota = NewGuestQuotaStore(client, time.Hour)
	t.Cleanup(func() { GuestQuota = oldGuestQuota })

	ctx := context.Background()
	guestID := uuid.NewString()
	first, err := Default.ReserveAsync(ctx, guestID, guestID, ExtractTextPDF, 1, 0, "/api/ocr/extract-text-async", uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if err := Default.CommitAsync(ctx, first.ID, first.Kind); err != nil {
		t.Fatal(err)
	}

	_, err = Default.ReserveAsync(ctx, guestID, guestID, ExtractTextPDF, 1, 0, "/api/ocr/extract-text-async", uuid.NewString())
	var billingErr *BillingError
	if !errors.As(err, &billingErr) || billingErr.Code != string(ErrHourlyLimit) {
		t.Fatalf("expected normal guest hourly rejection after consumption, got %v", err)
	}
}
