package studio

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/structure"
)

type staticMarkupAnalysisProvider struct{}

func (staticMarkupAnalysisProvider) AnalyzeMarkup(context.Context, uuid.UUID, identity.Identity) (*structure.PDFAnalysis, error) {
	return &structure.PDFAnalysis{PageCount: 1, Pages: []structure.PageAnalysis{{Page: 1, Kind: "text", HasSelectableText: true, WordCount: 2}}}, nil
}

func TestStudioMarkupAnalysisRouteIsReadOnlyAndTyped(t *testing.T) {
	controller := NewController(nil, nil, nil, nil, nil)
	controller.SetMarkupAnalysisProvider(staticMarkupAnalysisProvider{})
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: "analysis-guest", Type: identity.TypeGuest})
		return c.Next()
	})
	RegisterRoutes(app.Group("/api"), controller)

	sessionID := uuid.New()
	response, err := app.Test(httptest.NewRequest("GET", "/api/studio/v1/sessions/"+sessionID.String()+"/markup-analysis", nil))
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	var result structure.PDFAnalysis
	require.NoError(t, json.NewDecoder(response.Body).Decode(&result))
	require.Equal(t, 1, result.PageCount)
	require.Equal(t, "text", result.Pages[0].Kind)
}
