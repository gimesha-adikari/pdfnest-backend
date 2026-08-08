package disk

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

var ErrInsufficientStorage = errors.New("insufficient disk space available to perform requested document processing operation")

// CheckAvailableSpace inspects the filesystem containing targetDir and verifies that available
// disk space meets or exceeds requiredBytes. Returns ErrInsufficientStorage if space is insufficient.
func CheckAvailableSpace(targetDir string, requiredBytes uint64) error {
	dir := targetDir
	if dir == "" {
		dir = os.TempDir()
	}

	var stat unix.Statfs_t
	if err := unix.Statfs(dir, &stat); err != nil {
		// Fallback if Statfs fails: do not block execution
		return nil
	}

	availableBytes := stat.Bavail * uint64(stat.Bsize)
	if availableBytes < requiredBytes {
		return fmt.Errorf("%w: available %d MB, estimated requirement %d MB", ErrInsufficientStorage, availableBytes/(1024*1024), requiredBytes/(1024*1024))
	}

	return nil
}

// EstimateRequiredSpace computes estimated disk footprint for an operation given input size, multiplier, and headroom.
func EstimateRequiredSpace(inputSizeBytes int64, multiplier float64, headroomBytes uint64) uint64 {
	if inputSizeBytes <= 0 {
		return headroomBytes
	}
	estimated := uint64(float64(inputSizeBytes) * multiplier)
	return estimated + headroomBytes
}
