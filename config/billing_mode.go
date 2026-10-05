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

// BillingPolicy is the safe effective-policy projection exposed to clients.
// It describes monetization controls and does not represent subscription tier
// or technical resource limits.
type BillingPolicy struct {
	Mode                         BillingMode `json:"mode"`
	ProcessingUnitLimitsEnforced bool        `json:"processing_unit_limits_enforced"`
	PurchasesEnabled             bool        `json:"purchases_enabled"`
}

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

// CurrentBillingPolicy projects the process's current billing mode into the
// small policy contract needed by session clients.
func CurrentBillingPolicy() (BillingPolicy, error) {
	mode, err := CurrentBillingMode()
	if err != nil {
		return BillingPolicy{}, err
	}

	switch mode {
	case BillingModeNormal:
		return BillingPolicy{
			Mode:                         BillingModeNormal,
			ProcessingUnitLimitsEnforced: true,
			PurchasesEnabled:             true,
		}, nil
	case BillingModeFree:
		return BillingPolicy{
			Mode:                         BillingModeFree,
			ProcessingUnitLimitsEnforced: false,
			PurchasesEnabled:             false,
		}, nil
	default:
		return BillingPolicy{}, fmt.Errorf("unsupported billing mode %q", mode)
	}
}
