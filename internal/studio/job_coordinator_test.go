package studio

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"

	"pdfnest-backend/internal/studio/models"
)

func TestStudioJobNamesAreClosed(t *testing.T) {
	assert.True(t, validStudioJob(StudioJobMarkupHighlight))
	assert.True(t, validStudioJob(StudioJobEditCompile))
	assert.False(t, validStudioJob("arbitrary_worker_command"))
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

func TestEditedEditorLayoutCannotAddOrDropBaselineElements(t *testing.T) {
	base := []byte(`{"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"p1-text-1","text":"Before","x":10,"y":20,"width":40,"height":12,"size":10,"font":"Helvetica"}]}]}`)
	_, _, err := decodeEditorLayout(base)
	assert.NoError(t, err)
	added := []byte(`{"pages":[{"page_num":1,"width":612,"height":792,"kind":"text","elements":[{"id":"p1-text-1","text":"After","x":10,"y":20,"width":40,"height":12,"size":10,"font":"Helvetica"},{"id":"p1-text-2","text":"Injected","x":10,"y":40,"width":30,"height":12,"size":10,"font":"Helvetica"}]}]}`)
	b, _, _ := decodeEditorLayout(base)
	a, _, _ := decodeEditorLayout(added)
	assert.ErrorIs(t, validateEditedEditorLayout(b, a), ErrInvalidJob)
}
