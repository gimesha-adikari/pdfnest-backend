package conversion

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Removing validation before the browser starts must fail this test. The fake
// browser prevents the pre-fix renderer from issuing any network requests.
func TestMarkdownRejectsExternalImageBeforeStartingBrowser(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "browser-started")
	launcher := "#!/bin/sh\ntouch '" + marker + "'\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "headless_shell"), []byte(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":/usr/bin:/bin")
	input := filepath.Join(dir, "input.md")
	if err := os.WriteFile(input, []byte("![forbidden](http://127.0.0.1:12345/image.png)"), 0600); err != nil {
		t.Fatal(err)
	}
	output, err := (&ConversionService{}).MarkdownToPdf(context.Background(), input, PrintOptions{})
	if output != "" || err == nil || !strings.Contains(err.Error(), "markdown resource policy") {
		t.Fatalf("want explicit offline-policy failure, got output=%q err=%v", output, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("forbidden input started the browser: %v", err)
	}
}
