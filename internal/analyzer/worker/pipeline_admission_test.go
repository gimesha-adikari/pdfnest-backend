package worker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"pdfnest-backend/internal/analyzer/engine"
)

func TestPipelineAdmissionPerUserAndGlobal(t *testing.T) {
	t.Setenv("ANALYZER_MAX_ACTIVE_JOBS", "2")
	t.Setenv("ANALYZER_MAX_ACTIVE_JOBS_PER_USER", "1")
	first := make(chan struct{})
	other := make(chan struct{})
	blocked := make(chan struct{}, 1)
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 4)
	run := func(id, owner string, entered chan struct{}) {
		go func() {
			_, err := ExecutePipeline(ctx, &AnalyzerJob{TaskID: id, SessionID: id, SourceType: engine.SourceTypeLocalFolder, AdmissionIdentity: owner}, t.TempDir(), func(status TaskStatus, _ int, _ string) {
				if status == StatusAcquiring {
					entered <- struct{}{}
					<-release
				}
			})
			done <- err
		}()
	}
	run("one", "user:alice", first)
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first job not admitted")
	}
	run("two", "user:alice", blocked)
	run("three", "user:bob", other)
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("another user should use the other global slot")
	}
	run("four", "user:carol", blocked)
	select {
	case <-blocked:
		t.Fatal("per-user/global bound exceeded")
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	close(release)
	for i := 0; i < 4; i++ {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("admission wait did not cancel")
		}
	}
}

func TestPipelineAdmissionInvalidConfigAndCanceledContext(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"ANALYZER_MAX_ACTIVE_JOBS", "zero"}, {"ANALYZER_MAX_ACTIVE_JOBS", "0"},
		{"ANALYZER_MAX_ACTIVE_JOBS", "17"}, {"ANALYZER_MAX_ACTIVE_JOBS_PER_USER", "5"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			base := t.TempDir()
			_, err := ExecutePipeline(context.Background(), &AnalyzerJob{TaskID: "bad", SessionID: "bad", SourceType: engine.SourceTypeLocalFolder}, base, nil)
			require.Error(t, err)
			entries, err := os.ReadDir(base)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := ExecutePipeline(ctx, &AnalyzerJob{TaskID: "cancel", SessionID: "cancel", SourceType: engine.SourceTypeLocalFolder}, t.TempDir(), nil)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPipelineCleanupFailureDoesNotPublishCompletion(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("requires ordinary filesystem permission checks")
	}
	base := t.TempDir()
	scratch := ""
	completed := false
	_, err := ExecutePipeline(context.Background(), &AnalyzerJob{TaskID: "cleanup", SessionID: "cleanup", SourceType: engine.SourceTypeLocalFolder}, base, func(status TaskStatus, _ int, _ string) {
		if status == StatusAcquiring {
			entries, err := os.ReadDir(base)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			scratch = filepath.Join(base, entries[0].Name())
			require.NoError(t, os.WriteFile(filepath.Join(scratch, "blocked"), []byte("retained"), 0600))
			require.NoError(t, os.Chmod(scratch, 0500))
		}
		if status == StatusCompleted {
			completed = true
		}
	})
	require.ErrorContains(t, err, "sandbox cleanup failed")
	require.False(t, completed)
	require.NoError(t, os.Chmod(scratch, 0700))
	require.NoError(t, os.RemoveAll(scratch))
}
