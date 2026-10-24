package conversion

import (
	"context"
	"math"
	"mime/multipart"
	"strings"
	"testing"
)

func TestPreviewScaleSafety(t *testing.T) {
	s := &ConversionService{}
	for _, scale := range []float64{0, -1, math.NaN(), math.Inf(1), math.Inf(-1), 600.0/72.0 + .01} {
		_, err := s.ConvertPageToImageStream(context.Background(), &multipart.FileHeader{}, 1, scale, "owner")
		if err == nil || !strings.Contains(err.Error(), "scale") {
			t.Fatalf("upload scale %v not rejected: %v", scale, err)
		}
		_, err = s.GetPreviewSessionPage(context.Background(), "fixture", 1, scale, "owner")
		if err == nil || !strings.Contains(err.Error(), "scale") {
			t.Fatalf("session scale %v not rejected: %v", scale, err)
		}
	}
	for _, scale := range []float64{.5, 1, 1.5, 2.5, 600.0 / 72.0} {
		_, err := s.GetPreviewSessionPage(context.Background(), "fixture", 1, scale, "")
		if err == nil || !strings.Contains(err.Error(), "identity") {
			t.Fatalf("valid scale %v rejected: %v", scale, err)
		}
	}
}
