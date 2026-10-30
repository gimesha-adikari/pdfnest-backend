package billing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"pdfnest-backend/config"
	"pdfnest-backend/internal/models"
	"pdfnest-backend/internal/storage"
	"pdfnest-backend/internal/tasks"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

func TestDurableCompletionArtifactIntegrityBeforeBilling(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"wrong", []byte("unrelated object")},
		{"truncated", []byte("durable")},
		{"empty", nil},
		{"same_length_altered", []byte("DURABLE RESULT\n")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, _, _, row, source := completionFixture(t, false)
			key := "outputs/tasks/" + row.ID + "/result.txt"
			if err := storage.SaveObjectBytes(t.Context(), key, tc.data, "text/plain"); err != nil {
				t.Fatal(err)
			}
			err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, source, "text/plain")
			current := completionRow(t, db, row.ID)
			var reservation config.BillingReservation
			if lookupErr := db.First(&reservation, "id = ?", row.ReservationID).Error; lookupErr != nil {
				t.Fatal(lookupErr)
			}
			if !errors.Is(err, tasks.ErrSettlementPending) || current.State != "publishing" || reservation.Status != "reserved" {
				t.Fatalf("corrupt object accepted: error=%v task=%s reservation=%s recorded_bytes=%d actual_bytes=%d", err, current.State, reservation.Status, current.ArtifactBytes, len(tc.data))
			}
			actual, readErr := os.ReadFile(current.ResultURL)
			if readErr != nil || !bytes.Equal(actual, tc.data) {
				t.Fatalf("unverified object overwritten: %q %v", actual, readErr)
			}
			if err := tasks.Registry.ReconcileCompletion(t.Context(), row.ID); !errors.Is(err, tasks.ErrSettlementPending) {
				t.Fatalf("reconciliation accepted corrupt object: %v", err)
			}
			assertReserved(t, db, row.ReservationID, "reserved")
		})
	}
}

