package conversion

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestPreviewWorkerDedupScopeIsStableAndIdentityBound(t *testing.T) {
	t.Setenv("WORKER_SHARED_SECRET", "synthetic-preview-worker-secret")

	guestA := previewWorkerDedupScope(" guest:a ")
	guestAAgain := previewWorkerDedupScope("guest:a")
	guestB := previewWorkerDedupScope("guest:b")

	if guestA != guestAAgain {
		t.Fatal("the same normalized identity did not receive a stable worker deduplication scope")
	}
	if guestA == guestB {
		t.Fatal("different identities received the same worker deduplication scope")
	}
	if len(guestA) != 64 {
		t.Fatalf("worker deduplication scope has length %d, want 64 hexadecimal characters", len(guestA))
	}
	if _, err := hex.DecodeString(guestA); err != nil {
		t.Fatalf("worker deduplication scope is not hexadecimal: %v", err)
	}
	if strings.Contains(guestA, "guest") {
		t.Fatal("worker deduplication scope exposed identity text")
	}
}

func TestPreviewSessionCacheScopesSessionsByOwner(t *testing.T) {
	cache := &previewSessionCache{
		sessions: make(map[string]*previewWorkerSession),
		byHash:   make(map[string]string),
	}

	guestA := &previewWorkerSession{
		ID:         "worker-a",
		OwnerID:    "guest-a",
		SourceHash: "same-pdf",
		PageCount:  3,
		CreatedAt:  time.Now(),
	}
	guestB := &previewWorkerSession{
		ID:         "worker-b",
		OwnerID:    "guest-b",
		SourceHash: "same-pdf",
		PageCount:  3,
		CreatedAt:  time.Now(),
	}
	cache.put(guestA)
	cache.put(guestB)

	gotA, ok := cache.getByHash("guest-a", "same-pdf")
	if !ok || gotA.ID != guestA.ID {
		t.Fatalf("guest A did not receive its own preview session: %#v, %v", gotA, ok)
	}
	gotB, ok := cache.getByHash("guest-b", "same-pdf")
	if !ok || gotB.ID != guestB.ID {
		t.Fatalf("guest B did not receive its own preview session: %#v, %v", gotB, ok)
	}

	if _, err := cache.getByIDForOwner(guestA.ID, "guest-b"); !errors.Is(err, ErrPreviewSessionForbidden) {
		t.Fatalf("cross-owner lookup error = %v, want ErrPreviewSessionForbidden", err)
	}
	if _, err := cache.getByIDForOwner("missing", "guest-a"); !errors.Is(err, ErrPreviewSessionNotFound) {
		t.Fatalf("missing lookup error = %v, want ErrPreviewSessionNotFound", err)
	}

	cache.deleteByHash("guest-a", "same-pdf")
	if _, ok := cache.getByHash("guest-a", "same-pdf"); ok {
		t.Fatal("guest A preview session remained after owner-scoped deletion")
	}
	if gotB, ok := cache.getByHash("guest-b", "same-pdf"); !ok || gotB.ID != guestB.ID {
		t.Fatal("owner-scoped deletion removed guest B preview session")
	}
}
