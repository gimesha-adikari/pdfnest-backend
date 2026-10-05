package config

import (
	"fmt"
	"os"
)

// BillingMode is the backend's authoritative billing operating mode.
type BillingMode string

const (
	BillingModeNormal BillingMode = "normal"
	BillingModeFree   BillingMode = "free"
)

// CurrentBillingMode reads BILLING_MODE at call time so dotenv loading can
// happen before the process relies on the configured mode.
func CurrentBillingMode() (BillingMode, error) {
	value, ok := os.LookupEnv("BILLING_MODE")
	if !ok || value == "" {
		return BillingModeNormal, nil
	}

	switch BillingMode(value) {
	case BillingModeNormal:
		return BillingModeNormal, nil
	case BillingModeFree:
		return BillingModeFree, nil
	default:
		return "", fmt.Errorf("invalid BILLING_MODE %q: supported values are %q and %q", value, BillingModeNormal, BillingModeFree)
	}
}
