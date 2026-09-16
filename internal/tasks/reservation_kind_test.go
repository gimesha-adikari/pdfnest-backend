package tasks

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestTaskRegistryPreservesReservationKindAcrossAsyncStates(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	registry := &TaskRegistry{client: client}
	taskID := "guest-reservation-kind"
	ok, err := registry.SetWithKeyAndBilling(taskID, "PENDING", 0, "", "", "guest-owner", "reservation-1", "guest")
	if err != nil || !ok {
		t.Fatalf("create task: accepted=%v err=%v", ok, err)
	}
	if _, err := registry.SetWithKey(taskID, "PROCESSING", 35, "", "", "guest-owner"); err != nil {
		t.Fatalf("update task: %v", err)
	}

	status, err := registry.Get(taskID)
	if err != nil {
		t.Fatal(err)
	}
	if status == nil || status.ReservationID != "reservation-1" || status.ReservationKind != "guest" {
		t.Fatalf("reservation metadata was not preserved: %+v", status)
	}

	if err := client.Del(context.Background(), TaskKeyPrefix+taskID).Err(); err != nil {
		t.Fatal(err)
	}
}
