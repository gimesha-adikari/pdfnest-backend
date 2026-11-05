package tasks

import (
	"strings"
	"testing"

	"pdfnest-backend/internal/storage"

	"github.com/google/uuid"
)

func TestArtifactRecoveryObjectKeyDoesNotEmbedUntrustedTaskID(t *testing.T) {
	actionID := uuid.NewString()
	key := artifactRecoveryObjectKey("../../another-owner/output", actionID)
	if !storage.CanonicalObjectKey(key) {
		t.Fatalf("recovery key is not canonical: %q", key)
	}
	if strings.Contains(key, "another-owner") || strings.Contains(key, "..") {
		t.Fatalf("recovery key embeds untrusted task ID: %q", key)
	}
	if key == artifactRecoveryObjectKey("different-task", actionID) {
		t.Fatal("different task identities must not share a recovery key")
	}
}
