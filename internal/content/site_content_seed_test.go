package content

import (
	"os"
	"strings"
	"testing"
)

func TestAboutSeedUsesSemanticToolTitle(t *testing.T) {
	seed, err := os.ReadFile("site_content_seed.go")
	if err != nil {
		t.Fatalf("read About seed source: %v", err)
	}

	seedSource := string(seed)
	if strings.Contains(seedSource, "37+ PDF Tools") {
		t.Fatal("About seed must not retain an independently stored numeric tool count")
	}
	if !strings.Contains(seedSource, `"PDF Tools"`) {
		t.Fatal(`About seed must retain the semantic "PDF Tools" title`)
	}
}
