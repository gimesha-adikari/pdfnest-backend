package conversion_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/conversion"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/worker"

	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

var (
	pdfToMarkdownR2Once          sync.Once
	pdfToMarkdownR2Server        *httptest.Server
	pdfToMarkdownR2DeleteRequest atomic.Int32
)

func TestMain(m *testing.M) {
	code := m.Run()
	if pdfToMarkdownR2Server != nil {
		pdfToMarkdownR2Server.Close()
	}
	os.Exit(code)
}

func TestPDFToMarkdownRouteAliasesAllocateOnceForGuest(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)

	app, redisClient, workerCalls, _ := newPDFToMarkdownRouteTestApp(t)
	tasks.RegisterRoutes(app.Group("/api"))
	createdTasks := 0
	for _, route := range []string{
		"/api/conversion/pdf-to-markdown-async",
		"/api/conversion/pdf-to-markdown",
	} {
		t.Run(strings.TrimPrefix(route, "/api/conversion/"), func(t *testing.T) {
			guestID := "gim11-guest-" + uuid.NewString()
			response, body := postPDFToMarkdownRoute(t, app, route, guestID, string(identity.TypeGuest))
			if response != fiber.StatusAccepted {
				t.Errorf("valid request should create one task and reach worker submission; got status %d body %s", response, body)
				return
			}
			var accepted struct {
				TaskID string `json:"task_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &accepted))
			require.NotEmpty(t, accepted.TaskID)
			task, err := tasks.Registry.Get(accepted.TaskID)
			require.NoError(t, err)
			require.NotNil(t, task)
			require.NotEmpty(t, task.ReservationID)
			require.Equal(t, string(billing.ReservationKindGuest), task.ReservationKind)
			createdTasks++

			stateKey := "platen:guestquota:state:" + guestID
			state, err := redisClient.HGetAll(context.Background(), stateKey).Result()
			require.NoError(t, err)
			require.Equal(t, "4", state["pending_3h"], "one four-unit task should hold one guest allocation")
			require.Equal(t, "0", state["used_3h"], "a queued task must not be charged by the route middleware")

			resultFile, err := os.CreateTemp(t.TempDir(), "gim11-result-*.md")
			require.NoError(t, err)
			_, err = resultFile.WriteString("# Result\n")
			require.NoError(t, err)
			require.NoError(t, resultFile.Close())
			require.NoError(t, tasks.Registry.Set(accepted.TaskID, "COMPLETED", 100, resultFile.Name(), ""))

			unauthorizedStatus := getPDFToMarkdownTaskDownload(t, app, accepted.TaskID, "other-guest")
			require.Equal(t, fiber.StatusForbidden, unauthorizedStatus)
			state, err = redisClient.HGetAll(context.Background(), stateKey).Result()
			require.NoError(t, err)
			require.Equal(t, "4", state["pending_3h"], "an unauthorized download must not settle billing")

			t.Setenv("BILLING_MODE", "free")
			require.Equal(t, fiber.StatusOK, getPDFToMarkdownTaskDownload(t, app, accepted.TaskID, guestID))
			require.Equal(t, fiber.StatusOK, getPDFToMarkdownTaskDownload(t, app, accepted.TaskID, guestID))
			state, err = redisClient.HGetAll(context.Background(), stateKey).Result()
			require.NoError(t, err)
			require.Equal(t, "0", state["pending_3h"])
			require.Equal(t, "4", state["used_3h"], "repeated authorized download must commit exactly once")
			reservationKey := "platen:guestquota:res:" + task.ReservationID
			require.EqualValues(t, 0, redisClient.Exists(context.Background(), reservationKey).Val())
		})
	}
	require.Equal(t, int32(createdTasks), workerCalls.Load())
	require.Equal(t, 2, createdTasks, "both public aliases should use the same one-allocation async lifecycle")
}

func TestPDFToMarkdownRouteAliasesCreateOneAccountReservation(t *testing.T) {
	db := setupGIM11IsolatedTestDB(t)
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)

	app, _, _, _ := newPDFToMarkdownRouteTestApp(t)
	tasks.RegisterRoutes(app.Group("/api"))
	for _, route := range []string{
		"/api/conversion/pdf-to-markdown-async",
		"/api/conversion/pdf-to-markdown",
	} {
		t.Run(strings.TrimPrefix(route, "/api/conversion/"), func(t *testing.T) {
			userID := createGIM11Account(t, db)
			response, body := postPDFToMarkdownRoute(t, app, route, userID, string(identity.TypeUser))
			require.Equal(t, fiber.StatusAccepted, response, body)
			var accepted struct {
				TaskID string `json:"task_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &accepted))
			require.NotEmpty(t, accepted.TaskID)
			task, err := tasks.Registry.Get(accepted.TaskID)
			require.NoError(t, err)
			require.NotNil(t, task)
			require.NotEmpty(t, task.ReservationID)
			require.Equal(t, string(billing.ReservationKindDatabase), task.ReservationKind)

			var reservations []config.BillingReservation
			require.NoError(t, db.Where("user_id = ?", userID).Find(&reservations).Error)
			require.Len(t, reservations, 1, "one HTTP request must create exactly one billing reservation")
			require.Equal(t, "reserved", reservations[0].Status,
				"the task reservation remains pending until successful result finalization")
			require.Equal(t, 4, reservations[0].Units)
			require.Equal(t, 4, reservations[0].PlanUnits)
			require.Zero(t, reservations[0].CreditUnits)

			resultFile, err := os.CreateTemp(t.TempDir(), "gim11-account-result-*.md")
			require.NoError(t, err)
			_, err = resultFile.WriteString("# Result\n")
			require.NoError(t, err)
			require.NoError(t, resultFile.Close())
			require.NoError(t, tasks.Registry.Set(accepted.TaskID, "COMPLETED", 100, resultFile.Name(), ""))
			require.Equal(t, fiber.StatusForbidden, getPDFToMarkdownTaskDownload(t, app, accepted.TaskID, "other-user"))
			t.Setenv("BILLING_MODE", "free")
			require.Equal(t, fiber.StatusOK, getPDFToMarkdownTaskDownload(t, app, accepted.TaskID, userID))
			require.Equal(t, fiber.StatusOK, getPDFToMarkdownTaskDownload(t, app, accepted.TaskID, userID))

			require.NoError(t, db.Where("user_id = ?", userID).Find(&reservations).Error)
			require.Len(t, reservations, 1)
			require.Equal(t, "committed", reservations[0].Status)
			var subscription config.Subscription
			require.NoError(t, db.Where("user_id = ?", userID).First(&subscription).Error)
			require.Equal(t, 4, subscription.UsedUnits3h)
			require.Equal(t, 4, subscription.UsedUnitsDaily)
			require.Equal(t, 4, subscription.UsedUnitsMonthly)
		})
	}
}

