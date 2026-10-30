package studio

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/studio/vdm"
)

type renderingSessionFixture struct {
	Session struct {
		ID string `json:"id"`
	} `json:"session"`
	ActiveVersion struct {
		ID string `json:"id"`
	} `json:"active_version"`
	VDM vdm.DocumentModel `json:"vdm"`
}

// createRenderingSession exercises current source admission rather than trusting
// caller-supplied source/page IDs. Render tests must use the returned identities.
func createRenderingSession(t *testing.T, app *fiber.App, guestID string, pdf []byte, filename string) renderingSessionFixture {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(pdf)
	require.NoError(t, err)
	require.NoError(t, form.Close())
	req := httptest.NewRequest(http.MethodPost, "/api/studio/v1/sessions/from-upload", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.Header.Set("X-Test-Guest-ID", guestID)
	resp, err := app.Test(req, 10000)
	require.NoError(t, err)
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusCreated, resp.StatusCode, "source admission: %s", payload)
	var fixture renderingSessionFixture
	require.NoError(t, json.Unmarshal(payload, &fixture))
	_, err = uuid.Parse(fixture.Session.ID)
	require.NoError(t, err)
	_, err = uuid.Parse(fixture.ActiveVersion.ID)
	require.NoError(t, err)
	require.NotEmpty(t, fixture.VDM.Pages)
	require.Equal(t, fixture.VDM.PageCount, len(fixture.VDM.Pages))
	for index, page := range fixture.VDM.Pages {
		_, err = uuid.Parse(page.PageID)
		require.NoError(t, err)
		require.NotNil(t, page.SourceAssetID)
		require.NotEmpty(t, *page.SourceAssetID)
		require.Equal(t, index+1, page.SourcePageNumber)
		require.NotNil(t, page.Dimensions)
		require.Positive(t, page.Dimensions.Width)
		require.Positive(t, page.Dimensions.Height)
	}
	return fixture
}
