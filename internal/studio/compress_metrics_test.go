package studio

import (
	"math"
	"testing"
)

func TestCalculateCompressionMetricsUsesExactBytes(t *testing.T) {
	metrics := calculateCompressionMetrics(10_000, 6_000)

	if metrics.InputBytes != 10_000 || metrics.OutputBytes != 6_000 {
		t.Fatalf("unexpected exact sizes: %+v", metrics)
	}
	if metrics.SavedBytes != 4_000 || metrics.ReductionPercent != 40 {
		t.Fatalf("unexpected reduction metrics: %+v", metrics)
	}
}

func TestCalculateCompressionMetricsDoesNotClaimSavingsForExpansion(t *testing.T) {
	metrics := calculateCompressionMetrics(1_000, 1_250)

	if metrics.SavedBytes != 0 || metrics.ReductionPercent != 0 {
		t.Fatalf("expanded output must not claim savings: %+v", metrics)
	}
}

func TestCalculateCompressionMetricsHandlesZeroAndInvalidSizes(t *testing.T) {
	for _, sizes := range [][2]int64{{0, 10}, {-1, 10}, {10, -1}} {
		metrics := calculateCompressionMetrics(sizes[0], sizes[1])
		if math.IsNaN(metrics.ReductionPercent) || math.IsInf(metrics.ReductionPercent, 0) {
			t.Fatalf("metrics must remain finite and safe: sizes=%v metrics=%+v", sizes, metrics)
		}
	}
	if metrics := calculateCompressionMetrics(0, 10); metrics.ReductionPercent != 0 {
		t.Fatalf("zero input must have zero reduction: %+v", metrics)
	}
}

func TestValidCompressionLevelAcceptsOnlySupportedProfiles(t *testing.T) {
	for _, level := range []string{"low", "medium", "high", " HIGH "} {
		if !validCompressionLevel(level) {
			t.Errorf("expected compression level %q to be accepted", level)
		}
	}
	for _, level := range []string{"", "fast", "lossless", "very-high"} {
		if validCompressionLevel(level) {
			t.Errorf("expected compression level %q to be rejected", level)
		}
	}
}
