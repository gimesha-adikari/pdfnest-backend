package billing

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/identity"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func TestDurableCompletedCorruptArtifactIsQuarantinedAndRecoverable(t *testing.T) {
	db, _, _, row, source := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, source, "text/plain"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
	corrupt := bytes.Repeat([]byte("X"), len(original))
	if err := os.WriteFile(artifactPath, corrupt, 0600); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Get("/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, row.OwnerIdentity)
		return tasks.HandleTaskDownload(c)
	})
	request := func() ([]byte, int) {
		response, err := app.Test(httptest.NewRequest("GET", "/"+row.ID, nil))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, err := io.ReadAll(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		return body, response.StatusCode
	}

	body, status := request()
	if status == fiber.StatusOK && bytes.Equal(body, corrupt) {
		t.Fatal("corrupted post-success artifact was served")
	}
	if status != fiber.StatusGone {
		t.Fatalf("corrupt artifact response status=%d body=%q; want 410", status, body)
	}

	quarantined := completionRow(t, db, row.ID)
	if quarantined.State != "artifact_unavailable" {
		t.Fatalf("state=%q; want artifact_unavailable", quarantined.State)
	}
	if quarantined.ResultKey != key || quarantined.ArtifactSHA256 == "" || quarantined.ArtifactBytes != int64(len(original)) {
		t.Fatalf("quarantine discarded artifact identity: %+v", quarantined)
	}
	assertReserved(t, db, row.ReservationID, "committed")
	if !storage.ObjectExists(t.Context(), key) {
		t.Fatal("quarantine removed the object instead of retaining it for recovery")
	}
	task, err := tasks.Registry.Get(row.ID)
	if err != nil || task == nil || task.Status == "COMPLETED" {
		t.Fatalf("stale Redis projection still reported success: task=%+v err=%v", task, err)
	}

	if err := os.WriteFile(artifactPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()); err != nil {
		t.Fatalf("reconcile restored artifact: %v", err)
	}
	if recovered := completionRow(t, db, row.ID); recovered.State != "completed" {
		t.Fatalf("state after verified recovery=%q; want completed", recovered.State)
	}
	assertReserved(t, db, row.ReservationID, "committed")
	body, status = request()
	if status != fiber.StatusOK || !bytes.Equal(body, original) {
		t.Fatalf("recovered download status=%d body=%q", status, body)
	}

	var reservation config.BillingReservation
	if err := db.First(&reservation, "id = ?", row.ReservationID).Error; err != nil {
		t.Fatal(err)
	}
	var charges int64
	if err := db.Model(&config.UsageLog{}).Where("user_id = ?", reservation.UserID).Count(&charges).Error; err != nil && err != gorm.ErrRecordNotFound {
		t.Fatal(err)
	}
	if charges != 1 {
		t.Fatalf("usage charges=%d; want one original settlement", charges)
	}
}

