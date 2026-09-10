package process

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestLimitedWriterConsumesDiscardedBytes(t *testing.T) {
	var captured bytes.Buffer
	writer := &limitedWriter{buf: &captured, limit: 4}
	n, err := writer.Write([]byte("123456"))
	if err != nil || n != 6 || captured.String() != "1234" {
		t.Fatalf("write consumed %d bytes, error %v, captured %q", n, err, captured.String())
	}
}

func TestRunnerDrainsOutputBeyondCaptureLimit(t *testing.T) {
	output, err := (Runner{}).Run(context.Background(), 5*time.Second, "python3", "-c", "import sys; sys.stdout.write('x' * (2 * 1024 * 1024))")
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != strings.Repeat("x", 1<<20) {
		t.Fatalf("unexpected capture length %d", len(output))
	}
}
