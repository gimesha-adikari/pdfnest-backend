package conversion

import (
	"context"
	"testing"
	"time"
)

func TestHTMLPDFReadyDeadlineMs(t *testing.T) {
	const fallback = 8 * time.Second

	cases := []struct {
		name     string
		envValue string
		want     time.Duration
	}{
		{"unset uses fallback", "", fallback},
		{"valid milliseconds parsed", "500", 500 * time.Millisecond},
		{"zero rejected", "0", fallback},
		{"negative rejected", "-5", fallback},
		{"non numeric rejected", "abc", fallback},
		{"whitespace rejected", "   ", fallback},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HTML_PDF_TEST_DEADLINE_MS", tc.envValue)
			got := htmlPDFReadyDeadlineMs("HTML_PDF_TEST_DEADLINE_MS", fallback)
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWaitForPageQuietExpiresAtDeadline(t *testing.T) {
	// A near-zero deadline must return without contacting a browser, proving the
	// wait is always bounded and cannot block indefinitely.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	if err := waitForPageQuiet(ctx, time.Nanosecond); err != nil {
		t.Fatalf("waitForPageQuiet returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("waitForPageQuiet with 1ns deadline took %v; expected immediate return", elapsed)
	}
}

func TestWaitForPageQuietContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-canceled context

	err := waitForPageQuiet(ctx, 5*time.Second)
	if err == nil {
		t.Fatal("expected error on pre-canceled context, got nil")
	}
}
