package acquisition

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"pdfnest-backend/internal/analyzer/engine/acquisition/gitquota"
	"pdfnest-backend/internal/process"
)

func isolatedGitEnv(home, temp string) []string {
	return []string{"PATH=/usr/bin:/bin", "HOME=" + home, "XDG_CONFIG_HOME=" + home,
		"XDG_CACHE_HOME=" + home, "XDG_DATA_HOME=" + home, "XDG_STATE_HOME=" + home,
		"TMPDIR=" + temp, "TMP=" + temp, "TEMP=" + temp,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0", "GIT_LFS_SKIP_SMUDGE=1", "LC_ALL=C"}
}

// Required mode consumes independent administrator-owned configuration and
// verifies kernel quota state through the fixed read-only broker before DNS.
// Monitoring remains a separate defense and is never promoted to a hard quota.
func checkGitStoragePolicy() error {
	switch os.Getenv("ANALYZER_GIT_STORAGE_POLICY") {
	case "", "monitored":
		return nil
	case "required":
		manager, err := gitquota.Load(os.Getenv("ANALYZER_GIT_QUOTA_CONFIG"))
		if err == nil {
			err = manager.Probe()
		}
		if err != nil {
			return fmt.Errorf("%w: %w: %v", ErrGitAcquisitionFailed, ErrGitStorageUnavailable, err)
		}
		return nil
	default:
		return fmt.Errorf("%w: invalid Git storage policy", ErrGitAcquisitionFailed)
	}
}

func effectiveGitLimits(limits AcquisitionLimits) AcquisitionLimits {
	defaults := DefaultAcquisitionLimits()
	if limits.MaxExtractedBytes <= 0 {
		limits.MaxExtractedBytes = defaults.MaxExtractedBytes
	}
	if limits.MaxFileCount <= 0 {
		limits.MaxFileCount = defaults.MaxFileCount
	}
	return limits
}

// Count ALL scratch files, including .git pack/object/index and checkout data.
// This is deliberately stricter than counting only analysis-visible source.
func checkGitWorkspace(ctx context.Context, root string, limits AcquisitionLimits) (int, int64, error) {
	var files int
	var bytes, diskBytes int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if os.IsNotExist(walkErr) && path != root {
				return nil
			} // pack rename during traversal
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			} // files can disappear while Git repacks
			return err
		}
		usage := info.Size()
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Blocks*512 > usage {
			usage = stat.Blocks * 512
		}
		if usage > limits.MaxExtractedBytes-diskBytes {
			return fmt.Errorf("%w: allocated/logical scratch disk exceeds %d", ErrGitResourceLimit, limits.MaxExtractedBytes)
		}
		diskBytes += usage
		if entry.IsDir() {
			return nil
		}
		files++
		if files > limits.MaxFileCount {
			return fmt.Errorf("%w: scratch file count exceeds %d (including Git metadata)", ErrGitResourceLimit, limits.MaxFileCount)
		}
		// Compare without overflowing the accumulator for hostile sparse files.
		if info.Size() > limits.MaxExtractedBytes-bytes {
			return fmt.Errorf("%w: scratch bytes exceed %d (including Git objects)", ErrGitResourceLimit, limits.MaxExtractedBytes)
		}
		bytes += info.Size()
		return nil
	})
	return files, bytes, err
}

// Monitoring limits growth during acquisition and a final scan rejects clones
// finishing between ticks. This is NOT a filesystem quota: writes can overshoot
// between scans and during termination. A hard scratch ceiling needs OS quotas.
func runBoundedGit(ctx context.Context, cmd *exec.Cmd, root string, limits AcquisitionLimits) ([]byte, error) {
	limits = effectiveGitLimits(limits)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopped := make(chan struct{})
	monitored := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopped:
				monitored <- nil
				return
			case <-ctx.Done():
				monitored <- nil
				return
			case <-ticker.C:
				_, _, err := checkGitWorkspace(runCtx, root, limits)
				if err != nil {
					cancel()
					monitored <- err
					return
				}
			}
		}
	}()
	output, runErr := (process.Runner{}).RunCommand(runCtx, 0, cmd)
	// A normally exiting Git parent must not leave descendants that can write
	// after the sandbox is released. Confinement denies setsid/setpgid escape.
	if cmd.Process != nil {
		_ = process.KillProcessGroup(cmd.Process.Pid, 50*time.Millisecond)
	}
	close(stopped)
	if err := <-monitored; err != nil {
		return output, err
	}
	if runErr != nil {
		return output, runErr
	}
	_, _, err := checkGitWorkspace(ctx, root, limits)
	return output, err
}