func TestPDFToMarkdownAccountWorkerFailureReleasesReservation(t *testing.T) {
	db := setupGIM11IsolatedTestDB(t)
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)
	app, _, _, workerStatus := newPDFToMarkdownRouteTestApp(t)
	workerStatus.Store(http.StatusBadGateway)
	userID := createGIM11Account(t, db)
	response, body := postPDFToMarkdownRoute(
		t, app, "/api/conversion/pdf-to-markdown", userID, string(identity.TypeUser),
	)
	require.Equal(t, fiber.StatusBadGateway, response, body)
	var reservations []config.BillingReservation
	require.NoError(t, db.Where("user_id = ?", userID).Find(&reservations).Error)
	require.Len(t, reservations, 1)
	require.Equal(t, "released", reservations[0].Status)
	require.Equal(t, 4, reservations[0].Units)
	require.Equal(t, 4, reservations[0].PlanUnits)
	require.Zero(t, reservations[0].CreditUnits)
	var subscription config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&subscription).Error)
	require.Zero(t, subscription.UsedUnits3h)
	require.Zero(t, subscription.UsedUnitsDaily)
	require.Zero(t, subscription.UsedUnitsMonthly)
	require.Zero(t, subscription.CustomCredits)
}

func TestPDFToMarkdownAccountCancellationReleasesReservation(t *testing.T) {
	db := setupGIM11IsolatedTestDB(t)
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)
	app, _, _, _ := newPDFToMarkdownRouteTestApp(t)
	tasks.RegisterRoutes(app.Group("/api"))
	userID := createGIM11Account(t, db)
	response, body := postPDFToMarkdownRoute(
		t, app, "/api/conversion/pdf-to-markdown", userID, string(identity.TypeUser),
	)
	require.Equal(t, fiber.StatusAccepted, response, body)
	var accepted struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &accepted))
	task, err := tasks.Registry.Get(accepted.TaskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.NotEmpty(t, task.ReservationID)
	require.Equal(t, string(billing.ReservationKindDatabase), task.ReservationKind)

	var reservation config.BillingReservation
	require.NoError(t, db.Where("id = ?", task.ReservationID).First(&reservation).Error)
	require.Equal(t, "reserved", reservation.Status)
	require.Equal(t, fiber.StatusOK, cancelPDFToMarkdownTask(t, app, accepted.TaskID, userID))
	require.Equal(t, fiber.StatusOK, cancelPDFToMarkdownTask(t, app, accepted.TaskID, userID))
	require.NoError(t, db.Where("id = ?", task.ReservationID).First(&reservation).Error)
	require.Equal(t, "released", reservation.Status)
	var subscription config.Subscription
	require.NoError(t, db.Where("user_id = ?", userID).First(&subscription).Error)
	require.Zero(t, subscription.UsedUnits3h)
	require.Zero(t, subscription.UsedUnitsDaily)
	require.Zero(t, subscription.UsedUnitsMonthly)
}

