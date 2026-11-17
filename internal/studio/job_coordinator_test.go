package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/markup"
	"pdfnest-backend/internal/studio/models"
	"pdfnest-backend/internal/studio/vdm"
)

func TestStudioJobNamesAreClosed(t *testing.T) {
	assert.True(t, validStudioJob(StudioJobMarkupHighlight))
	assert.True(t, validStudioJob(StudioJobEditCompile))
	assert.False(t, validStudioJob("arbitrary_worker_command"))
}

func TestStudioMarkupModesExposeOnlyPublicModes(t *testing.T) {
	assert.True(t, validStudioMarkupMode(StudioMarkupModeManual))
	assert.True(t, validStudioMarkupMode(StudioMarkupModeSmart))
	assert.True(t, validStudioMarkupMode(StudioMarkupModeOCR))
	assert.False(t, validStudioMarkupMode("text"), "worker-internal text mode must not cross the Studio boundary")
	assert.False(t, validStudioMarkupMode("unexpected"))
}

func TestMarkupTargetPageIDsAreStableAndDeduplicated(t *testing.T) {
	base := &vdm.DocumentModel{Pages: []vdm.PageDescriptor{
		{PageID: "page-one"},
		{PageID: "page-two"},
		{PageID: "page-three"},
	}}
	raw, err := json.Marshal(MarkupJobParameters{Boxes: []markup.Box{
		{Page: 3}, {Page: 1}, {Page: 3},
	}})
	require.NoError(t, err)

	assert.JSONEq(t, `["page-three","page-one"]`, string(markupTargetPageIDs(base, models.JSON(raw))))
}

func TestMarkupTargetPageIDsFailClosedForInvalidPayload(t *testing.T) {
	base := &vdm.DocumentModel{Pages: []vdm.PageDescriptor{{PageID: "page-one"}}}
	assert.Nil(t, markupTargetPageIDs(base, models.JSON([]byte(`{"boxes":[{"page":2}]}`))))
}

func TestStudioStagePayloadRejectsWorkerInternalMarkupMode(t *testing.T) {
	coordinator := &studioJobCoordinator{}
	_, err := coordinator.stagePayload(
		context.Background(),
		StudioJobMarkupHighlight,
		[]byte(`{"boxes":[{"x":1,"y":2,"width":10,"height":10,"page":1}],"mode":"text"}`),
		uuid.New(),
	)
	assert.ErrorIs(t, err, ErrInvalidJob)
}

func TestWorkerStatusMirrorsExistingWorkerLifecycle(t *testing.T) {
	job := &models.StudioJob{Status: "queued"}
	applyWorkerStatus(job, workerJobStatus{ID: "worker-1", Status: "cancel_requested", Progress: 37, Message: "cancellation requested"})
	assert.Equal(t, "cancel_requested", job.Status)
	assert.Equal(t, 37, job.Progress)
	assert.False(t, terminalStudioJob(job.Status))
	assert.True(t, terminalStudioJob("cancelled"))
	assert.True(t, terminalStudioJob("succeeded"))
}

func TestStudioJobIdempotencyCanonicalizesJSONBKeyOrder(t *testing.T) {
	persisted, err := canonicalJSON(json.RawMessage(`{"mode":"manual","boxes":[{"x":1,"y":2}]}`))
	assert.NoError(t, err)
	retry, err := canonicalJSON(json.RawMessage(`{"boxes":[{"y":2,"x":1}],"mode":"manual"}`))
	assert.NoError(t, err)
	assert.True(t, bytes.Equal(persisted, retry))
}

func TestEditorLayoutValidationRequiresStableIDsAndFiniteGeometry(t *testing.T) {
	valid := []byte(`{"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"p1-text-1","text":"Before","original_text":"Before","x":10,"y":20,"width":40,"height":12,"size":10,"font":"Helvetica"}]}]}`)
	_, _, err := decodeEditorLayout(valid)
	assert.NoError(t, err)
	invalidID := bytes.Replace(valid, []byte(`"id":"p1-text-1"`), []byte(`"id":""`), 1)
	_, _, err = decodeEditorLayout(invalidID)
	assert.ErrorIs(t, err, ErrInvalidJob)
	invalidPage := bytes.Replace(valid, []byte(`"page_num":1`), []byte(`"page_num":2`), 1)
	_, _, err = decodeEditorLayout(invalidPage)
	assert.ErrorIs(t, err, ErrInvalidJob)
}

