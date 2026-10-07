package config

import (
	"os"
	"strings"
	"testing"
)

func TestCurrentBillingModeDefaultsToNormalWhenUnset(t *testing.T) {
	previous, wasSet := os.LookupEnv("BILLING_MODE")
	t.Cleanup(func() {
		if wasSet {
			_ = os.Setenv("BILLING_MODE", previous)
		} else {
			_ = os.Unsetenv("BILLING_MODE")
		}
	})
	if err := os.Unsetenv("BILLING_MODE"); err != nil {
		t.Fatal(err)
	}

	got, err := CurrentBillingMode()
	if err != nil {
		t.Fatalf("CurrentBillingMode() error = %v", err)
	}
	if got != BillingModeNormal {
		t.Fatalf("CurrentBillingMode() = %q, want %q", got, BillingModeNormal)
	}
}

func TestCurrentBillingModeAcceptsSupportedValues(t *testing.T) {
	for _, test := range []struct {
		value string
		want  BillingMode
	}{
		{value: "normal", want: BillingModeNormal},
		{value: "free", want: BillingModeFree},
	} {
		t.Run(test.value, func(t *testing.T) {
			t.Setenv("BILLING_MODE", test.value)

			got, err := CurrentBillingMode()
			if err != nil {
				t.Fatalf("CurrentBillingMode() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("CurrentBillingMode() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCurrentBillingModeRejectsInvalidValues(t *testing.T) {
	for _, value := range []string{"unlimited", "FREE", "free "} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BILLING_MODE", value)

			if _, err := CurrentBillingMode(); err == nil || !strings.Contains(err.Error(), "BILLING_MODE") {
				t.Fatalf("CurrentBillingMode() error = %v, want an error naming BILLING_MODE", err)
			}
		})
	}
}

func TestCurrentBillingModeReadsEnvironmentAfterItChanges(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	first, err := CurrentBillingMode()
	if err != nil {
		t.Fatalf("CurrentBillingMode() error = %v", err)
	}
	if first != BillingModeNormal {
		t.Fatalf("first CurrentBillingMode() = %q, want %q", first, BillingModeNormal)
	}

	t.Setenv("BILLING_MODE", "free")
	second, err := CurrentBillingMode()
	if err != nil {
		t.Fatalf("CurrentBillingMode() after environment change error = %v", err)
	}
	if second != BillingModeFree {
		t.Fatalf("CurrentBillingMode() after environment change = %q, want %q", second, BillingModeFree)
	}
}