func TestPDFToMarkdownReleasesReservationWhenTaskPersistenceFails(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)
	app, redisClient, workerCalls, _ := newPDFToMarkdownRouteTestApp(t)
	redisFailure := &failTaskPersistenceHook{}
	redisClient.AddHook(redisFailure)
	redisFailure.enabled.Store(true)

	guestID := "gim11-task-persist-failure-" + uuid.NewString()
	deletesBefore := pdfToMarkdownR2DeleteRequest.Load()
	response, body := postPDFToMarkdownRoute(
		t, app, "/api/conversion/pdf-to-markdown-async", guestID, string(identity.TypeGuest),
	)
	require.Equal(t, fiber.StatusServiceUnavailable, response, body)
	require.Contains(t, body, `"code":"TASK_STORAGE_UNAVAILABLE"`)
	require.Zero(t, workerCalls.Load(), "worker submission must not happen without a persisted task")

	state, err := redisClient.HGetAll(context.Background(), "platen:guestquota:state:"+guestID).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["pending_3h"], "failed task creation must release the one guest reservation")
	require.Equal(t, "0", state["used_3h"])
	reservationKeys, err := redisClient.Keys(context.Background(), "platen:guestquota:res:*").Result()
	require.NoError(t, err)
	require.Empty(t, reservationKeys, "no billing reservation may remain after failed task creation")
	require.Equal(t, deletesBefore+1, pdfToMarkdownR2DeleteRequest.Load(), "uploaded source object should be cleaned when task persistence fails")
}

func TestPDFToMarkdownWorkerSubmissionFailureReleasesGuestReservation(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)
	app, redisClient, workerCalls, workerStatus := newPDFToMarkdownRouteTestApp(t)
	tasks.RegisterRoutes(app.Group("/api"))
	workerStatus.Store(http.StatusBadGateway)
	guestID := "gim11-worker-failure-" + uuid.NewString()
	response, body := postPDFToMarkdownRoute(
		t, app, "/api/conversion/pdf-to-markdown", guestID, string(identity.TypeGuest),
	)
	require.Equal(t, fiber.StatusBadGateway, response, body)
	require.Contains(t, body, `"code":"WORKER_DISPATCH_ERR"`)
	require.EqualValues(t, 1, workerCalls.Load())

	state, err := redisClient.HGetAll(context.Background(), "platen:guestquota:state:"+guestID).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["pending_3h"], "worker failure must release the single reservation")
	require.Equal(t, "0", state["used_3h"])
	keys, err := redisClient.Keys(context.Background(), tasks.TaskKeyPrefix+"*").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	taskID := strings.TrimPrefix(keys[0], tasks.TaskKeyPrefix)
	task, err := tasks.Registry.Get(taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Equal(t, "FAILED", task.Status)
	require.NotEmpty(t, task.ReservationID)
	require.Equal(t, string(billing.ReservationKindGuest), task.ReservationKind)
	reservationKeys, err := redisClient.Keys(context.Background(), "platen:guestquota:res:*").Result()
	require.NoError(t, err)
	require.Empty(t, reservationKeys)
}

