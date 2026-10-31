package conversion_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/billing"
	"pdfnest-backend/internal/conversion"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/limiter"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/tasks"
	"pdfnest-backend/internal/worker"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

// Opt-in actual public HTTP/identity/DB/Worker/actor/storage qualification.
// scripts/test-public-markdown-e2e.py owns every service and synthetic credential.
func TestPublicMarkdownQueuedLifecycle(t *testing.T) {
	if os.Getenv("PLATEN_PUBLIC_MARKDOWN_E2E") != "1" {
		t.Skip("run scripts/test-public-markdown-e2e.py for private fixture services")
	}
	root := os.Getenv("PLATEN_PUBLIC_MARKDOWN_ROOT")
	require.True(t, strings.HasPrefix(root, "/tmp/platen-public-markdown-"))
	dsn := os.Getenv("DATABASE_URL")
	require.True(t, strings.HasPrefix(dsn, "host="+root+" "))
	require.Equal(t, filepath.Join(root, "objects"), os.Getenv("LOCAL_STORAGE_DIR"))
	workerURL, err := url.Parse(worker.GetWorkerURL())
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", workerURL.Hostname())
	require.Equal(t, "http", workerURL.Scheme)
	require.NotEmpty(t, workerURL.Port())
	options, err := redis.ParseURL(os.Getenv("REDIS_URL"))
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(options.Addr, "127.0.0.1:"))
	client := redis.NewClient(options)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(t.Context()).Err())
	db := openGIM11DurabilityDB(t, dsn)
	oldRedis, oldGuest, oldGovernor, oldIdentity := config.Redis, billing.GuestQuota, limiter.Default, identity.DefaultStore
	oldTimeout := worker.Client.Timeout
	t.Cleanup(func() {
		config.Redis, billing.GuestQuota, limiter.Default, identity.DefaultStore = oldRedis, oldGuest, oldGovernor, oldIdentity
		worker.Client.Timeout = oldTimeout
		worker.SetDurabilityStore(nil)
	})
	config.Redis = client
	billing.Initialize(billing.NewGuestQuotaStore(client, 24*time.Hour))
	limiter.Default = limiter.NewGovernor()
	store := identity.NewStore(client, time.Hour)
	require.NoError(t, worker.ConfigureDurability(db))
	worker.Client.Timeout = 5 * time.Second
	app := fiber.New(fiber.Config{DisableStartupMessage: true})
	app.Use(identity.Resolve(store))
	conversion.RegisterRoutes(app.Group("/api"), conversion.NewController(conversion.NewService()))
	tasks.RegisterRoutes(app.Group("/api"))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	serverDone := make(chan error, 1)
	go func() { serverDone <- app.Listener(listener) }()
	t.Cleanup(func() { require.NoError(t, app.Shutdown()); require.NoError(t, <-serverDone) })
	base := "http://" + listener.Addr().String()
	httpClient := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil}}
	t.Cleanup(func() { httpClient.CloseIdleConnections() })
	pdf, err := os.ReadFile(filepath.Join(root, "synthetic.pdf"))
	require.NoError(t, err)
	// Use the actual strict middleware: tampered signed bytes and unsigned
	// reads must fail before creating a job or broker delivery.
	tampered, err := http.NewRequestWithContext(t.Context(), http.MethodPost, worker.GetWorkerURL()+"/api/v1/jobs/submit", strings.NewReader(`{"actor_name":"pdf_to_markdown_job","args":[]}`))
	require.NoError(t, err)
	require.NoError(t, worker.SignRequest(tampered))
	_ = tampered.Body.Close()
	tampered.Body = io.NopCloser(strings.NewReader(`{"actor_name":"pdf_to_markdown_job","args":[1]}`))
	tampered.ContentLength = int64(len(`{"actor_name":"pdf_to_markdown_job","args":[1]}`))
	tampered.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(tampered)
	require.NoError(t, err)
	_ = response.Body.Close()
	require.Equal(t, http.StatusUnauthorized, response.StatusCode)
	unsigned, err := httpClient.Get(worker.GetWorkerURL() + "/api/v1/jobs/" + uuid.NewString())
	require.NoError(t, err)
	_ = unsigned.Body.Close()
	require.Equal(t, http.StatusUnauthorized, unsigned.StatusCode)
	require.Equal(t, int64(0), client.ZCard(t.Context(), "pdfnest:jobs:index").Val())
	require.Equal(t, int64(0), client.LLen(t.Context(), "dramatiq:conversion").Val())

	for index, scenario := range []struct{ mode, kind string }{{"normal", "guest_cancelled"}, {"free", "guest"}, {"free", "account"}, {"normal", "guest"}, {"normal", "account"}} {
		if !t.Run(scenario.mode+"_"+scenario.kind, func(t *testing.T) {
			t.Setenv("BILLING_MODE", scenario.mode)
			owner, credential := uuid.NewString(), ""
			if scenario.kind == "account" {
				owner = createGIM11Account(t, db)
				require.NoError(t, db.Model(&config.User{}).Where("id = ?", owner).Updates(map[string]any{"status": "active", "email_verified": true}).Error)
				require.NoError(t, db.Model(&config.Subscription{}).Where("user_id = ?", owner).Update("tier", "pro").Error)
				credential, err = jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"user_id": owner, "role": "user", "session_version": 0, "exp": time.Now().Add(time.Hour).Unix()}).SignedString([]byte(os.Getenv("JWT_SECRET")))
				require.NoError(t, err)
			} else {
				require.NoError(t, store.Save(t.Context(), &identity.GuestRecord{ID: owner, QuotaID: owner, CreatedAt: time.Now(), Trust: 1}))
			}
			request := func(method, path string, body io.Reader, contentType string, authorized bool) (int, []byte) {
				req, requestErr := http.NewRequestWithContext(t.Context(), method, base+path, body)
				require.NoError(t, requestErr)
				if contentType != "" {
					req.Header.Set("Content-Type", contentType)
				}
				if authorized {
					if credential != "" {
						req.Header.Set("Authorization", "Bearer "+credential)
					} else {
						req.AddCookie(&http.Cookie{Name: identity.CookieGuestID, Value: owner})
					}
				}
				response, requestErr := httpClient.Do(req)
				require.NoError(t, requestErr)
				defer response.Body.Close()
				data, readErr := io.ReadAll(response.Body)
				require.NoError(t, readErr)
				return response.StatusCode, data
			}
			var form bytes.Buffer
			writer := multipart.NewWriter(&form)
			part, err := writer.CreateFormFile("file", "synthetic.pdf")
			require.NoError(t, err)
			_, err = part.Write(pdf)
			require.NoError(t, err)
			require.NoError(t, writer.Close())
			route := "/api/conversion/pdf-to-markdown"
			if index%2 == 1 {
				route += "-async"
			}
			code, data := request(http.MethodPost, route, &form, writer.FormDataContentType(), true)
			require.Equal(t, http.StatusAccepted, code, string(data))
			var accepted struct {
				TaskID        string `json:"task_id"`
				DownloadToken string `json:"downloadToken"`
			}
			require.NoError(t, json.Unmarshal(data, &accepted))
			require.NotEmpty(t, accepted.TaskID)
			var completion models.TaskCompletion
			require.NoError(t, db.First(&completion, "id = ?", accepted.TaskID).Error)
			require.Equal(t, owner, completion.OwnerIdentity)
			if scenario.mode == "free" {
				require.Empty(t, completion.ReservationID)
			} else {
				require.NotEmpty(t, completion.ReservationID)
			}
			if scenario.kind == "guest_cancelled" {
				// The runner has not started the actor yet. Exercise the durable
				// reconciler's restart-safe cancellation intent before any native work.
				code, _ = request(http.MethodDelete, "/api/v1/tasks/"+accepted.TaskID, nil, "", false)
				require.Equal(t, http.StatusForbidden, code)
				for range 2 {
					code, _ = request(http.MethodDelete, "/api/v1/tasks/"+accepted.TaskID, nil, "", true)
					require.Equal(t, http.StatusOK, code)
				}
				require.NoError(t, worker.RunDurabilityOnce(t.Context(), time.Now()))
				cancelReq, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, worker.GetWorkerURL()+"/api/v1/jobs/"+accepted.TaskID, nil)
				cancelResponse, err := worker.Do(cancelReq)
				require.NoError(t, err)
				var cancelledStatus struct {
					Status string `json:"status"`
				}
				require.NoError(t, json.NewDecoder(cancelResponse.Body).Decode(&cancelledStatus))
				_ = cancelResponse.Body.Close()
				require.Equal(t, "cancel_requested", cancelledStatus.Status, "durable public cancellation must reach the queued Worker job before actor startup")
				require.NoError(t, os.WriteFile(filepath.Join(root, "start-actor"), []byte(accepted.TaskID), 0600))
				require.Eventually(t, func() bool {
					req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, worker.GetWorkerURL()+"/api/v1/jobs/"+accepted.TaskID, nil)
					response, pollErr := worker.Do(req)
					if pollErr != nil {
						return false
					}
					defer response.Body.Close()
					var status struct {
						Status string `json:"status"`
					}
					return json.NewDecoder(response.Body).Decode(&status) == nil && status.Status == "cancelled"
				}, 60*time.Second, 100*time.Millisecond)
				require.NoError(t, tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()))
				require.NoError(t, db.First(&completion, "id = ?", accepted.TaskID).Error)
				require.Equal(t, "cancelled", completion.State)
				require.Empty(t, completion.ResultKey)
				code, _ = request(http.MethodGet, "/api/v1/download/"+accepted.TaskID, nil, "", true)
				require.Equal(t, http.StatusNotFound, code)
				state, err := client.HGetAll(t.Context(), "platen:guestquota:state:"+owner).Result()
				require.NoError(t, err)
				require.Equal(t, "0", state["pending_3h"])
				require.Equal(t, "0", state["used_3h"])
				evidence, _ := json.Marshal(map[string]any{"mode": "normal", "identity": "guest_cancelled", "task_id": completion.ID, "committed_units": 0, "task_state": "cancelled", "worker_state": "cancelled", "authorized_download": 404, "native_starts": 0, "cancellation_scope": "queued_before_actor"})
				t.Logf("PUBLIC_MARKDOWN_E2E %s", evidence)
				return
			}
			var lastStatus string
			defer func() {
				if t.Failed() {
					t.Logf("last Worker status: %s", lastStatus)
				}
			}()
			require.Eventually(t, func() bool {
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, worker.GetWorkerURL()+"/api/v1/jobs/"+accepted.TaskID, nil)
				response, pollErr := worker.Do(req)
				if pollErr != nil {
					lastStatus = pollErr.Error()
					return false
				}
				statusBytes, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				lastStatus = string(statusBytes)
				if readErr != nil || response.StatusCode != http.StatusOK {
					return false
				}
				if err := tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()); err != nil {
					lastStatus += " sweep=" + err.Error()
					return false
				}
				return db.First(&completion, "id = ?", accepted.TaskID).Error == nil && completion.State == "completed"
			}, 60*time.Second, 100*time.Millisecond)
			require.NotEmpty(t, completion.ArtifactSHA256)
			require.Greater(t, completion.ArtifactBytes, int64(0))
			code, _ = request(http.MethodGet, "/api/v1/tasks/"+accepted.TaskID, nil, "", false)
			require.Equal(t, http.StatusForbidden, code)
			code, _ = request(http.MethodGet, "/api/v1/download/"+accepted.TaskID, nil, "", false)
			require.Equal(t, http.StatusForbidden, code)
			var downloaded []byte
			for range 2 {
				code, downloaded = request(http.MethodGet, "/api/v1/download/"+accepted.TaskID, nil, "", true)
				require.Equal(t, http.StatusOK, code, string(downloaded))
				require.Contains(t, string(downloaded), "PLATEN PUBLIC ALPHA")
				require.Contains(t, string(downloaded), "PLATEN PUBLIC BETA")
			}
			digest := sha256.Sum256(downloaded)
			require.Equal(t, completion.ArtifactSHA256, hex.EncodeToString(digest[:]))
			require.Equal(t, completion.ArtifactBytes, int64(len(downloaded)))
			code, tokenData := request(http.MethodGet, "/api/v1/download/"+accepted.TaskID+"?token="+url.QueryEscape(accepted.DownloadToken), nil, "", false)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, downloaded, tokenData)
			// Erase only this fixture's metadata/projection to model Redis expiry.
			// The retained SQL ownership and canonical object must serve the result.
			require.NoError(t, client.Del(t.Context(), "pdfnest:jobs:"+accepted.TaskID, tasks.TaskKeyPrefix+accepted.TaskID).Err())
			code, statusData := request(http.MethodGet, "/api/v1/tasks/"+accepted.TaskID, nil, "", true)
			require.Equal(t, http.StatusOK, code)
			require.Contains(t, string(statusData), `"status":"COMPLETED"`)
			req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, worker.GetWorkerURL()+"/api/v1/jobs/"+accepted.TaskID, nil)
			response, err := worker.Do(req)
			require.NoError(t, err)
			retainedStatus, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			_ = response.Body.Close()
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Contains(t, string(retainedStatus), `"succeeded"`)
			code, recovered := request(http.MethodGet, "/api/v1/download/"+accepted.TaskID, nil, "", true)
			require.Equal(t, http.StatusOK, code)
			require.Equal(t, downloaded, recovered)
			require.NoError(t, tasks.Registry.ReconcileCompletion(t.Context(), accepted.TaskID))
			var units int
			if scenario.kind == "account" {
				var reservations []config.BillingReservation
				require.NoError(t, db.Where("user_id = ?", owner).Find(&reservations).Error)
				var sub config.Subscription
				require.NoError(t, db.First(&sub, "user_id = ?", owner).Error)
				if scenario.mode == "free" {
					require.Empty(t, reservations)
					require.Zero(t, sub.UsedUnitsMonthly)
				} else {
					require.Len(t, reservations, 1)
					require.Equal(t, "committed", reservations[0].Status)
					require.Equal(t, 4, sub.UsedUnits3h)
					require.Equal(t, 4, sub.UsedUnitsDaily)
					require.Equal(t, 4, sub.UsedUnitsMonthly)
					units = sub.UsedUnitsMonthly
				}
			} else {
				state, err := client.HGetAll(t.Context(), "platen:guestquota:state:"+owner).Result()
				require.NoError(t, err)
				if scenario.mode == "free" {
					require.Empty(t, state)
				} else {
					require.Equal(t, "0", state["pending_3h"])
					require.Equal(t, "4", state["used_3h"])
					require.Equal(t, "committed", client.Get(t.Context(), "platen:guestquota:outcome:"+completion.ReservationID).Val())
					units = 4
				}
			}
			var owned models.WorkerResultOwnership
			require.NoError(t, db.First(&owned, "worker_job_id = ?", accepted.TaskID).Error)
			require.Equal(t, "terminal", owned.State)
			require.True(t, strings.HasPrefix(owned.ResultKey, "jobs/markdown/output/"))
			evidence, _ := json.Marshal(map[string]any{"mode": scenario.mode, "identity": scenario.kind, "task_id": completion.ID, "owner": owner, "worker_result": owned.ResultKey, "artifact": completion.ResultKey, "sha256": completion.ArtifactSHA256, "bytes": completion.ArtifactBytes, "committed_units": units, "unauthorized_status": 403, "unauthorized_download": 403, "authorized_downloads": 3, "capability_download": 200, "redis_metadata_loss_recovered": true})
			t.Logf("PUBLIC_MARKDOWN_E2E %s", evidence)
		}) {
			t.FailNow()
		}
	}
	qualifyLocalNativeClients(t, root)
	require.Equal(t, int64(0), client.ZCard(context.Background(), "pdfnest:limiter:active").Val())
}