func TestDurableCompletedMissingArtifactIsQuarantined(t *testing.T) {
	db, _, _, row, source := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, source, "text/plain"); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
	if err := os.Remove(artifactPath); err != nil {
		t.Fatal(err)
	}

	app := fiber.New()
	app.Get("/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, row.OwnerIdentity)
		return tasks.HandleTaskDownload(c)
	})
	response, err := app.Test(httptest.NewRequest("GET", "/"+row.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusGone {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("missing artifact response status=%d body=%q; want 410", response.StatusCode, body)
	}
	if current := completionRow(t, db, row.ID); current.State != "artifact_unavailable" {
		t.Fatalf("missing artifact left state=%q; want artifact_unavailable", current.State)
	}
	assertReserved(t, db, row.ReservationID, "committed")
	task, err := tasks.Registry.Get(row.ID)
	if err != nil || task == nil || task.Status == "COMPLETED" {
		t.Fatalf("missing artifact still reported success: task=%+v err=%v", task, err)
	}
}

func TestDurableCompletedArtifactTransientStorageFailureIsNotQuarantined(t *testing.T) {
	db, _, _, row, source := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, source, "text/plain"); err != nil {
		t.Fatal(err)
	}

	// A broken local storage root is an infrastructure failure, not proof that
	// the durable artifact was lost or corrupted.
	blockedRoot := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blockedRoot, []byte("block mkdir"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LOCAL_STORAGE_DIR", blockedRoot)
	app := fiber.New()
	app.Get("/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, row.OwnerIdentity)
		return tasks.HandleTaskDownload(c)
	})
	response, err := app.Test(httptest.NewRequest("GET", "/"+row.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != fiber.StatusServiceUnavailable {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("transient storage response status=%d body=%q; want 503", response.StatusCode, body)
	}
	if current := completionRow(t, db, row.ID); current.State != "completed" {
		t.Fatalf("transient storage error changed durable state to %q; want completed", current.State)
	}
}

func TestDurableUnavailableArtifactUsesOriginalRetentionDeadline(t *testing.T) {
	db, _, _, row, source := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, source, "text/plain"); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
	if err := os.WriteFile(artifactPath, bytes.Repeat([]byte("X"), len("durable result\n")), 0600); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Registry.ReconcileCompletion(t.Context(), row.ID); !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatalf("corrupt completed artifact error=%v; want settlement pending", err)
	}
	if current := completionRow(t, db, row.ID); current.State != "artifact_unavailable" {
		t.Fatalf("state after quarantine=%q; want artifact_unavailable", current.State)
	}

	if err := tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()); err != nil {
		t.Fatalf("retry recovery before retention deadline: %v", err)
	}
	if !storage.ObjectExists(t.Context(), key) {
		t.Fatal("quarantined artifact was removed before its original retention deadline")
	}

	now := time.Now().Add(2 * time.Second)
	if err := db.Model(&models.TaskCompletion{}).Where("id = ?", row.ID).Update("expires_at", now.Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if current := completionRow(t, db, row.ID); !current.ExpiresAt.Before(now) {
		t.Fatalf("test setup expiry=%s is not before sweep now=%s", current.ExpiresAt, now)
	}
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), now); err != nil {
		t.Fatalf("retire expired quarantined artifact: %v", err)
	}
	if current := completionRow(t, db, row.ID); current.State != "deleted" {
		t.Fatalf("expired quarantine state=%q; want deleted", current.State)
	}
	if storage.ObjectExists(t.Context(), key) {
		t.Fatal("expired quarantined artifact remained in local storage")
	}
	assertReserved(t, db, row.ReservationID, "committed")
}