func TestPDFToMarkdownFreeGuestTaskStaysUnbilledAfterSwitchToNormal(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	setPDFToMarkdownR2Environment(t)
	app, redisClient, _, _ := newPDFToMarkdownRouteTestApp(t)
	tasks.RegisterRoutes(app.Group("/api"))
	billing.GuestQuota = nil
	guestID := "gim11-free-guest-" + uuid.NewString()
	response, body := postPDFToMarkdownRoute(
		t, app, "/api/conversion/pdf-to-markdown", guestID, string(identity.TypeGuest),
	)
	require.Equal(t, fiber.StatusAccepted, response, body)
	var accepted struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &accepted))
	task, err := tasks.Registry.Get(accepted.TaskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Empty(t, task.ReservationID)
	require.Equal(t, string(billing.ReservationKindGuest), task.ReservationKind)

	quotaKeys, err := redisClient.Keys(context.Background(), "platen:guestquota:*").Result()
	require.NoError(t, err)
	require.Empty(t, quotaKeys, "free guest allocation must not create billing quota state")

	resultFile, err := os.CreateTemp(t.TempDir(), "gim11-free-result-*.md")
	require.NoError(t, err)
	_, err = resultFile.WriteString("# Free Result\n")
	require.NoError(t, err)
	require.NoError(t, resultFile.Close())
	require.NoError(t, tasks.Registry.Set(accepted.TaskID, "COMPLETED", 100, resultFile.Name(), ""))
	t.Setenv("BILLING_MODE", "normal")
	require.Equal(t, fiber.StatusOK, getPDFToMarkdownTaskDownload(t, app, accepted.TaskID, guestID))
	quotaKeys, err = redisClient.Keys(context.Background(), "platen:guestquota:*").Result()
	require.NoError(t, err)
	require.Empty(t, quotaKeys, "completion after switching to normal must not create guest billing state retroactively")
}

func TestPDFToMarkdownFreeAccountTaskStaysUnbilledAfterSwitchToNormal(t *testing.T) {
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })
	t.Setenv("BILLING_MODE", "free")
	setPDFToMarkdownR2Environment(t)
	app, _, _, _ := newPDFToMarkdownRouteTestApp(t)
	tasks.RegisterRoutes(app.Group("/api"))
	userID := "gim11-free-account-" + uuid.NewString()
	response, body := postPDFToMarkdownRoute(
		t, app, "/api/conversion/pdf-to-markdown", userID, string(identity.TypeUser),
	)
	require.Equal(t, fiber.StatusAccepted, response, body)
	var accepted struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &accepted))
	task, err := tasks.Registry.Get(accepted.TaskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.Empty(t, task.ReservationID)
	require.Equal(t, string(billing.ReservationKindDatabase), task.ReservationKind)

	resultFile, err := os.CreateTemp(t.TempDir(), "gim11-free-account-result-*.md")
	require.NoError(t, err)
	_, err = resultFile.WriteString("# Free Account Result\n")
	require.NoError(t, err)
	require.NoError(t, resultFile.Close())
	require.NoError(t, tasks.Registry.Set(accepted.TaskID, "COMPLETED", 100, resultFile.Name(), ""))
	t.Setenv("BILLING_MODE", "normal")
	require.Equal(t, fiber.StatusOK, getPDFToMarkdownTaskDownload(t, app, accepted.TaskID, userID))
	require.Nil(t, config.DB, "empty-ID finalization must not need billing PostgreSQL")
}

