package limiter

import (
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gofiber/fiber/v2"
)

func TestGovernor_AcquireAndRelease(t *testing.T) {
	gov := NewGovernorWithCapacity(2)

	rel1, ok1 := gov.TryAcquire()
	if !ok1 || rel1 == nil {
		t.Fatalf("Expected acquire 1 to succeed")
	}
	if gov.ActiveCount() != 1 {
		t.Errorf("Expected active count 1, got %d", gov.ActiveCount())
	}

	rel2, ok2 := gov.TryAcquire()
	if !ok2 || rel2 == nil {
		t.Fatalf("Expected acquire 2 to succeed")
	}
	if gov.ActiveCount() != 2 {
		t.Errorf("Expected active count 2, got %d", gov.ActiveCount())
	}

	_, ok3 := gov.TryAcquire()
	if ok3 {
		t.Fatalf("Expected acquire 3 to fail when capacity is exhausted")
	}

	rel1()
	if gov.ActiveCount() != 1 {
		t.Errorf("Expected active count 1 after release, got %d", gov.ActiveCount())
	}

	// Test double-release idempotency
	rel1()
	if gov.ActiveCount() != 1 {
		t.Errorf("Expected active count to remain 1 after duplicate release call, got %d", gov.ActiveCount())
	}

	rel2()
	if gov.ActiveCount() != 0 {
		t.Errorf("Expected active count 0 after all releases, got %d", gov.ActiveCount())
	}
}

func TestGovernor_ConcurrentAcquire(t *testing.T) {
	cap := 10
	gov := NewGovernorWithCapacity(cap)

	var wg sync.WaitGroup
	acquired := make(chan func(), 50)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rel, ok := gov.TryAcquire(); ok {
				acquired <- rel
			}
		}()
	}

	wg.Wait()
	close(acquired)

	count := 0
	for rel := range acquired {
		count++
		rel()
	}

	if count != cap {
		t.Errorf("Expected exactly %d successful acquisitions, got %d", cap, count)
	}

	if gov.ActiveCount() != 0 {
		t.Errorf("Expected active count 0 after releasing all, got %d", gov.ActiveCount())
	}
}

func TestGovernor_Middleware429(t *testing.T) {
	gov := NewGovernorWithCapacity(1)
	app := fiber.New()
	app.Use(gov.Middleware())
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendString("ok")
	})

	rel, ok := gov.TryAcquire()
	if !ok {
		t.Fatalf("Expected manual acquire to succeed")
	}

	req := httptest.NewRequest("GET", "/test", nil)
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("Failed to execute test request: %v", err)
	}

	if resp.StatusCode != fiber.StatusTooManyRequests {
		t.Errorf("Expected status 429, got %d", resp.StatusCode)
	}

	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "5" {
		t.Errorf("Expected Retry-After header '5', got %q", retryAfter)
	}

	rel()
}