func TestDurableExpiredArtifactWithInvalidKeyIsHeldWithoutDeletion(t *testing.T) {
	for _, invalidKey := range []string{"", "outputs/tasks/placeholder/../unrelated.txt"} {
		name := "traversal"
		if invalidKey == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			db, _, _, row, source := completionFixture(t, false)
			validKey := "outputs/tasks/" + row.ID + "/result.txt"
			if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, validKey, source, "text/plain"); err != nil {
				t.Fatal(err)
			}

			key := invalidKey
			var unrelatedPath string
			if key == "outputs/tasks/placeholder/../unrelated.txt" {
				key = "outputs/tasks/" + row.ID + "/../unrelated.txt"
				unrelatedPath = filepath.Join(storage.GetLocalStorageDir(), "outputs", "tasks", "unrelated.txt")
				if err := os.MkdirAll(filepath.Dir(unrelatedPath), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(unrelatedPath, []byte("synthetic unrelated object"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			url := "r2://" + key
			expiredAt := time.Now().Add(-time.Minute)
			if err := db.Model(&models.TaskCompletion{}).Where("id = ?", row.ID).Updates(map[string]any{
				"state": "completed", "result_key": key, "result_url": url, "expires_at": expiredAt,
			}).Error; err != nil {
				t.Fatal(err)
			}

			now := time.Now()
			if err := tasks.Registry.SweepDurableCompletions(t.Context(), now); err != nil {
				t.Fatalf("hold expired invalid artifact: %v", err)
			}
			current := completionRow(t, db, row.ID)
			if current.State != "artifact_recovery_hold" || current.ResultKey != key {
				t.Fatalf("row=%+v; want retained recovery hold for original key", current)
			}
			status, err := tasks.Registry.Get(row.ID)
			if err != nil || status == nil {
				t.Fatalf("task projection status=%+v err=%v", status, err)
			}
			if status.Status != "FAILED" || status.ResultKey != "" || status.ResultURL != "" {
				t.Fatalf("public projection=%+v; invalid storage ownership must not be downloadable", status)
			}
			if unrelatedPath != "" {
				if data, err := os.ReadFile(unrelatedPath); err != nil || string(data) != "synthetic unrelated object" {
					t.Fatalf("unrelated object changed: data=%q err=%v", data, err)
				}
			}
			var tombstones int64
			if err := db.Table("studio_storage_object_retirements").Where("object_key = ?", key).Count(&tombstones).Error; err != nil {
				t.Fatal(err)
			}
			if tombstones != 0 {
				t.Fatalf("invalid key created %d retirement tombstones", tombstones)
			}
			assertReserved(t, db, row.ReservationID, "committed")
		})
	}
}

func TestDurableUnavailableArtifactRecoversAfterProcessRestart(t *testing.T) {
	db, _, srv, row, source := completionFixture(t, false)
	restartRedisAddr := srv.Addr()
	if configured := os.Getenv("PLATEN_M1_TEST_REDIS_ADDR"); configured != "" {
		client := redis.NewClient(&redis.Options{Addr: configured})
		if err := client.Ping(t.Context()).Err(); err != nil {
			_ = client.Close()
			t.Fatalf("disposable Redis ping: %v", err)
		}
		if err := client.FlushDB(t.Context()).Err(); err != nil {
			_ = client.Close()
			t.Fatalf("clear disposable Redis test database: %v", err)
		}
		oldRedis := config.Redis
		config.Redis = client
		t.Cleanup(func() {
			config.Redis = oldRedis
			_ = client.Close()
		})
		restartRedisAddr = configured
	}
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, source, "text/plain"); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key))
	if err := os.WriteFile(artifactPath, bytes.Repeat([]byte("X"), len(original)), 0600); err != nil {
		t.Fatal(err)
	}
	app := fiber.New()
	app.Get("/:id", func(c *fiber.Ctx) error {
		c.Locals(identity.LocalIdentityIDKey, row.OwnerIdentity)
		return tasks.HandleTaskDownload(c)
	})
	response, err := app.Test(httptest.NewRequest("GET", "/"+row.ID, nil))
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != fiber.StatusGone {
		t.Fatalf("corrupt artifact response status=%d; want 410", response.StatusCode)
	}
	if current := completionRow(t, db, row.ID); current.State != "artifact_unavailable" {
		t.Fatalf("state before process restart=%q; want artifact_unavailable", current.State)
	}
	if err := os.WriteFile(artifactPath, original, 0600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDurableArtifactProcessRestartHelper$")
	cmd.Env = append(os.Environ(),
		"PLATEN_M1_RESTART_HELPER=1",
		"PLATEN_M1_RESTART_TASK_ID="+row.ID,
		"PLATEN_M1_RESTART_REDIS_ADDR="+restartRedisAddr,
		"LOCAL_STORAGE_DIR="+storage.GetLocalStorageDir(),
		"STORAGE_MODE=local",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("post-restart reconciliation failed: %v\n%s", err, output)
	}
	if current := completionRow(t, db, row.ID); current.State != "completed" {
		t.Fatalf("state after process restart=%q; want completed", current.State)
	}
}

func TestDurableArtifactProcessRestartHelper(t *testing.T) {
	if os.Getenv("PLATEN_M1_RESTART_HELPER") != "1" {
		t.Skip("subprocess-only restart helper")
	}
	db := setupTestDB(t)
	client := redis.NewClient(&redis.Options{Addr: os.Getenv("PLATEN_M1_RESTART_REDIS_ADDR")})
	config.Redis = client
	t.Cleanup(func() { _ = client.Close() })
	if err := tasks.Registry.SweepDurableCompletions(t.Context(), time.Now()); err != nil {
		t.Fatal(err)
	}
	row := completionRow(t, db, os.Getenv("PLATEN_M1_RESTART_TASK_ID"))
	if row.State != "completed" {
		t.Fatalf("restarted reconciler state=%q; want completed", row.State)
	}
}
