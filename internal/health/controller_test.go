package health

import (
	"context"
	"errors"
	"net/http/httptest"
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