func editorLayoutWithRotation(t *testing.T, rotation any, includeRotation, legacy bool, extraPageFields map[string]any) []byte {
	t.Helper()

	page := map[string]any{
		"page_num": 1,
		"width":    800,
		"height":   600,
		"kind":     "scanned",
		"elements": []map[string]any{{
			"id": "p1-ocr-v2-line-1", "text": "Phase Eight Alpha Bravo", "original_text": "Phase Eight Alpha Bravo",
			"x": 60, "y": 110, "width": 300, "height": 28, "size": 32.2, "font": "tiro",
		}},
	}
	if includeRotation {
		page["rotation"] = rotation
	}
	for key, value := range extraPageFields {
		page[key] = value
	}

	document := map[string]any{"success": true, "pages": []map[string]any{page}}
	if !legacy {
		document["schema_version"] = "ocr_v2_editor_layout.v1"
		document["ocr_v2"] = true
		document["geometry_space"] = "studio_visible"
	}
	raw, err := json.Marshal(document)
	require.NoError(t, err)
	return raw
}

func TestEditorLayoutPreservesWorkerPageRotation(t *testing.T) {
	for _, expected := range []int{0, 90, 180, 270} {
		t.Run(fmt.Sprintf("accepts_%d_degrees", expected), func(t *testing.T) {
			layout, canonical, err := decodeEditorLayout(editorLayoutWithRotation(t, expected, true, false, nil))
			require.NoError(t, err, "the Studio worker's rotation metadata is part of its public layout contract")
			assert.Equal(t, expected, int(layout.Pages[0].Rotation))
			assert.Equal(t, float64(800), layout.Pages[0].Width)
			assert.Equal(t, float64(600), layout.Pages[0].Height)
			if expected != 0 {
				assert.Contains(t, string(canonical), fmt.Sprintf(`"rotation":%d`, expected))
			}
		})
	}

	t.Run("legacy_layout_without_rotation_defaults_to_zero", func(t *testing.T) {
		layout, canonical, err := decodeEditorLayout(editorLayoutWithRotation(t, nil, false, true, nil))
		require.NoError(t, err, "pre-rotation Studio layouts remain valid")
		assert.Zero(t, layout.Pages[0].Rotation)
		assert.NotContains(t, string(canonical), `"rotation"`)
	})

	for name, value := range map[string]any{
		"unsupported_45": 45, "unsupported_360": 360, "unsupported_negative": -90,
		"fraction": 90.5, "string": "90", "boolean": true, "null": nil,
		"array": []any{90}, "object": map[string]any{"degrees": 90},
	} {
		t.Run("rejects_"+name, func(t *testing.T) {
			_, _, err := decodeEditorLayout(editorLayoutWithRotation(t, value, true, false, nil))
			assert.ErrorIs(t, err, ErrInvalidJob, "malformed or unsupported page rotations remain fail-closed")
		})
	}

	t.Run("unrelated_worker_fields_remain_rejected", func(t *testing.T) {
		_, _, err := decodeEditorLayout(editorLayoutWithRotation(t, 90, true, false, map[string]any{"unexpected_field": true}))
		assert.ErrorIs(t, err, ErrInvalidJob, "the strict Studio layout decoder remains closed to unrelated fields")
	})

	t.Run("edited_layout_cannot_change_source_rotation", func(t *testing.T) {
		base, _, err := decodeEditorLayout(editorLayoutWithRotation(t, 90, true, false, nil))
		require.NoError(t, err)
		edited, _, err := decodeEditorLayout(editorLayoutWithRotation(t, 180, true, false, nil))
		require.NoError(t, err)
		assert.ErrorIs(t, validateEditedEditorLayout(base, edited), ErrInvalidJob, "source page rotation is immutable across layout edits")
	})
}

func TestEditorLayoutAcceptsStudioVisibleGeometrySpace(t *testing.T) {
	valid := []byte(`{"geometry_space":"studio_visible","pages":[{"page_num":1,"width":612,"height":792,"kind":"scanned","elements":[{"id":"p1-ocr-1","text":"edited","original_text":"source","x":10,"y":20,"width":40,"height":12,"size":10,"font":"tiro"}]}]}`)
	layout, _, err := decodeEditorLayout(valid)
	require.NoError(t, err)
	assert.Equal(t, "studio_visible", layout.GeometrySpace)

	invalid := []byte(`{"geometry_space":"native_pdf","pages":[{"page_num":1,"width":612,"height":792,"kind":"scanned","elements":[{"id":"p1-ocr-1","text":"edited","original_text":"source","x":10,"y":20,"width":40,"height":12,"size":10,"font":"tiro"}]}]}`)
	_, _, err = decodeEditorLayout(invalid)
	assert.ErrorIs(t, err, ErrInvalidJob)
}

