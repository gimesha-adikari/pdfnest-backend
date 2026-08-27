package studio

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/studio/vdm"
)

func redactValidationModel() *vdm.DocumentModel {
	asset := "source-asset"
	return &vdm.DocumentModel{
		PageCount: 2,
		Pages: []vdm.PageDescriptor{
			{PageID: "page-one", SourceAssetID: &asset, Dimensions: &vdm.Dimensions{Width: 600, Height: 800}},
			{PageID: "page-two", SourceAssetID: &asset, Dimensions: &vdm.Dimensions{Width: 600, Height: 800}},
		},
	}
}

func TestValidateRedactParameters(t *testing.T) {
	model := redactValidationModel()
	valid := RedactParameters{Boxes: []RedactBox{{ID: "area-1", PageID: "page-one", Page: 1, X: 0.1, Y: 0.2, Width: 0.3, Height: 0.2}}}
	require.NoError(t, validateRedactParameters(valid, model))

	tests := map[string]RedactParameters{
		"foreign page":  {Boxes: []RedactBox{{ID: "area-1", PageID: "other-page", Page: 1, X: 0.1, Y: 0.2, Width: 0.3, Height: 0.2}}},
		"out of bounds": {Boxes: []RedactBox{{ID: "area-1", PageID: "page-one", Page: 1, X: 0.8, Y: 0.2, Width: 0.3, Height: 0.2}}},
		"non finite":    {Boxes: []RedactBox{{ID: "area-1", PageID: "page-one", Page: 1, X: math.NaN(), Y: 0.2, Width: 0.3, Height: 0.2}}},
		"duplicate id": {Boxes: []RedactBox{
			{ID: "area-1", PageID: "page-one", Page: 1, X: 0.1, Y: 0.2, Width: 0.3, Height: 0.2},
			{ID: "area-1", PageID: "page-two", Page: 2, X: 0.1, Y: 0.2, Width: 0.3, Height: 0.2},
		}},
	}
	for name, params := range tests {
		t.Run(name, func(t *testing.T) { require.Error(t, validateRedactParameters(params, model)) })
	}
}

func TestRedactParametersAllowKeywordOnlyAndRejectEmpty(t *testing.T) {
	model := redactValidationModel()
	require.NoError(t, validateRedactParameters(RedactParameters{Keywords: []string{"SECRET"}}, model))
	require.NoError(t, validateRedactParameters(RedactParameters{}, model))
}

func TestRunProcessorUsesTypedAreaBoxesAtWorkerBoundary(t *testing.T) {
	model := redactValidationModel()
	var received []RedactBox
	coordinator := &studioMaterializationCoordinator{processors: MaterializationProcessors{
		Redact: func(_ string, _ string, _ []string, boxes string) (string, error) {
			require.NoError(t, json.Unmarshal([]byte(boxes), &received))
			return "redacted.pdf", nil
		},
	}}
	params, err := json.Marshal(RedactParameters{Keywords: []string{"ALPHA"}, Boxes: []RedactBox{{ID: "area-1", PageID: "page-two", Page: 2, X: 0.2, Y: 0.3, Width: 0.2, Height: 0.1}}})
	require.NoError(t, err)
	output, err := coordinator.runProcessor(context.Background(), &MaterializedVersion{Path: "source.pdf", Model: model}, MaterializeRedact, params, "/tmp")
	require.NoError(t, err)
	require.Equal(t, "/tmp/redacted.pdf", output)
	require.Len(t, received, 1)
	require.Equal(t, "page-two", received[0].PageID)
}

func TestRunProcessorRejectsEmptyMalformedAndOutOfBoundsRedactionInput(t *testing.T) {
	model := redactValidationModel()
	coordinator := &studioMaterializationCoordinator{processors: MaterializationProcessors{
		Redact: func(_ string, _ string, _ []string, _ string) (string, error) { return "redacted.pdf", nil },
	}}
	for name, raw := range map[string][]byte{
		"empty":       []byte(`{"keywords":[],"boxes":[]}`),
		"malformed":   []byte(`{"keywords":[],"boxes":[`),
		"out of page": []byte(`{"keywords":[],"boxes":[{"id":"a","page_id":"page-one","page":1,"x":0.9,"y":0,"width":0.2,"height":0.1}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := coordinator.runProcessor(context.Background(), &MaterializedVersion{Path: "source.pdf", Model: model}, MaterializeRedact, raw, "/tmp")
			require.ErrorIs(t, err, ErrInvalidMaterialization)
		})
	}
}
