package config

import "testing"

func TestManagedRuntimeRejectsMissingValues(t *testing.T) {
	t.Setenv("APP_ENV", "canary")
	for _, name := range []string{"DATABASE_URL", "REDIS_URL", "R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY", "R2_SECRET_KEY", "JWT_SECRET", "FILE_ENCRYPTION_KEY", "WORKER_SHARED_SECRET", "FRONTEND_URL", "ALLOWED_ORIGINS", "PDFNEST_WORKER_URL", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"} {
		t.Setenv(name, "")
	}
	if err := ValidateRuntimeConfig(); err == nil {
		t.Fatal("expected managed configuration validation to reject missing values")
	}
}

func TestDevelopmentRuntimeKeepsLocalFallbacks(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("BILLING_MODE", "normal")
	if err := ValidateRuntimeConfig(); err != nil {
		t.Fatalf("development fallback should remain available: %v", err)
	}
}

func TestDevelopmentRuntimeRejectsInvalidBillingMode(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("BILLING_MODE", "unlimited")
	if err := ValidateRuntimeConfig(); err == nil {
		t.Fatal("expected development runtime validation to reject an invalid BILLING_MODE")
	}
}
