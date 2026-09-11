package conversion

import (
	"context"
	"errors"
	"os"
	"pdfnest-backend/internal/netguard"
	"testing"
	"time"
)

func TestHTMLPDFRejectsPrivateAndNonHTTPDestinations(t *testing.T) {
	for _, target := range []string{"http://127.0.0.1/", "http://[::1]/", "http://169.254.169.254/", "file:///etc/passwd", "http://localhost/"} {
		path, err := (&ConversionService{}).HtmlToPdf(context.Background(), target, PrintOptions{PaperSize: "A4"})
		if path != "" || !errors.Is(err, netguard.ErrForbidden) {
			t.Fatalf("destination %q: path=%q error=%v", target, path, err)
		}
	}
}

func TestHTMLPDFPublicNetworkIntegration(t *testing.T) {
	if os.Getenv("PDFNEST_TEST_PUBLIC_RENDER") != "true" {
		t.Skip("requires Chromium and outbound public HTTPS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	path, err := (&ConversionService{}).HtmlToPdf(ctx, "https://example.com/", PrintOptions{PaperSize: "A4"})
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path)
	data, err := os.ReadFile(path)
	if err != nil || len(data) < 1000 || string(data[:5]) != "%PDF-" {
		t.Fatalf("invalid PDF output: bytes=%d err=%v", len(data), err)
	}
}

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
