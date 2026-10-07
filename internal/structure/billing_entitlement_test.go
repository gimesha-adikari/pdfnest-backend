package structure

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/uploads"

	"github.com/gofiber/fiber/v2"
)

func TestDuplicateProcessingLimitsFollowEffectiveBillingPolicy(t *testing.T) {
	tests := []struct {
		name       string
		mode       config.BillingMode
		tier       string
		status     string
		wantPages  int
		wantCopies int
	}{
		{name: "normal guest", mode: config.BillingModeNormal, wantPages: 5, wantCopies: 2},
		{name: "normal free account", mode: config.BillingModeNormal, tier: "free", status: "active", wantPages: 5, wantCopies: 2},
		{name: "normal plus account", mode: config.BillingModeNormal, tier: "plus", status: "active", wantPages: 5, wantCopies: 2},
		{name: "normal inactive pro", mode: config.BillingModeNormal, tier: "pro", status: "canceled", wantPages: 5, wantCopies: 2},
		{name: "normal active pro", mode: config.BillingModeNormal, tier: "pro", status: "active", wantPages: 50, wantCopies: 10},
		{name: "free guest", mode: config.BillingModeFree, wantPages: 50, wantCopies: 10},
		{name: "free account", mode: config.BillingModeFree, tier: "free", status: "active", wantPages: 50, wantCopies: 10},
		{name: "free plus account", mode: config.BillingModeFree, tier: "plus", status: "active", wantPages: 50, wantCopies: 10},
		{name: "free pro account", mode: config.BillingModeFree, tier: "pro", status: "active", wantPages: 50, wantCopies: 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy := config.BillingPolicy{
				Mode:                         tt.mode,
				ProcessingUnitLimitsEnforced: tt.mode == config.BillingModeNormal,
				PurchasesEnabled:             tt.mode == config.BillingModeNormal,
			}
			gotPages, gotCopies := duplicateProcessingLimits(policy, tt.tier, tt.status)
			if gotPages != tt.wantPages || gotCopies != tt.wantCopies {
				t.Fatalf("duplicateProcessingLimits() = (%d, %d), want (%d, %d)", gotPages, gotCopies, tt.wantPages, tt.wantCopies)
			}
		})
	}
}

func TestFreeModeDuplicateLimitsRemainFinite(t *testing.T) {
	policy := config.BillingPolicy{Mode: config.BillingModeFree, ProcessingUnitLimitsEnforced: false, PurchasesEnabled: false}
	maxPages, maxCopies := duplicateProcessingLimits(policy, "free", "active")
	if maxPages != 50 || maxCopies != 10 {
		t.Fatalf("free mode duplicate limits = (%d, %d), want finite bounds (50, 10)", maxPages, maxCopies)
	}
	if 51 <= maxPages || 11 <= maxCopies {
		t.Fatalf("free mode limits must continue rejecting values above the finite bounds: got (%d, %d)", maxPages, maxCopies)
	}
}

func TestDuplicateFreeModeDoesNotRequireSubscriptionDatabase(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", "free-mode-user")
		return c.Next()
	})
	controller := NewController(&mockStructureService{})
	app.Post("/duplicate", uploads.Prepare(), controller.Duplicate)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	if err := writer.WriteField("pages", "1"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("copies", "1"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/duplicate", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("request failed before reaching upload validation: %v", err)
	}
	defer resp.Body.Close()
	responseBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(responseBody), "MISSING_UPLOAD_FILE") {
		t.Fatalf("free duplicate request should skip subscription DB lookup and reach file validation; got %d: %s", resp.StatusCode, responseBody)
	}
}
