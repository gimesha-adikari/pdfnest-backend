package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// These opt-in characterization tests deliberately preserve evidence of the
// current inconsistency. A passing test is NOT a fixed billing/publication
// guarantee. Replace the bad-outcome assertions after an approved durable
// settlement/recovery design is implemented. No production hooks are added.
//
// PLATEN_M1_BILLING_TEST_DATABASE_URL must name a disposable loopback test DB.
func TestR2BillingQualificationCurrentOrdering(t *testing.T) {
	for _, allocation := range []string{"plan", "credits"} {
		for _, scenario := range []string{"cancel_before_commit", "cancel_after_commit", "redis_completion_failure", "redis_completion_ack_lost", "success_duplicate_and_late_cancel"} {
			t.Run(allocation+"/"+scenario, func(t *testing.T) {
				db := qualificationBillingDB(t)
				t.Setenv("BILLING_MODE", "normal")
				t.Setenv("STORAGE_MODE", "local")
				t.Setenv("APP_ENV", "development")
				root := t.TempDir()
				t.Setenv("LOCAL_STORAGE_DIR", root)
				client := qualificationTaskState(t)
				owner, initial := qualificationAccount(t, db, allocation == "credits")
				gate := newCompletionGate(scenario == "redis_completion_failure")
				gate.failAfter = scenario == "redis_completion_ack_lost"
				client.AddHook(gate)
				var commitEntered, allowCommit chan struct{}
				if scenario == "cancel_before_commit" {
					commitEntered, allowCommit = make(chan struct{}), make(chan struct{})
					var intercepted atomic.Bool
					require.NoError(t, db.Callback().Query().Before("gorm:query").Register("m1_s207_before_commit", func(tx *gorm.DB) {
						_, locked := tx.Statement.Clauses["FOR"]
						if tx.Statement.Table != "billing_reservations" || !locked || !intercepted.CompareAndSwap(false, true) {
							return
						}
						close(commitEntered)
						select {
						case <-allowCommit:
						case <-time.After(10 * time.Second):
							tx.AddError(errors.New("test commit gate timed out"))
						}
					}))
					t.Cleanup(func() { _ = db.Callback().Query().Remove("m1_s207_before_commit") })
				}
				output := writeSyntheticOnePagePDF(t)
				app := qualificationApp(owner, NewController(&completedR2JobService{outputPath: output}))
				taskID := qualificationSubmit(t, app, owner)
				// Never leave the actual worker goroutine blocked when a test fails.
				var releaseCommit sync.Once
				unblockCommit := func() {
					if allowCommit != nil {
						releaseCommit.Do(func() { close(allowCommit) })
					}
				}
				defer func() {
					unblockCommit()
					gate.release()
					require.Eventually(t, func() bool { _, err := os.Stat(output); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond, "task-owned processor output must be cleaned")
				}()
				var reservation config.BillingReservation
				if scenario == "cancel_before_commit" {
					awaitQualification(t, commitEntered)
					require.NoError(t, db.Where("task_id = ?", taskID).First(&reservation).Error)
					require.Equal(t, "reserved", reservation.Status)
					require.FileExists(t, qualificationArtifact(root, taskID, output))
					require.Equal(t, http.StatusOK, qualificationCancel(t, app, taskID))
					require.NoError(t, db.Where("id = ?", reservation.ID).First(&reservation).Error)
					require.Equal(t, "released", reservation.Status)
					unblockCommit()
				}
				awaitQualification(t, gate.entered)
				require.NoError(t, db.Where("task_id = ?", taskID).First(&reservation).Error)
				require.FileExists(t, qualificationArtifact(root, taskID, output))
				if scenario != "cancel_before_commit" {
					require.Equal(t, "committed", reservation.Status)
				}
				if scenario == "cancel_after_commit" {
					require.Equal(t, http.StatusOK, qualificationCancel(t, app, taskID))
				}
				gate.release()
				awaitQualification(t, gate.returned)
				require.Eventually(t, func() bool { _, err := os.Stat(output); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond)
				task, err := tasks.Registry.Get(taskID)
				require.NoError(t, err)
				require.NotNil(t, task)
				require.Equal(t, owner, task.OwnerIdentity)
				require.Equal(t, string(billing.ReservationKindDatabase), task.ReservationKind)
				require.NoError(t, db.Where("id = ?", reservation.ID).First(&reservation).Error)
				artifact := qualificationArtifact(root, taskID, output)
				if scenario != "success_duplicate_and_late_cancel" {
					require.Eventually(t, func() bool { _, err := os.Stat(artifact); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond)
				}
				switch scenario {
				case "cancel_before_commit":
					require.Equal(t, "CANCELLED", task.Status)
					require.Equal(t, "released", reservation.Status)
					qualificationUsage(t, db, owner, initial, reservation, false)
					require.NoFileExists(t, artifact)
				case "cancel_after_commit":
					require.Equal(t, "CANCELLED", task.Status)
					require.Equal(t, "committed", reservation.Status, "KNOWN INCONSISTENCY: release does not reverse a committed charge")
					qualificationUsage(t, db, owner, initial, reservation, true)
					require.NoFileExists(t, artifact)
				case "redis_completion_failure":
					require.Equal(t, "PROCESSING", task.Status)
					require.Equal(t, "committed", reservation.Status)
					require.NoFileExists(t, artifact)
					// Existing stale-task recovery releases only reserved billing. Force its
					// clock input, rather than waiting, to show it cannot repair this charge.
					task.UpdatedAt = time.Now().Add(-time.Hour).Unix()
					raw, err := json.Marshal(task)
					require.NoError(t, err)
					require.NoError(t, client.Set(context.Background(), tasks.TaskKeyPrefix+taskID, raw, tasks.TaskTTL).Err())
					stale, err := tasks.Registry.Get(taskID)
					require.NoError(t, err)
					require.Equal(t, "FAILED", stale.Status)
					require.NoError(t, db.Where("id = ?", reservation.ID).First(&reservation).Error)
					require.Equal(t, "committed", reservation.Status, "KNOWN INCONSISTENCY: stale recovery has no compensation")
					qualificationUsage(t, db, owner, initial, reservation, true)
				case "redis_completion_ack_lost":
					require.Equal(t, "COMPLETED", task.Status)
					require.Equal(t, "committed", reservation.Status)
					require.NoFileExists(t, artifact, "KNOWN INCONSISTENCY: lost write acknowledgement deletes a completed artifact")
					qualificationUsage(t, db, owner, initial, reservation, true)
				case "success_duplicate_and_late_cancel":
					require.Equal(t, "COMPLETED", task.Status)
					require.FileExists(t, artifact)
					var wg sync.WaitGroup
					for i := 0; i < 2; i++ {
						wg.Add(1)
						go func() {
							defer wg.Done()
							assertErr := billing.Default.CommitAsync(context.Background(), reservation.ID, billing.ReservationKindDatabase)
							if assertErr != nil {
								t.Errorf("duplicate commit: %v", assertErr)
							}
						}()
					}
					wg.Wait()
					accepted, err := tasks.Registry.SetWithResultURL(taskID, "COMPLETED", 100, artifact, "", owner, reservation.ID, string(billing.ReservationKindDatabase))
					require.NoError(t, err)
					require.False(t, accepted, "terminal completion retry must be rejected")
					require.Equal(t, http.StatusOK, qualificationCancel(t, app, taskID))
					after, err := tasks.Registry.Get(taskID)
					require.NoError(t, err)
					require.Equal(t, "COMPLETED", after.Status)
					qualificationUsage(t, db, owner, initial, reservation, true)
				}
				download, err := app.Test(httptest.NewRequest(http.MethodGet, "/api/v1/download/"+taskID, nil))
				require.NoError(t, err)
				defer download.Body.Close()
				if scenario == "success_duplicate_and_late_cancel" {
					require.Equal(t, http.StatusOK, download.StatusCode)
					payload, err := io.ReadAll(download.Body)
					require.NoError(t, err)
					require.True(t, bytes.HasPrefix(payload, []byte("%PDF-")))
				} else if scenario == "redis_completion_ack_lost" {
					require.Equal(t, http.StatusGone, download.StatusCode)
				} else {
					require.Equal(t, http.StatusNotFound, download.StatusCode)
				}
				t.Logf("characterization: scenario=%s allocation=%s task=%s reservation=%s plan_units=%d credit_units=%d artifact_available=%t", scenario, allocation, task.Status, reservation.Status, reservation.PlanUnits, reservation.CreditUnits, scenario == "success_duplicate_and_late_cancel")
			})
		}
	}
}

func TestR2BillingQualificationFreeModeEmptyReservation(t *testing.T) {
	t.Setenv("BILLING_MODE", "free")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("APP_ENV", "development")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	previousDB := config.DB
	config.DB = nil
	t.Cleanup(func() { config.DB = previousDB })
	client := qualificationTaskState(t)
	gate := newCompletionGate(false)
	client.AddHook(gate)
	defer gate.release()
	owner := uuid.NewString()
	output := writeSyntheticOnePagePDF(t)
	app := qualificationApp(owner, NewController(&completedR2JobService{outputPath: output}))
	taskID := qualificationSubmit(t, app, owner)
	awaitQualification(t, gate.entered)
	gate.release()
	awaitQualification(t, gate.returned)
	require.Eventually(t, func() bool { _, err := os.Stat(output); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond)
	task, err := tasks.Registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "COMPLETED", task.Status)
	require.Empty(t, task.ReservationID)
	require.NoError(t, billing.Default.CommitAsync(context.Background(), "", billing.ReservationKindDatabase))
	require.NoError(t, billing.Default.ReleaseAsync(context.Background(), "", billing.ReservationKindGuest))
}

func TestR2BillingQualificationRelatedGuestSettlement(t *testing.T) {
	for _, committedFirst := range []bool{false, true} {
		t.Run(fmt.Sprint("committed_before_cancel=", committedFirst), func(t *testing.T) {
			t.Setenv("BILLING_MODE", "normal")
			client := qualificationTaskState(t)
			store := billing.NewGuestQuotaStore(client, time.Hour)
			billing.Initialize(store)
			owner := uuid.NewString()
			taskID := uuid.NewString()
			ctx := context.Background()
			reservation, err := billing.Default.ReserveAsync(ctx, owner, owner, billing.ImageToTextPDF, 0, 1, "/related-guest-contract", taskID)
			require.NoError(t, err)
			require.Equal(t, billing.ReservationKindGuest, reservation.Kind)
			accepted, err := tasks.Registry.SetWithKeyAndBilling(taskID, "PROCESSING", 30, "", "", owner, reservation.ID, string(reservation.Kind))
			require.NoError(t, err)
			require.True(t, accepted)
			if committedFirst {
				require.NoError(t, billing.Default.CommitAsync(ctx, reservation.ID, reservation.Kind))
			}
			app := qualificationApp(owner, nil)
			require.Equal(t, http.StatusOK, qualificationCancel(t, app, taskID))
			require.NoError(t, billing.Default.CommitAsync(ctx, reservation.ID, reservation.Kind))
			require.NoError(t, billing.Default.ReleaseAsync(ctx, reservation.ID, reservation.Kind))
			task, err := tasks.Registry.Get(taskID)
			require.NoError(t, err)
			require.Equal(t, "CANCELLED", task.Status)
			state, err := client.HGetAll(ctx, "platen:guestquota:state:"+owner).Result()
			require.NoError(t, err)
			require.Equal(t, "0", state["pending_3h"])
			expected := "0"
			if committedFirst {
				expected = fmt.Sprint(billing.ImageToTextPDF.Units(0, 1))
			}
			require.Equal(t, expected, state["used_3h"])
			require.Equal(t, expected, state["used_day"])
			require.Equal(t, expected, state["used_month"])
			require.EqualValues(t, 0, client.Exists(ctx, "platen:guestquota:res:"+reservation.ID).Val())
			t.Logf("related guest service characterization (not /api/ocr/jobs guest routing): committed_first=%t cancelled_task_used_units=%s", committedFirst, expected)
		})
	}
}

func qualificationBillingDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("PLATEN_M1_BILLING_TEST_DATABASE_URL"))
	if dsn == "" {
		t.Skip("disposable loopback PostgreSQL not configured; billing race qualification was not run")
	}
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	require.Contains(t, []string{"postgres", "postgresql"}, parsed.Scheme)
	require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, parsed.Hostname())
	require.True(t, strings.HasPrefix(strings.TrimPrefix(parsed.Path, "/"), "test_"), "disposable DB name must start with test_")
	for _, key := range []string{"host", "hostaddr", "service", "servicefile"} {
		require.Empty(t, parsed.Query().Get(key), "must not override loopback DB target")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	require.NoError(t, err)
	pool, err := db.DB()
	require.NoError(t, err)
	pool.SetMaxOpenConns(4)
	pool.SetMaxIdleConns(2)
	previous := config.DB
	config.DB = db
	t.Cleanup(func() { config.DB = previous; _ = pool.Close() })
	require.NoError(t, db.AutoMigrate(&config.User{}, &config.Subscription{}, &config.BillingReservation{}, &config.UsageLog{}))
	return db
}
func qualificationTaskState(t *testing.T) *redis.Client {
	t.Helper()
	var client *redis.Client
	if target := strings.TrimSpace(os.Getenv("PLATEN_M1_BILLING_TEST_REDIS_URL")); target != "" {
		parsed, err := url.Parse(target)
		require.NoError(t, err)
		require.Equal(t, "redis", parsed.Scheme)
		require.Contains(t, []string{"127.0.0.1", "localhost", "::1"}, parsed.Hostname())
		require.NotEmpty(t, parsed.Port())
		require.NotEqual(t, "6379", parsed.Port(), "use a dedicated nondefault task-owned Redis listener")
		options, err := redis.ParseURL(target)
		require.NoError(t, err)
		client = redis.NewClient(options)
		previousRedis := config.Redis
		config.Redis = client
		t.Cleanup(func() { config.Redis = previousRedis; _ = client.Close() })
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, client.Ping(ctx).Err())
	} else {
		client = setupTestRedis(t)
	}
	oldRegistry := tasks.Registry
	oldQuota := billing.GuestQuota
	oldStale, oldStaleKind := tasks.StaleTaskBillingHandler, tasks.StaleTaskBillingHandlerWithKind
	oldCommit, oldCommitKind := tasks.CommitTaskBillingHandler, tasks.CommitTaskBillingHandlerWithKind
	oldCancel, oldCancelKind := tasks.CancelTaskBillingHandler, tasks.CancelTaskBillingHandlerWithKind
	tasks.Registry = &tasks.TaskRegistry{}
	billing.Initialize(nil)
	t.Cleanup(func() {
		tasks.Registry = oldRegistry
		billing.GuestQuota = oldQuota
		tasks.StaleTaskBillingHandler = oldStale
		tasks.StaleTaskBillingHandlerWithKind = oldStaleKind
		tasks.CommitTaskBillingHandler = oldCommit
		tasks.CommitTaskBillingHandlerWithKind = oldCommitKind
		tasks.CancelTaskBillingHandler = oldCancel
		tasks.CancelTaskBillingHandlerWithKind = oldCancelKind
	})
	return client
}
func qualificationAccount(t *testing.T, db *gorm.DB, credits bool) (string, config.Subscription) {
	t.Helper()
	owner := uuid.NewString()
	now := time.Now()
	require.NoError(t, db.Create(&config.User{ID: owner, Email: owner + "@example.test", CreatedAt: now, UpdatedAt: now}).Error)
	initial := config.Subscription{ID: uuid.NewString(), UserID: owner, Tier: "free", Status: "active", CustomCredits: 50, PaddleCustomerID: uuid.NewString(), PaddleSubscriptionID: uuid.NewString(), Window3HResetAt: now.Add(time.Hour), WindowDailyResetAt: now.Add(24 * time.Hour), WindowMonthlyResetAt: now.AddDate(0, 1, 0), CurrentPeriodEnd: now.AddDate(1, 0, 0), CreatedAt: now, UpdatedAt: now}
	if credits {
		limits := billing.GetTierLimits("free")
		initial.UsedUnits3h = limits.Units3H
		initial.UsedUnitsDaily = limits.UnitsDay
		initial.UsedUnitsMonthly = limits.UnitsMonth
	}
	require.NoError(t, db.Create(&initial).Error)
	t.Cleanup(func() {
		db.Where("user_id = ?", owner).Delete(&config.UsageLog{})
		db.Where("user_id = ?", owner).Delete(&config.BillingReservation{})
		db.Where("user_id = ?", owner).Delete(&config.Subscription{})
		db.Unscoped().Where("id = ?", owner).Delete(&config.User{})
	})
	return owner, initial
}
func qualificationApp(owner string, controller *Controller) *fiber.App {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals("user_id", owner)
		c.Locals(identity.LocalIdentityIDKey, owner)
		return c.Next()
	})
	if controller != nil {
		app.Post("/api/ocr/jobs", controller.HandleAsyncImageToTextPDFR2)
	}
	tasks.RegisterRoutes(app.Group("/api"))
	return app
}
func qualificationSubmit(t *testing.T, app *fiber.App, owner string) string {
	t.Helper()
	raw, err := json.Marshal(OCRR2JobRequest{Tool: "image_to_text_pdf", Lang: "eng", Files: []R2ImageRef{{Key: storage.NewOwnedKey(owner, "image_to_text_pdf", ".png"), Name: "synthetic.png", Type: "image/png", Size: 10}}})
	require.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/api/ocr/jobs", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	response, err := app.Test(req, 5000)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusAccepted, response.StatusCode)
	var accepted struct {
		TaskID string `json:"taskId"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&accepted))
	require.NotEmpty(t, accepted.TaskID)
	return accepted.TaskID
}
func qualificationCancel(t *testing.T, app *fiber.App, taskID string) int {
	t.Helper()
	response, err := app.Test(httptest.NewRequest(http.MethodDelete, "/api/v1/tasks/"+taskID, nil), 5000)
	require.NoError(t, err)
	defer response.Body.Close()
	return response.StatusCode
}
func qualificationArtifact(root, id, output string) string {
	return filepath.Join(root, "outputs", "tasks", id, filepath.Base(output))
}
func qualificationUsage(t *testing.T, db *gorm.DB, owner string, initial config.Subscription, res config.BillingReservation, charged bool) {
	t.Helper()
	var actual config.Subscription
	require.NoError(t, db.Where("user_id = ?", owner).First(&actual).Error)
	plan, credit, count := 0, 0, int64(0)
	if charged {
		plan = res.PlanUnits
		credit = res.CreditUnits
		count = 1
	}
	require.Equal(t, initial.UsedUnits3h+plan, actual.UsedUnits3h)
	require.Equal(t, initial.UsedUnitsDaily+plan, actual.UsedUnitsDaily)
	require.Equal(t, initial.UsedUnitsMonthly+plan, actual.UsedUnitsMonthly)
	require.Equal(t, initial.CustomCredits-credit, actual.CustomCredits)
	var logs int64
	require.NoError(t, db.Model(&config.UsageLog{}).Where("user_id = ?", owner).Count(&logs).Error)
	require.Equal(t, count, logs)
}
func awaitQualification(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("controlled synchronization point was not reached")
	}
}

type completionGate struct {
	entered, proceed, returned chan struct{}
	once                       sync.Once
	seen                       atomic.Bool
	fail                       bool
	failAfter                  bool
}

func newCompletionGate(fail bool) *completionGate {
	return &completionGate{entered: make(chan struct{}), proceed: make(chan struct{}), returned: make(chan struct{}), fail: fail}
}
func (g *completionGate) release()                                    { g.once.Do(func() { close(g.proceed) }) }
func (g *completionGate) DialHook(next redis.DialHook) redis.DialHook { return next }
func (g *completionGate) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
func (g *completionGate) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		args := cmd.Args()
		var candidate tasks.TaskStatus
		if cmd.Name() != "eval" || len(args) < 5 || json.Unmarshal([]byte(fmt.Sprint(args[4])), &candidate) != nil || candidate.Status != "COMPLETED" || !g.seen.CompareAndSwap(false, true) {
			return next(ctx, cmd)
		}
		close(g.entered)
		defer close(g.returned)
		select {
		case <-g.proceed:
		case <-ctx.Done():
			return ctx.Err()
		}
		if g.fail {
			return errors.New("synthetic Redis completion write failure")
		}
		err := next(ctx, cmd)
		if err == nil && g.failAfter {
			return errors.New("synthetic lost Redis completion acknowledgement")
		}
		return err
	}
}

// This qualifies simultaneous store-level settlement calls, not two supported
// worker executions: /api/ocr/jobs creates a fresh task for every accepted POST.
func TestR2BillingQualificationCompetingSettlement(t *testing.T) {
	for _, allocation := range []string{"plan", "credits"} {
		t.Run(allocation, func(t *testing.T) {
			db := qualificationBillingDB(t)
			t.Setenv("BILLING_MODE", "normal")
			qualificationTaskState(t)
			owner, initial := qualificationAccount(t, db, allocation == "credits")
			taskID := uuid.NewString()
			reservation, err := billing.Default.ReserveWithTaskID(owner, billing.ImageToTextPDF, 0, 1, "/api/ocr/jobs", taskID)
			require.NoError(t, err)
			output := writeSyntheticOnePagePDF(t)
			accepted, err := tasks.Registry.SetWithResultURL(taskID, "PROCESSING", 30, "", "", owner, reservation.ID, string(billing.ReservationKindDatabase))
			require.NoError(t, err)
			require.True(t, accepted)
			start := make(chan struct{})
			var wg sync.WaitGroup
			var completed atomic.Int32
			for i := 0; i < 2; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if err := billing.Default.CommitAsync(context.Background(), reservation.ID, billing.ReservationKindDatabase); err != nil {
						t.Errorf("competing billing commit: %v", err)
						return
					}
					accepted, err := tasks.Registry.SetWithResultURL(taskID, "COMPLETED", 100, output, "", owner, reservation.ID, string(billing.ReservationKindDatabase))
					if err != nil {
						t.Errorf("competing terminal write: %v", err)
						return
					}
					if accepted {
						completed.Add(1)
					}
				}()
			}
			close(start)
			wg.Wait()
			require.EqualValues(t, 1, completed.Load())
			qualificationUsage(t, db, owner, initial, *reservation, true)
			task, err := tasks.Registry.Get(taskID)
			require.NoError(t, err)
			require.Equal(t, "COMPLETED", task.Status)
			require.FileExists(t, output)
		})
	}
}

func TestR2BillingQualificationDirectGuestContext(t *testing.T) {
	db := qualificationBillingDB(t)
	t.Setenv("BILLING_MODE", "normal")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("APP_ENV", "development")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	qualificationTaskState(t)
	// identity.Resolve can populate user_id for guests as well. The frontend's
	// requireAuth limits normal UI entry; this is a direct API context test.
	owner := uuid.NewString()
	output := writeSyntheticOnePagePDF(t)
	controller := NewController(&completedR2JobService{outputPath: output})
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error {
		c.Locals(identity.LocalUserIDKey, owner)
		c.Locals(identity.LocalIdentityIDKey, owner)
		c.Locals(identity.LocalIdentityType, string(identity.TypeGuest))
		c.Locals(identity.LocalIdentityKey, identity.Identity{ID: owner, QuotaID: owner, Type: identity.TypeGuest})
		return c.Next()
	})
	app.Post("/api/ocr/jobs", controller.HandleAsyncImageToTextPDFR2)
	tasks.RegisterRoutes(app.Group("/api"))
	taskID := qualificationSubmit(t, app, owner)
	require.Eventually(t, func() bool { _, err := os.Stat(output); return os.IsNotExist(err) }, 5*time.Second, 10*time.Millisecond)
	task, err := tasks.Registry.Get(taskID)
	require.NoError(t, err)
	require.Equal(t, "FAILED", task.Status)
	require.NotEmpty(t, task.ReservationID)
	require.Equal(t, string(billing.ReservationKindDatabase), task.ReservationKind)
	var reservations, logs int64
	require.NoError(t, db.Model(&config.BillingReservation{}).Where("task_id = ?", taskID).Count(&reservations).Error)
	require.NoError(t, db.Model(&config.UsageLog{}).Where("user_id = ?", owner).Count(&logs).Error)
	require.Zero(t, reservations)
	require.Zero(t, logs)
	t.Log("direct guest /api/ocr/jobs context: accepted, unpersisted database reservation, billing finalization fails safely; no guest-quota allocation")
}