func TestPDFToMarkdownStaleTaskReleasesStoredGuestReservation(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)
	app, redisClient, _, _ := newPDFToMarkdownRouteTestApp(t)
	guestID := "gim11-stale-" + uuid.NewString()
	response, body := postPDFToMarkdownRoute(
		t, app, "/api/conversion/pdf-to-markdown-async", guestID, string(identity.TypeGuest),
	)
	require.Equal(t, fiber.StatusAccepted, response, body)
	var accepted struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &accepted))
	task, err := tasks.Registry.Get(accepted.TaskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.NotEmpty(t, task.ReservationID)
	require.Equal(t, string(billing.ReservationKindGuest), task.ReservationKind)
	task.UpdatedAt = time.Now().Add(-2 * time.Minute).Unix()
	stalePayload, err := json.Marshal(task)
	require.NoError(t, err)
	require.NoError(t, redisClient.Set(context.Background(), tasks.TaskKeyPrefix+task.ID, stalePayload, tasks.TaskTTL).Err())
	t.Setenv("STUCK_TASK_TIMEOUT_SECONDS", "1")

	staleTask, transitioned, reservationID, err := tasks.Registry.GetWithTransition(task.ID)
	require.NoError(t, err)
	require.True(t, transitioned)
	require.Equal(t, task.ReservationID, reservationID)
	require.Equal(t, "FAILED", staleTask.Status)
	state, err := redisClient.HGetAll(context.Background(), "platen:guestquota:state:"+guestID).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["pending_3h"], "stale cleanup must release the stored guest reservation")
	require.Equal(t, "0", state["used_3h"])
}

