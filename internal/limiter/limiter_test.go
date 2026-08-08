package limiter

import (
	"context"
	"testing"
	"time"
)

func TestGovernor_InMemoryFallback(t *testing.T) {
	gov := &Governor{
		client:          nil,
		globalLimit:     2,
		identityLimit:   1,
		leaseTTLSeconds: 10,
	}

	ctx := context.Background()

	rel1, ok1, err1 := gov.AcquireWithRelease(ctx, "task-1", "user-A")
	if err1 != nil || !ok1 {
		t.Fatalf("Expected task-1 acquisition to succeed, got ok=%v, err=%v", ok1, err1)
	}

	rel2, ok2, err2 := gov.AcquireWithRelease(ctx, "task-2", "user-B")
	if err2 != nil || !ok2 {
		t.Fatalf("Expected task-2 acquisition to succeed, got ok=%v, err=%v", ok2, err2)
	}

	_, ok3, _ := gov.AcquireWithRelease(ctx, "task-3", "user-C")
	if ok3 {
		t.Fatalf("Expected task-3 acquisition to fail (capacity 2 full)")
	}

	rel1()
	rel2()
}

func TestGetEnvInt(t *testing.T) {
	if val := GetEnvInt("NON_EXISTENT_VAR_123", 42); val != 42 {
		t.Errorf("Expected 42, got %d", val)
	}
}

func TestAcquireResult_Structure(t *testing.T) {
	res := AcquireResult{
		Status:    "ACCEPTED",
		Reason:    "ACQUIRED",
		ExpiresAt: time.Now().Unix() + 600,
	}
	if res.Status != "ACCEPTED" {
		t.Errorf("Expected ACCEPTED, got %s", res.Status)
	}
}