func TestEditedEditorLayoutCannotAddOrDropBaselineElements(t *testing.T) {
	base := []byte(`{"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"p1-text-1","text":"Before","x":10,"y":20,"width":40,"height":12,"size":10,"font":"Helvetica"}]}]}`)
	_, _, err := decodeEditorLayout(base)
	assert.NoError(t, err)
	added := []byte(`{"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"p1-text-1","text":"After","x":10,"y":20,"width":40,"height":12,"size":10,"font":"Helvetica"},{"id":"p1-text-2","text":"Injected","x":10,"y":40,"width":30,"height":12,"size":10,"font":"Helvetica"}]}]}`)
	b, _, _ := decodeEditorLayout(base)
	a, _, _ := decodeEditorLayout(added)
	assert.ErrorIs(t, validateEditedEditorLayout(b, a), ErrInvalidJob)
}

func TestEditorLayoutAcceptsTypedStylesAndRejectsUnknownOrInvalidStyles(t *testing.T) {
	valid := []byte(`{"success":true,"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"e1","text":"base","original_text":"base","target_substring":"as","selection_start":1,"selection_end":3,"x":10,"y":20,"width":30,"height":12,"size":10,"font":"helv","style":{"fontFamily":"cour","fontSize":14,"bold":true,"italic":true,"underline":true,"strikethrough":true,"color":"#112233","background":"#fef08a"}}]}]}`)
	_, _, err := decodeEditorLayout(valid)
	assert.NoError(t, err)
	invalidFamily := []byte(`{"success":true,"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"e1","text":"x","x":1,"y":1,"width":1,"height":1,"size":10,"font":"helv","style":{"fontFamily":"arbitrary"}}]}]}`)
	_, _, err = decodeEditorLayout(invalidFamily)
	assert.ErrorIs(t, err, ErrInvalidJob)
	unknown := []byte(`{"success":true,"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"e1","text":"x","x":1,"y":1,"width":1,"height":1,"size":10,"font":"helv","style":{"provider_payload":"no"}}]}]}`)
	_, _, err = decodeEditorLayout(unknown)
	assert.ErrorIs(t, err, ErrInvalidJob)
	invalidRange := []byte(`{"success":true,"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"e1","text":"base","original_text":"base","target_substring":"wrong","selection_start":1,"selection_end":3,"x":1,"y":1,"width":1,"height":1,"size":10,"font":"helv"}]}]}`)
	_, _, err = decodeEditorLayout(invalidRange)
	assert.ErrorIs(t, err, ErrInvalidJob)
}

func TestEditedEditorLayoutPreservesImmutableIdentityAndGeometry(t *testing.T) {
	baseRaw := []byte(`{"success":true,"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"e1","text":"base","original_text":"base","x":10,"y":20,"width":30,"height":12,"size":10,"font":"helv"}]}]}`)
	base, _, err := decodeEditorLayout(baseRaw)
	require.NoError(t, err)
	edited, _, err := decodeEditorLayout(baseRaw)
	require.NoError(t, err)
	edited.Pages[0].Elements[0].Text = "edited"
	assert.NoError(t, validateEditedEditorLayout(base, edited))
	edited.Pages[0].Elements[0].X++
	assert.ErrorIs(t, validateEditedEditorLayout(base, edited), ErrInvalidJob)
}

func TestStudioEditorLanguageContractIsExplicitAndBounded(t *testing.T) {
	value, err := validateStudioEditorLanguage(EditExtractJobParameters{LanguageMode: "AUTO", Languages: []string{"eng", "sin", "tam"}})
	assert.NoError(t, err)
	assert.Equal(t, "AUTO", value.Mode)
	assert.Equal(t, []string{"eng", "sin", "tam"}, value.Languages)
	_, err = validateStudioEditorLanguage(EditExtractJobParameters{LanguageMode: "EXPLICIT", Languages: []string{"sin"}})
	assert.NoError(t, err)
	_, err = validateStudioEditorLanguage(EditExtractJobParameters{LanguageMode: "AUTO", Languages: []string{"eng", "fra"}})
	assert.ErrorIs(t, err, ErrInvalidJob)
	_, err = validateStudioEditorLanguage(EditExtractJobParameters{})
	assert.ErrorIs(t, err, ErrInvalidJob)
}