func TestPDFToMarkdownCancellationReleasesGuestReservation(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)
	app, redisClient, _, _ := newPDFToMarkdownRouteTestApp(t)
	tasks.RegisterRoutes(app.Group("/api"))
	guestID := "gim11-cancel-" + uuid.NewString()
	response, body := postPDFToMarkdownRoute(
		t, app, "/api/conversion/pdf-to-markdown-async", guestID, string(identity.TypeGuest),
	)
	require.Equal(t, fiber.StatusAccepted, response, body)
	var accepted struct {
		TaskID string `json:"task_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &accepted))
	task, err := tasks.Registry.Get(accepted.TaskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	require.NotEmpty(t, task.ReservationID)

	stateKey := "platen:guestquota:state:" + guestID
	state, err := redisClient.HGetAll(context.Background(), stateKey).Result()
	require.NoError(t, err)
	require.Equal(t, "4", state["pending_3h"])

	require.Equal(t, fiber.StatusOK, cancelPDFToMarkdownTask(t, app, accepted.TaskID, guestID))
	state, err = redisClient.HGetAll(context.Background(), stateKey).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["pending_3h"], "cancelling a queued task must release its reservation")
	require.Equal(t, "0", state["used_3h"], "cancellation must not consume guest quota")
	require.EqualValues(t, 0, redisClient.Exists(context.Background(), "platen:guestquota:res:"+task.ReservationID).Val())

	require.Equal(t, fiber.StatusOK, cancelPDFToMarkdownTask(t, app, accepted.TaskID, guestID))
	state, err = redisClient.HGetAll(context.Background(), stateKey).Result()
	require.NoError(t, err)
	require.Equal(t, "0", state["pending_3h"], "repeated cancellation must stay settled")
	require.Equal(t, "0", state["used_3h"])
}

func TestPDFToMarkdownRouteAliasesReplayTheSameIdempotentTask(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)
	app, redisClient, workerCalls, _ := newPDFToMarkdownRouteTestApp(t)
	key := "gim11-idempotency-" + uuid.NewString()

	for _, route := range []string{
		"/api/conversion/pdf-to-markdown-async",
		"/api/conversion/pdf-to-markdown",
	} {
		t.Run(strings.TrimPrefix(route, "/api/conversion/"), func(t *testing.T) {
			guestID := "gim11-idempotency-guest-" + uuid.NewString()
			firstStatus, firstBody := postPDFToMarkdownRouteWithKey(
				t, app, route, guestID, string(identity.TypeGuest), key,
			)
			require.Equal(t, fiber.StatusAccepted, firstStatus, firstBody)
			var first struct {
				TaskID string `json:"task_id"`
			}
			require.NoError(t, json.Unmarshal([]byte(firstBody), &first))
			require.NotEmpty(t, first.TaskID)
			task, err := tasks.Registry.Get(first.TaskID)
			require.NoError(t, err)
			require.NotNil(t, task)
			require.NotEmpty(t, task.ReservationID)
			require.Equal(t, string(billing.ReservationKindGuest), task.ReservationKind)

			secondStatus, secondBody := postPDFToMarkdownRouteWithKey(
				t, app, route, guestID, string(identity.TypeGuest), key,
			)
			require.Equal(t, fiber.StatusAccepted, secondStatus, secondBody)
			var replay struct {
				TaskID string `json:"taskId"`
			}
			require.NoError(t, json.Unmarshal([]byte(secondBody), &replay))
			require.Equal(t, first.TaskID, replay.TaskID, "same idempotency key must return the existing task")
			state, err := redisClient.HGetAll(context.Background(), "platen:guestquota:state:"+guestID).Result()
			require.NoError(t, err)
			require.Equal(t, "4", state["pending_3h"], "replaying a normal-mode task must not allocate guest quota twice")
			require.Equal(t, "0", state["used_3h"])
			require.EqualValues(t, 1, redisClient.Exists(context.Background(), "platen:guestquota:res:"+task.ReservationID).Val())
		})
	}
	require.Equal(t, int32(2), workerCalls.Load(), "each alias should submit once; replays must not submit another job")
}

func TestPDFToMarkdownValidationFailureReleasesIdempotencyKey(t *testing.T) {
	t.Setenv("BILLING_MODE", "normal")
	setPDFToMarkdownR2Environment(t)
	app, _, workerCalls, _ := newPDFToMarkdownRouteTestApp(t)
	key := "gim11-invalid-retry-" + uuid.NewString()
	for _, route := range []string{
		"/api/conversion/pdf-to-markdown-async",
		"/api/conversion/pdf-to-markdown",
	} {
		t.Run(strings.TrimPrefix(route, "/api/conversion/"), func(t *testing.T) {
			guestID := "gim11-invalid-guest-" + uuid.NewString()
			for attempt := 0; attempt < 2; attempt++ {
				status, body := postInvalidPDFToMarkdownRoute(t, app, route, guestID, key)
				require.Equal(t, fiber.StatusBadRequest, status, body)
				require.Contains(t, body, `"code":"MISSING_FILE"`)
			}
		})
	}
	require.Zero(t, workerCalls.Load())
}

func newPDFToMarkdownRouteTestApp(t *testing.T) (*fiber.App, *redis.Client, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	previousRedis := config.Redis
	previousQuota := billing.GuestQuota
	previousGovernor := limiter.Default
	previousStale := tasks.StaleTaskBillingHandler
	previousCommit := tasks.CommitTaskBillingHandler
	previousStaleWithKind := tasks.StaleTaskBillingHandlerWithKind
	previousCommitWithKind := tasks.CommitTaskBillingHandlerWithKind
	previousCancel := tasks.CancelTaskBillingHandler
	previousCancelWithKind := tasks.CancelTaskBillingHandlerWithKind
	previousWorkerClient := worker.Client
	config.Redis = client
	billing.Initialize(billing.NewGuestQuotaStore(client, time.Hour))
	limiter.Default = limiter.NewGovernor()
	workerCalls := &atomic.Int32{}
	workerStatus := &atomic.Int32{}
	workerStatus.Store(http.StatusAccepted)
	worker.Client = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.Path != "/api/v1/jobs/submit" {
			return nil, fmt.Errorf("unexpected worker request %s %s", request.Method, request.URL.Path)
		}
		workerCalls.Add(1)
		statusCode := int(workerStatus.Load())
		return &http.Response{
			Status: fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)), StatusCode: statusCode,
			Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
			Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"success":true}`)), Request: request,
		}, nil
	})}
	t.Cleanup(func() {
		config.Redis = previousRedis
		billing.GuestQuota = previousQuota
		limiter.Default = previousGovernor
		tasks.StaleTaskBillingHandler = previousStale
		tasks.CommitTaskBillingHandler = previousCommit
		tasks.StaleTaskBillingHandlerWithKind = previousStaleWithKind
		tasks.CommitTaskBillingHandlerWithKind = previousCommitWithKind
		tasks.CancelTaskBillingHandler = previousCancel
		tasks.CancelTaskBillingHandlerWithKind = previousCancelWithKind
		worker.Client = previousWorkerClient
		_ = client.Close()
	})

	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		id := c.Get("X-Test-Identity")
		kind := identity.Type(c.Get("X-Test-Identity-Type"))
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: id, QuotaID: id, Type: kind})
		c.Locals(identity.LocalIdentityIDKey, id)
		c.Locals(identity.LocalIdentityType, string(kind))
		c.Locals(identity.LocalUserIDKey, id)
		return c.Next()
	})
	conversion.RegisterRoutes(app.Group("/api"), conversion.NewController(conversion.NewService()))
	return app, client, workerCalls, workerStatus
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type failTaskPersistenceHook struct {
	enabled atomic.Bool
}

