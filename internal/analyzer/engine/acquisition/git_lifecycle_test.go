package acquisition

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCloneDeadlineTerminatesInheritedOutputPipes(t *testing.T) {
	bin := t.TempDir()
	// Model a Git transport child retaining stdout after its parent is killed.
	require.NoError(t, os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\n/bin/sleep 3 &\nwait\n"), 0700))
	t.Setenv("PATH", bin)
	sandbox, err := NewSandbox(t.TempDir(), "deadline")
	require.NoError(t, err)
	defer sandbox.Cleanup()
	limits := DefaultAcquisitionLimits()
	limits.GitTimeout = 100 * time.Millisecond
	started := time.Now()
	_, err = CloneGitRepository(context.Background(), "https://8.8.8.8/repository.git", sandbox, limits)
	require.ErrorIs(t, err, ErrGitTimeout)
	require.Less(t, time.Since(started), 2*time.Second, "transport descendants must not outlive the clone deadline")
}
