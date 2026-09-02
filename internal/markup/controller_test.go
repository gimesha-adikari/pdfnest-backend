package markup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyMarkupPayloadCarriesExplicitConsumerMarker(t *testing.T) {
	payload, err := json.Marshal(legacyMarkupPayload(
		[]Box{{Page: 1, X: 10, Y: 20, Width: 30, Height: 12}},
		"smart",
		"",
		ActionHighlight,
	))
	if err != nil {
		t.Fatalf("marshal legacy markup payload: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal legacy markup payload: %v", err)
	}
	if decoded["consumer"] != LegacyMarkupConsumer {
		t.Fatalf("expected consumer marker %q, got %v", LegacyMarkupConsumer, decoded["consumer"])
	}
	if decoded["mode"] != "smart" || decoded["action"] != string(ActionHighlight) {
		t.Fatalf("legacy markup operation fields changed: %#v", decoded)
	}
}

func TestLegacyMarkupPersistsObjectsInConfiguredLocalStorage(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("STORAGE_MODE", "local")
	t.Setenv("LOCAL_STORAGE_DIR", t.TempDir())
	t.Setenv("FILE_ENCRYPTION_KEY", "")

	sourcePath := filepath.Join(t.TempDir(), "source.pdf")
	sourceBytes := []byte("%PDF-local-markup-source")
	if err := os.WriteFile(sourcePath, sourceBytes, 0600); err != nil {
		t.Fatalf("write source fixture: %v", err)
	}

	if err := persistMarkupSource(context.Background(), "markup/source/test.pdf", sourcePath); err != nil {
		t.Fatalf("persist local source: %v", err)
	}

	payloadBytes := []byte(`{"consumer":"legacy_markup"}`)
	if err := persistMarkupPayload(context.Background(), "markup/payload/test.json", payloadBytes); err != nil {
		t.Fatalf("persist local payload: %v", err)
	}

	root := os.Getenv("LOCAL_STORAGE_DIR")
	for key, want := range map[string][]byte{
		"markup/source/test.pdf":   sourceBytes,
		"markup/payload/test.json": payloadBytes,
	} {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(key)))
		if err != nil {
			t.Fatalf("read persisted %s: %v", key, err)
		}
		if string(got) != string(want) {
			t.Fatalf("persisted %s changed: got %q want %q", key, got, want)
		}
	}
}