func (h *failTaskPersistenceHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

func (h *failTaskPersistenceHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		if h.enabled.Load() && strings.EqualFold(command.Name(), "eval") {
			for _, arg := range command.Args() {
				if strings.Contains(fmt.Sprint(arg), tasks.TaskKeyPrefix) {
					return errors.New("simulated task state write failure")
				}
			}
		}
		return next(ctx, command)
	}
}

func (h *failTaskPersistenceHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func setPDFToMarkdownR2Environment(t *testing.T) {
	t.Helper()
	pdfToMarkdownR2Once.Do(func() {
		pdfToMarkdownR2Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodDelete {
				pdfToMarkdownR2DeleteRequest.Add(1)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if r.Method == http.MethodGet && r.URL.Query().Has("location") {
				w.Header().Set("Content-Type", "application/xml")
				_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", `"gim11-test-etag"`)
			w.WriteHeader(http.StatusOK)
		}))
	})
	t.Setenv("R2_BUCKET", "gim11-test")
	t.Setenv("R2_ACCESS_KEY", "gim11-test-access")
	t.Setenv("R2_SECRET_KEY", "gim11-test-secret")
	t.Setenv("R2_ENDPOINT", pdfToMarkdownR2Server.URL)
	t.Setenv("PDFNEST_WORKER_URL", "http://worker.test")
}

func postPDFToMarkdownRoute(t *testing.T, app *fiber.App, route, identityID, identityType string) (int, string) {
	return postPDFToMarkdownRouteWithKey(t, app, route, identityID, identityType, "")
}

func postPDFToMarkdownRouteWithKey(t *testing.T, app *fiber.App, route, identityID, identityType, idempotencyKey string) (int, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "one-page.pdf")
	require.NoError(t, err)
	_, err = part.Write(singlePagePDF())
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	request := httptest.NewRequest(http.MethodPost, route, body)
	request.Header.Set(fiber.HeaderContentType, writer.FormDataContentType())
	request.Header.Set("X-Test-Identity", identityID)
	request.Header.Set("X-Test-Identity-Type", identityType)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := app.Test(request, 5000)
	require.NoError(t, err)
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(responseBody)
}

func postInvalidPDFToMarkdownRoute(t *testing.T, app *fiber.App, route, identityID, idempotencyKey string) (int, string) {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("file", "not-a-pdf.pdf")
	require.NoError(t, err)
	_, err = part.Write([]byte("not a PDF file"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	request := httptest.NewRequest(http.MethodPost, route, body)
	request.Header.Set(fiber.HeaderContentType, writer.FormDataContentType())
	request.Header.Set("X-Test-Identity", identityID)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeGuest))
	request.Header.Set("Idempotency-Key", idempotencyKey)
	response, err := app.Test(request, 5000)
	require.NoError(t, err)
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	return response.StatusCode, string(responseBody)
}

func getPDFToMarkdownTaskDownload(t *testing.T, app *fiber.App, taskID, identityID string) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/download/"+taskID, nil)
	request.Header.Set("X-Test-Identity", identityID)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeGuest))
	response, err := app.Test(request, 5000)
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	return response.StatusCode
}

