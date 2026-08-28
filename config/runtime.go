package config

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// Managed environments deliberately have no local-service fallbacks. Keep this
// list small and explicit so a typo in APP_ENV cannot silently enable DDL or
// localhost dependencies in a managed deployment.
func IsManagedEnvironment() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV"))) {
	case "canary", "staging", "production":
		return true
	default:
		return false
	}
}

func ManagedEnvironmentName() string {
	return strings.ToLower(strings.TrimSpace(os.Getenv("APP_ENV")))
}

func ValidateRuntimeConfig() error {
	if !IsManagedEnvironment() {
		return nil
	}

	var missing []string
	for _, name := range []string{
		"DATABASE_URL", "REDIS_URL", "R2_ENDPOINT", "R2_BUCKET", "R2_ACCESS_KEY", "R2_SECRET_KEY",
		"JWT_SECRET", "FILE_ENCRYPTION_KEY", "WORKER_SHARED_SECRET", "FRONTEND_URL", "ALLOWED_ORIGINS",
		"PDFNEST_WORKER_URL", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET",
	} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("managed APP_ENV=%s requires: %s", ManagedEnvironmentName(), strings.Join(missing, ", "))
	}

	for _, name := range []string{"DATABASE_URL", "REDIS_URL", "R2_ENDPOINT", "FRONTEND_URL", "PDFNEST_WORKER_URL"} {
		if containsLocalFallback(os.Getenv(name)) {
			return fmt.Errorf("managed configuration %s must not point to localhost or loopback", name)
		}
	}
	if len(strings.TrimSpace(os.Getenv("FILE_ENCRYPTION_KEY"))) != 32 {
		return fmt.Errorf("managed FILE_ENCRYPTION_KEY must be exactly 32 characters")
	}
	if ManagedEnvironmentName() == "canary" && !strings.Contains(strings.ToLower(os.Getenv("R2_BUCKET")), "canary") {
		return fmt.Errorf("canary R2_BUCKET must be an explicitly canary-named dedicated bucket")
	}
	if strings.Contains(os.Getenv("ALLOWED_ORIGINS"), "localhost") || strings.Contains(os.Getenv("ALLOWED_ORIGINS"), "127.0.0.1") {
		return fmt.Errorf("managed ALLOWED_ORIGINS must not include localhost or loopback")
	}
	if err := requireHTTPURL("FRONTEND_URL"); err != nil {
		return err
	}
	if err := requireHTTPURL("PDFNEST_WORKER_URL"); err != nil {
		return err
	}
	return nil
}

func containsLocalFallback(value string) bool {
	value = strings.ToLower(value)
	return strings.Contains(value, "localhost") || strings.Contains(value, "127.0.0.1") || strings.Contains(value, "0.0.0.0")
}

func requireHTTPURL(name string) error {
	u, err := url.Parse(strings.TrimSpace(os.Getenv(name)))
	if err != nil || u.Scheme == "" || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("managed %s must be an absolute HTTP(S) URL", name)
	}
	return nil
}
