package ocrv2

import (
	"context"
	"testing"
)

type fakeMarkupPreviewInvoker struct {
	fakeInvoker
	response *MarkupPreviewResponse
	err      error
	received TextRequest
}

func (f *fakeMarkupPreviewInvoker) Preview(_ context.Context, _ string, request TextRequest) (*MarkupPreviewResponse, error) {
	f.received = request
	return f.response, f.err
}

func TestServicePreviewMarkupUsesTheAuthorizedPreviewProjection(t *testing.T) {
	fake := &fakeMarkupPreviewInvoker{
		response: &MarkupPreviewResponse{
			SchemaVersion: "ocr_v2_markup_preview.v1",
			Profile:       ProfileMarkupV2,
			Status:        "SUCCEEDED",
			PageCount:     1,
			Pages: []MarkupPreviewPage{{
				PageIndex:         0,
				PageNumber:        1,
				PageID:            "page-0",
				Width:             600,
				Height:            800,
				Classification:    "IMAGE_SCAN",
				Kind:              "scanned",
				SelectionMode:     "ocr",
				Status:            "SUCCESS",
				HasSelectableText: true,
				WordCount:         1,
				ReadingOrder:      []string{"word-0"},
				Words:             []MarkupPreviewWord{{ID: "word-0", Text: "Alpha", X: 10, Y: 20, Width: 40, Height: 12, Order: 0}},
			}},
		},
	}
	service := NewService(fake)

	preview, err := service.PreviewMarkup(context.Background(), samplePDFPath(t), TextRequest{
		RequestID:     "preview-request",
		Profile:       ProfileMarkupV2,
		Language:      "eng+sin",
		LanguageMode:  "EXPLICIT",
		Languages:     []string{"eng", "sin"},
		RoutingPolicy: RoutingFast,
	})
	if err != nil {
		t.Fatalf("expected markup preview, got %v", err)
	}
	if preview == nil || preview.SchemaVersion != "ocr_v2_markup_preview.v1" || len(preview.Pages) != 1 {
		t.Fatalf("unexpected preview response: %+v", preview)
	}
	if fake.received.Profile != ProfileMarkupV2 || fake.received.Language != "eng+sin" || fake.received.RoutingPolicy != RoutingFast {
		t.Fatalf("preview request was not forwarded intact: %+v", fake.received)
	}
}