func cancelPDFToMarkdownTask(t *testing.T, app *fiber.App, taskID, identityID string) int {
	t.Helper()
	request := httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+taskID, nil)
	request.Header.Set("X-Test-Identity", identityID)
	request.Header.Set("X-Test-Identity-Type", string(identity.TypeGuest))
	response, err := app.Test(request, 5000)
	require.NoError(t, err)
	defer response.Body.Close()
	_, err = io.Copy(io.Discard, response.Body)
	require.NoError(t, err)
	return response.StatusCode
}

func singlePagePDF() []byte {
	var pdf bytes.Buffer
	pdf.WriteString("%PDF-1.4\n")
	offsets := []int{0}
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] >>",
	}
	for index, object := range objects {
		offsets = append(offsets, pdf.Len())
		_, _ = fmt.Fprintf(&pdf, "%d 0 obj\n%s\nendobj\n", index+1, object)
	}
	xrefOffset := pdf.Len()
	pdf.WriteString("xref\n0 4\n0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		_, _ = fmt.Fprintf(&pdf, "%010d 00000 n \n", offset)
	}
	_, _ = fmt.Fprintf(&pdf, "trailer\n<< /Size 4 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xrefOffset)
	return pdf.Bytes()
}

func setupGIM11IsolatedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("GIM6_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("GIM6_TEST_DATABASE_URL is unset; isolated PDF-to-Markdown PostgreSQL assertions were not run")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err, "GIM6_TEST_DATABASE_URL must be a PostgreSQL URL")
	require.True(t, parsed.Scheme == "postgres" || parsed.Scheme == "postgresql")
	host := strings.ToLower(parsed.Hostname())
	require.True(t, host == "localhost" || host == "127.0.0.1" || host == "::1",
		"GIM6_TEST_DATABASE_URL must point to local PostgreSQL")
	for _, name := range []string{"host", "hostaddr", "service"} {
		require.Empty(t, parsed.Query().Get(name), "GIM6_TEST_DATABASE_URL must not override its local host via %s", name)
	}
	databaseName, err := url.PathUnescape(strings.TrimPrefix(parsed.EscapedPath(), "/"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(databaseName, "test_") || strings.HasSuffix(databaseName, "_test"),
		"GIM6_TEST_DATABASE_URL database name must start with test_ or end with _test")

	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Skipf("explicitly configured isolated local PostgreSQL is unavailable: %v", err)
	}
	require.NoError(t, db.AutoMigrate(&config.User{}, &config.Subscription{}, &config.BillingReservation{}, &config.UsageLog{}))
	previousDB := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = previousDB })
	return db
}

func createGIM11Account(t *testing.T, db *gorm.DB) string {
	t.Helper()
	userID := uuid.NewString()
	now := time.Now()
	require.NoError(t, db.Create(&config.User{
		ID: userID, Email: userID + "@example.test", CreatedAt: now, UpdatedAt: now,
	}).Error)
	subscriptionID := uuid.NewString()
	require.NoError(t, db.Create(&config.Subscription{
		ID: subscriptionID, UserID: userID,
		PaddleCustomerID:     "gim11_customer_" + subscriptionID,
		PaddleSubscriptionID: "gim11_subscription_" + subscriptionID,
		Tier:                 "free", Status: "active", CurrentPeriodEnd: now.AddDate(1, 0, 0),
		Window3HResetAt: now.Add(3 * time.Hour), WindowDailyResetAt: now.Add(24 * time.Hour),
		WindowMonthlyResetAt: now.AddDate(0, 1, 0), CreatedAt: now, UpdatedAt: now,
	}).Error)
	t.Cleanup(func() {
		_ = db.Where("user_id = ?", userID).Delete(&config.BillingReservation{}).Error
		_ = db.Where("user_id = ?", userID).Delete(&config.UsageLog{}).Error
		_ = db.Where("user_id = ?", userID).Delete(&config.Subscription{}).Error
		_ = db.Where("id = ?", userID).Delete(&config.User{}).Error
	})
	return userID
}
