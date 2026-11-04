package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
)

func TestReadyReportsDependencies(t *testing.T) {
	app := fiber.New()
	RegisterRoutes(app, NewControllerWithPingers(func(context.Context) error { return nil }, func(context.Context) error { return nil }))
	res, err := app.Test(httptest.NewRequest("GET", "/ready", nil))
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode)
}

func TestReadyReturns503WhenDependencyFails(t *testing.T) {
	app := fiber.New()
	RegisterRoutes(app, NewControllerWithPingers(func(context.Context) error { return errors.New("db down") }, func(context.Context) error { return nil }))
	res, err := app.Test(httptest.NewRequest("GET", "/ready", nil))
	require.NoError(t, err)
	require.Equal(t, 503, res.StatusCode)
}

func TestWorkerReadyReturnsSanitizedCapabilitiesAndCachesProbe(t *testing.T) {
	var calls atomic.Int32
	probe := func(context.Context) (string, map[string]bool, error) {
		calls.Add(1)
		return "ready", map[string]bool{"document_processing": true, "ocr": true, "office_to_pdf": false, "pdf_to_office": true}, nil
	}
	app := fiber.New()
	RegisterRoutes(app, NewControllerWithWorkerProbe(probe))

	for i := 0; i < 2; i++ {
		res, err := app.Test(httptest.NewRequest("GET", "/health/worker", nil))
		require.NoError(t, err)
		require.Equal(t, 200, res.StatusCode)
		var body map[string]any
		require.NoError(t, json.NewDecoder(res.Body).Decode(&body))
		require.Equal(t, "ready", body["status"])
		caps, ok := body["capabilities"].(map[string]any)
		require.True(t, ok)
		require.Equal(t, true, caps["ocr"])
		require.Equal(t, false, caps["office_to_pdf"])
		require.Equal(t, true, caps["pdf_to_office"])
	}
	require.EqualValues(t, 1, calls.Load(), "the public probe should use the short cache")
}

func TestWorkerProbeAuthenticationFailureBecomesUnknownWithoutDetails(t *testing.T) {
	probe := func(context.Context) (string, map[string]bool, error) {
		return "", nil, errors.New("worker returned 401 with secret-bearing detail")
	}
	app := fiber.New()
	RegisterRoutes(app, NewControllerWithWorkerProbe(probe))
	res, err := app.Test(httptest.NewRequest("GET", "/health/worker", nil))
	require.NoError(t, err)
	require.Equal(t, 200, res.StatusCode, "a worker auth failure is unknown, not a public probe failure")
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.NotContains(t, string(body), "secret-bearing")
	require.Contains(t, string(body), `"status":"unknown"`)
}

func TestWorkerNotReadyIsReportedAsSanitizedNotReady(t *testing.T) {
	probe := func(context.Context) (string, map[string]bool, error) {
		return "not_ready", map[string]bool{"document_processing": false, "ocr": false, "office_to_pdf": false, "pdf_to_office": false}, nil
	}
	app := fiber.New()
	RegisterRoutes(app, NewControllerWithWorkerProbe(probe))
	res, err := app.Test(httptest.NewRequest("GET", "/health/worker", nil))
	require.NoError(t, err)
	require.Equal(t, 503, res.StatusCode)
	body, err := io.ReadAll(res.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), `"status":"not_ready"`)
	require.NotContains(t, string(body), "redis")
	require.NotContains(t, string(body), "R2")
}