func publishingIdentity(t *testing.T, db *gorm.DB, row *models.TaskCompletion, source, key string) []byte {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(data)
	if err := db.Model(row).Updates(map[string]any{"state": "publishing", "result_key": key, "result_url": filepath.Join(storage.GetLocalStorageDir(), filepath.FromSlash(key)), "artifact_sha256": hex.EncodeToString(h[:]), "artifact_bytes": len(data)}).Error; err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDurableCompletionMatchingObjectRecoversLostUploadAcknowledgement(t *testing.T) {
	db, client, _, row, source := completionFixture(t, false)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	data := publishingIdentity(t, db, row, source, key)
	// Crash/lost acknowledgement after object publication, before settlement.
	if err := storage.SaveObjectBytes(t.Context(), key, data, "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	client.Del(t.Context(), tasks.TaskKeyPrefix+row.ID)
	for range 3 {
		if err := tasks.Registry.ReconcileCompletion(t.Context(), row.ID); err != nil {
			t.Fatal(err)
		}
	}
	assertReserved(t, db, row.ReservationID, "committed")
	if completionRow(t, db, row.ID).State != "completed" {
		t.Fatal("matching stored bytes did not recover")
	}
	// Completed retries do not need their deleted source or overwrite the key.
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, source, "text/plain"); err != nil {
		t.Fatal(err)
	}
}

func TestDurableCompletionSourceMutationAfterIdentityUsesCapturedBytes(t *testing.T) {
	db, _, _, row, source := completionFixture(t, false)
	callback := "mutate_artifact_source_" + uuid.NewString()
	if err := db.Callback().Update().After("gorm:update").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Table == "task_completions" {
			if err := os.WriteFile(source, []byte("substitution after recorded fingerprint"), 0600); err != nil {
				t.Error(err)
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Update().Remove(callback)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := tasks.Registry.PublishArtifact(t.Context(), row.ID, key, source, "text/plain"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(completionRow(t, db, row.ID).ResultURL)
	if err != nil || string(data) != "durable result\n" {
		t.Fatalf("source mutation reached billed artifact: %q %v", data, err)
	}
	assertReserved(t, db, row.ReservationID, "committed")
}

func TestDurablePublicationRetryAfterSourceLoss(t *testing.T) {
	for _, scenario := range []string{"matching", "corrupt", "missing", "different_key"} {
		t.Run(scenario, func(t *testing.T) {
			db, client, _, row, source := completionFixture(t, false)
			key := "outputs/tasks/" + row.ID + "/result.txt"
			data := publishingIdentity(t, db, row, source, key)
			if scenario == "corrupt" {
				data[0] ^= 1
			}
			if scenario != "missing" {
				if err := storage.SaveObjectBytes(t.Context(), key, data, "text/plain"); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			client.Del(t.Context(), tasks.TaskKeyPrefix+row.ID)
			requestedKey := key
			if scenario == "different_key" {
				requestedKey = "outputs/tasks/" + row.ID + "/other.txt"
			}
			for range 2 {
				err := tasks.Registry.PublishArtifact(t.Context(), row.ID, requestedKey, source, "text/plain")
				current := completionRow(t, db, row.ID)
				if scenario == "matching" {
					if err != nil || current.State != "completed" {
						t.Fatalf("lost-source retry did not recover: %v state=%s", err, current.State)
					}
					assertReserved(t, db, row.ReservationID, "committed")
				} else {
					if err == nil || current.State != "publishing" || current.ResultKey != key {
						t.Fatalf("unsafe source-loss retry: %v state=%s key=%s", err, current.State, current.ResultKey)
					}
					assertReserved(t, db, row.ReservationID, "reserved")
				}
			}
		})
	}
}

func TestDurableCompletionMissingArtifactRecoversOnlyMatchingDurableWorker(t *testing.T) {
	for _, scenario := range []string{"valid", "content", "owner", "worker_id", "state", "status", "namespace", "submit_path", "native_source_lost"} {
		t.Run(scenario, func(t *testing.T) {
			db, client, _, row, source := completionFixture(t, false)
			key := "outputs/tasks/" + row.ID + "/result.md"
			data := publishingIdentity(t, db, row, source, key)
			workerKey := "jobs/markdown/output/" + uuid.NewString() + ".md"
			expired := time.Now().Add(-time.Hour)
			owned := models.WorkerResultOwnership{ID: uuid.NewString(), RequestID: uuid.NewString(), RequestHash: "hash", WorkerJobID: row.ID, OwnerIdentity: row.OwnerIdentity, SubmitPath: "/api/v1/jobs/submit", SourceKeys: `[]`, State: "terminal", ResultKey: workerKey, StatusJSON: `{"status":"succeeded"}`, ExpiresAt: &expired}
			switch scenario {
			case "content":
				data[0] ^= 1
			case "owner":
				owned.OwnerIdentity = uuid.NewString()
			case "worker_id":
				owned.WorkerJobID = uuid.NewString()
			case "state":
				owned.State = "uncertain"
			case "status":
				owned.StatusJSON = `{"status":"failed"}`
			case "namespace":
				owned.ResultKey = "owned/other/result.md"
			case "submit_path":
				owned.SubmitPath = "/api/v1/editor/extract"
			}
			if err := storage.SaveObjectBytes(t.Context(), workerKey, data, "text/markdown"); err != nil {
				t.Fatal(err)
			}
			if scenario != "native_source_lost" {
				if err := db.Create(&owned).Error; err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Delete(&owned) })
			}
			if err := os.Remove(source); err != nil {
				t.Fatal(err)
			}
			client.Del(t.Context(), tasks.TaskKeyPrefix+row.ID)
			for range 2 {
				err := tasks.Registry.ReconcileCompletion(t.Context(), row.ID)
				if scenario == "valid" {
					if err != nil {
						t.Fatal(err)
					}
				} else if !errors.Is(err, tasks.ErrSettlementPending) {
					t.Fatalf("unsafe source accepted: %v", err)
				}
			}
			if scenario == "valid" {
				assertReserved(t, db, row.ReservationID, "committed")
				if completionRow(t, db, row.ID).State != "completed" {
					t.Fatal("source-free recovery did not complete")
				}
			} else {
				assertReserved(t, db, row.ReservationID, "reserved")
				if completionRow(t, db, row.ID).State != "publishing" || storage.ObjectExists(t.Context(), key) {
					t.Fatal("unverified recovery changed outcome")
				}
			}
			if !storage.ObjectExists(t.Context(), workerKey) {
				t.Fatal("last recovery object deleted")
			}
		})
	}
}

func TestDurableCompletionGuestIntegrityFailureDoesNotCommit(t *testing.T) {
	db, client, _, row, source := completionFixture(t, true)
	key := "outputs/tasks/" + row.ID + "/result.txt"
	if err := storage.SaveObjectBytes(t.Context(), key, []byte("different bytes"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if err := tasks.Registry.PublishArtifact(context.Background(), row.ID, key, source, "text/plain"); !errors.Is(err, tasks.ErrSettlementPending) {
		t.Fatal(err)
	}
	if completionRow(t, db, row.ID).State != "publishing" {
		t.Fatal("guest ownership lost")
	}
	if client.Exists(t.Context(), GuestQuota.outcomeKey(row.ReservationID)).Val() != 0 {
		t.Fatal("corrupt artifact committed guest reservation")
	}
	if client.Exists(t.Context(), GuestQuota.resKey(row.ReservationID)).Val() != 1 {
		t.Fatal("guest reservation released")
	}
}
