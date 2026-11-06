package tasks

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fasthttp/websocket"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
)

func authorizationTestApp(t *testing.T, registry *TaskRegistry) *fiber.App {
	t.Helper()
	previous := Registry
	Registry = registry
	t.Cleanup(func() { Registry = previous })
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, c.Get("X-Test-Identity"))
		c.Locals(identity.LocalIdentityType, c.Get("X-Test-Identity-Type"))
		return c.Next()
	})
	RegisterRoutes(app)
	return app
}

func setCachedCompletedTask(t *testing.T, registry *TaskRegistry, id, owner, token, resultURL string) *TaskStatus {
	t.Helper()
	_, err := registry.SetWithDownloadToken(id, "COMPLETED", 100, "", "", owner, token)
	require.NoError(t, err)
	task, err := registry.Get(id)
	require.NoError(t, err)
	require.NotNil(t, task)
	task.ResultURL = resultURL
	payload, err := json.Marshal(task)
	require.NoError(t, err)
	require.NoError(t, registry.client.Set(context.Background(), TaskKeyPrefix+id, payload, TaskTTL).Err())
	return task
}

func setAuthorizationTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	previous := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = previous })
}

func TestAccountTaskRoutesFailClosedWhenDatabaseIsNil(t *testing.T) {
	_, registry, cleanup := setupDownloadTestRedis(t)
	defer cleanup()
	setAuthorizationTestDB(t, nil)
	t.Setenv("STORAGE_MODE", "local")

	ownerID := uuid.NewString()
	resultPath := filepath.Join(t.TempDir(), "synthetic-account-output.pdf")
	require.NoError(t, os.WriteFile(resultPath, []byte("synthetic account artifact"), 0o600))
	task := setCachedCompletedTask(t, registry, "cached-account-task", ownerID, "previously-issued-account-capability", resultPath)
	app := authorizationTestApp(t, registry)

	for _, path := range []string{
		"/v1/download/" + task.ID + "?token=" + task.DownloadToken,
		"/v1/tasks/" + task.ID + "?token=" + task.DownloadToken,
		"/v1/tasks/" + task.ID + "/progress?token=" + task.DownloadToken,
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("X-Test-Identity", ownerID)
			request.Header.Set("X-Test-Identity-Type", string(identity.TypeUser))
			response, err := app.Test(request)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, fiber.StatusForbidden, response.StatusCode)
		})
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/download/"+task.ID, nil)
	request.Header.Set("X-Test-Identity", ownerID)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeUser))
	request.Header.Set("X-Download-Token", task.DownloadToken)
	response, err := app.Test(request)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, fiber.StatusForbidden, response.StatusCode)

	// The same old capability cannot use the cache fallback when the account
	// identity is absent from the request either.
	request = httptest.NewRequest(http.MethodGet, "/v1/download/"+task.ID+"?token="+task.DownloadToken, nil)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeGuest))
	response, err = app.Test(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.NotEqual(t, fiber.StatusOK, response.StatusCode)
}

func TestGuestUUIDTaskRemainsAuthorizedWithoutDatabase(t *testing.T) {
	_, registry, cleanup := setupDownloadTestRedis(t)
	defer cleanup()
	setAuthorizationTestDB(t, nil)
	t.Setenv("STORAGE_MODE", "local")

	guestID := uuid.NewString()
	resultPath := filepath.Join(t.TempDir(), "synthetic-guest-output.pdf")
	require.NoError(t, os.WriteFile(resultPath, []byte("synthetic guest artifact"), 0o600))
	task := setCachedCompletedTask(t, registry, "cached-guest-task", guestID, "guest-capability", resultPath)
	app := authorizationTestApp(t, registry)

	request := httptest.NewRequest(http.MethodGet, "/v1/download/"+task.ID, nil)
	request.Header.Set("X-Test-Identity", guestID)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeGuest))
	response, err := app.Test(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusOK, response.StatusCode)
}

func TestAccountTaskAuthorizationFailsClosedOnDatabaseQueryFailure(t *testing.T) {
	_, registry, cleanup := setupDownloadTestRedis(t)
	defer cleanup()
	ownerID := uuid.NewString()

	missingSocket := filepath.Join(t.TempDir(), "no-postgres.sock")
	db, err := gorm.Open(postgres.Open("host="+missingSocket+" port=55447 user=platen_test dbname=platen_authz_test sslmode=disable connect_timeout=1"), &gorm.Config{DisableAutomaticPing: true})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	defer pool.Close()
	_, err = registry.SetWithDownloadToken("db-error-task", "COMPLETED", 100, "", "", ownerID, "db-error-capability")
	require.NoError(t, err)
	setAuthorizationTestDB(t, db)
	require.False(t, accountOwnerNotDeleted(context.Background(), ownerID))
	app := authorizationTestApp(t, registry)
	request := httptest.NewRequest(http.MethodGet, "/v1/download/db-error-task?token=db-error-capability", nil)
	request.Header.Set("X-Test-Identity", ownerID)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeUser))
	response, err := app.Test(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.NotEqual(t, fiber.StatusOK, response.StatusCode)
}

