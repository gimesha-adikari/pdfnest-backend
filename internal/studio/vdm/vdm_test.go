package vdm

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVDMValidation(t *testing.T) {
	assetID := "ast_123"

	// 1. Valid VDM
	validModel := DocumentModel{
		DocumentID: "doc_1",
		VersionID:  "ver_1",
		PageCount:  2,
		Pages: []PageDescriptor{
			{
				PageID:           "p_1",
				SourceAssetID:    &assetID,
				SourcePageNumber: 1,
				Rotation:         90,
				CropBox:          []float64{0, 0, 595.28, 841.89},
				Overlays: []Overlay{
					{
						ID:       "ovl_1",
						Type:     "watermark",
						Text:     "CONFIDENTIAL",
						Opacity:  0.5,
						Rotation: 45,
					},
				},
			},
			{
				PageID:           "p_2",
				SourceAssetID:    nil,
				SourcePageNumber: 0,
				IsBlank:          true,
				Dimensions:       &Dimensions{Width: 595.28, Height: 841.89},
				Rotation:         0,
				Overlays:         []Overlay{},
			},
		},
	}
	require.NoError(t, validModel.Validate())

	// 2. Mismatched page_count
	invalidCount := validModel
	invalidCount.PageCount = 3
	assert.Error(t, invalidCount.Validate())

	// 3. Duplicate page_id
	dupPageID := validModel
	dupPageID.Pages = []PageDescriptor{
		validModel.Pages[0],
		validModel.Pages[0], // Duplicate page_id "p_1"
	}
	assert.Error(t, dupPageID.Validate())

	// 4. Invalid rotation (e.g. 45 degrees)
	invalidRot := validModel
	invalidRot.Pages[0].Rotation = 45
	assert.Error(t, invalidRot.Validate())

	// 5. Invalid crop_box (must have 4 elements)
	invalidCrop := validModel
	invalidCrop.Pages[0].CropBox = []float64{0, 0, 500}
	assert.Error(t, invalidCrop.Validate())

	// 6. Non-blank page missing source_asset_id
	missingAsset := validModel
	missingAsset.Pages[0].SourceAssetID = nil
	missingAsset.Pages[0].IsBlank = false
	assert.Error(t, missingAsset.Validate())
}

func TestVDMJSONRoundTrip(t *testing.T) {
	assetID := "ast_test_roundtrip"
	model := DocumentModel{
		DocumentID: "doc_test_123",
		VersionID:  "ver_test_456",
		PageCount:  1,
		Pages: []PageDescriptor{
			{
				PageID:           "p_page_1",
				SourceAssetID:    &assetID,
				SourcePageNumber: 1,
				Rotation:         180,
				CropBox:          []float64{50.0, 50.0, 500.0, 700.0},
				Overlays: []Overlay{
					{
						ID:       "sig_1",
						Type:     "signature",
						AssetID:  "ast_sig_png",
						Rect:     []float64{100, 100, 150, 50},
						Opacity:  1.0,
						Rotation: 0,
					},
				},
			},
		},
		PageNumbering: &PageNumberingRule{
			Enabled:  true,
			Format:   "Page {page} of {total}",
			Position: "bottom-center",
			FontSize: 10,
			StartAt:  1,
		},
		Metadata: map[string]string{
			"Title":  "Quarterly Audit",
			"Author": "Finance Dept",
		},
	}

	bytes, err := model.ToJSON()
	require.NoError(t, err)

	deserialized, err := FromJSON(bytes)
	require.NoError(t, err)

	assert.Equal(t, model.DocumentID, deserialized.DocumentID)
	assert.Equal(t, model.PageCount, deserialized.PageCount)
	assert.Equal(t, len(model.Pages), len(deserialized.Pages))
	assert.Equal(t, model.Pages[0].Rotation, deserialized.Pages[0].Rotation)
	assert.Equal(t, model.Pages[0].CropBox, deserialized.Pages[0].CropBox)
	assert.Equal(t, model.Pages[0].Overlays[0].Type, deserialized.Pages[0].Overlays[0].Type)
	assert.Equal(t, model.PageNumbering.Format, deserialized.PageNumbering.Format)
	assert.Equal(t, model.Metadata["Title"], deserialized.Metadata["Title"])
}