func TestAccountDeletionRevokesCachedTaskAndCapabilityButAllowsActiveAccount(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	setAuthorizationTestDB(t, db)
	require.NoError(t, db.Exec("CREATE TABLE public.account_storage_cleanups (user_id uuid PRIMARY KEY)").Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS public.account_storage_cleanups").Error })
	t.Setenv("STORAGE_MODE", "local")

	app := authorizationTestApp(t, registry)
	activeOwner := uuid.NewString()
	activeFile := filepath.Join(t.TempDir(), "active-account-output.pdf")
	require.NoError(t, os.WriteFile(activeFile, []byte("active account artifact"), 0o600))
	activeTask := setCachedCompletedTask(t, registry, "active-account-task", activeOwner, "active-account-token", activeFile)

	activeRequest := httptest.NewRequest(http.MethodGet, "/v1/download/"+activeTask.ID, nil)
	activeRequest.Header.Set("X-Test-Identity", activeOwner)
	activeRequest.Header.Set("X-Test-Identity-Type", string(identity.TypeUser))
	activeResponse, err := app.Test(activeRequest)
	require.NoError(t, err)
	activeResponse.Body.Close()
	require.Equal(t, fiber.StatusOK, activeResponse.StatusCode)

	deletedOwner := uuid.NewString()
	deletedFile := filepath.Join(t.TempDir(), "deleted-account-output.pdf")
	require.NoError(t, os.WriteFile(deletedFile, []byte("deleted account artifact"), 0o600))
	deletedTask := setCachedCompletedTask(t, registry, "deleted-account-cached-task", deletedOwner, "issued-before-deletion", deletedFile)
	require.NoError(t, db.Exec("INSERT INTO public.account_storage_cleanups(user_id) VALUES (?)", deletedOwner).Error)

	for _, path := range []string{
		"/v1/download/" + deletedTask.ID + "?token=" + deletedTask.DownloadToken,
		"/v1/tasks/" + deletedTask.ID + "?token=" + deletedTask.DownloadToken,
		"/v1/tasks/" + deletedTask.ID + "/progress?token=" + deletedTask.DownloadToken,
	} {
		t.Run(path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, path, nil)
			request.Header.Set("X-Test-Identity", deletedOwner)
			request.Header.Set("X-Test-Identity-Type", string(identity.TypeUser))
			response, err := app.Test(request)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, fiber.StatusForbidden, response.StatusCode)
		})
	}
}

func TestProgressWebSocketRechecksAccountDeletion(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	setAuthorizationTestDB(t, db)
	require.NoError(t, db.Exec("CREATE TABLE public.account_storage_cleanups (user_id uuid PRIMARY KEY)").Error)
	t.Cleanup(func() { _ = db.Exec("DROP TABLE IF EXISTS public.account_storage_cleanups").Error })

	ownerID := uuid.NewString()
	_, err := registry.SetWithDownloadToken("live-progress-task", "PROCESSING", 25, "", "", ownerID, "progress-capability")
	require.NoError(t, err)
	app := authorizationTestApp(t, registry)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	serverErr := make(chan error, 1)
	go func() { serverErr <- app.Listener(listener) }()
	t.Cleanup(func() {
		_ = app.Shutdown()
		select {
		case <-serverErr:
		case <-time.After(2 * time.Second):
		}
	})

	requestURL := "ws://" + listener.Addr().String() + "/v1/tasks/live-progress-task/progress"
	header := http.Header{
		"X-Test-Identity":      []string{ownerID},
		"X-Test-Identity-Type": []string{string(identity.TypeUser)},
	}
	connection, response, err := websocket.DefaultDialer.Dial(requestURL, header)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		require.NoError(t, err)
	}
	defer connection.Close()

	var first map[string]any
	require.NoError(t, connection.SetReadDeadline(time.Now().Add(2*time.Second)))
	require.NoError(t, connection.ReadJSON(&first))
	require.Equal(t, "PROCESSING", first["status"])
	require.NoError(t, db.Exec("INSERT INTO public.account_storage_cleanups(user_id) VALUES (?)", ownerID).Error)

	require.NoError(t, connection.SetReadDeadline(time.Now().Add(3*time.Second)))
	_, _, err = connection.ReadMessage()
	require.Error(t, err, "revoked progress stream must close instead of sending another update")
}

func TestMissingDeletionLedgerFailsClosedForAccountIdentity(t *testing.T) {
	db, registry, _ := setupTasksPostgresDB(t)
	setAuthorizationTestDB(t, db)
	ownerID := uuid.NewString()
	_, err := registry.SetWithDownloadToken("missing-ledger-task", "COMPLETED", 100, "", "", ownerID, "missing-ledger-token")
	require.NoError(t, err)
	app := authorizationTestApp(t, registry)
	request := httptest.NewRequest(http.MethodGet, "/v1/tasks/missing-ledger-task?token=missing-ledger-token", nil)
	request.Header.Set("X-Test-Identity", ownerID)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeUser))
	response, err := app.Test(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, fiber.StatusForbidden, response.StatusCode)
}
