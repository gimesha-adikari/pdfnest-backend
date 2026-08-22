package acquisition

import (
	"errors"
	"os"
	"strconv"
	"time"

	"pdfnest-backend/internal/analyzer/engine"
)

var (
	// ErrSSRFBlocked is returned when a URL points to a loopback, private, link-local, or cloud metadata IP address.
	ErrSSRFBlocked = errors.New("acquisition: network target blocked by SSRF defense policy")

	// ErrUnsupportedGitProtocol is returned when a non-HTTPS Git URL is provided.
	ErrUnsupportedGitProtocol = errors.New("acquisition: unsupported Git protocol (HTTPS required)")

	// ErrGitCloneFailed is returned when Git shallow clone fails or exits with non-zero code.
	ErrGitCloneFailed = errors.New("acquisition: git clone operation failed")

	// ErrGitAcquisitionFailed is returned when Git shallow clone fails.
	ErrGitAcquisitionFailed = errors.New("GIT_ACQUISITION_FAILED: git clone operation failed")

	// ErrGitTimeout is returned when Git clone exceeds the acquisition deadline.
	ErrGitTimeout = errors.New("GIT_TIMEOUT: git clone operation timed out")

	// ErrGitResourceLimit is returned when Git clone process is terminated due to memory or system resource limits.
	ErrGitResourceLimit = errors.New("GIT_RESOURCE_LIMIT: git clone process exceeded system resource limits")

	// ErrGitUnreachable is returned when the remote Git host or network target is unreachable.
	ErrGitUnreachable = errors.New("GIT_UNREACHABLE: could not connect to remote git repository")

	// ErrGitAuthRequired is returned when the repository requires private authentication.
	ErrGitAuthRequired = errors.New("GIT_AUTH_REQUIRED: authentication required for repository")

	// ErrGitInvalidRepository is returned when the remote repository URL or ref does not exist.
	ErrGitInvalidRepository = errors.New("GIT_INVALID_REPOSITORY: repository not found or inaccessible")

	// ErrMaxExtractedSizeExceeded is returned when uncompressed archive volume exceeds the 250 MB ceiling.
	ErrMaxExtractedSizeExceeded = errors.New("acquisition: archive extracted size exceeds maximum allowed ceiling (250 MB)")

	// ErrMaxFileCountExceeded is returned when extracted file count exceeds the 25,000 files ceiling.
	ErrMaxFileCountExceeded = errors.New("acquisition: archive file count exceeds maximum allowed ceiling (25,000 files)")

	// ErrArchiveSymlinkRejected is returned when an archive contains a hostile or external symlink.
	ErrArchiveSymlinkRejected = errors.New("acquisition: archive symlink entries are rejected for security")

	// ErrSandboxEscape is returned when a resolved path escapes the sandbox root boundary.
	ErrSandboxEscape = errors.New("acquisition: path escapes sandbox root boundary")

	// ErrInvalidAnalysisID is returned when an invalid analysis/session ID is supplied.
	ErrInvalidAnalysisID = errors.New("acquisition: invalid or malformed analysis session identifier")

	// ErrSandboxClosed is returned when an operation is attempted on an already cleaned up sandbox.
	ErrSandboxClosed = errors.New("acquisition: sandbox workspace is closed or cleaned up")
)

// AcquisitionLimits encapsulates operational resource limits and timeouts for repository acquisition.
type AcquisitionLimits struct {
	MaxExtractedBytes     int64
	MaxFileCount          int
	MaxDecompressionRatio float64
	GitTimeout            time.Duration
	ExtractTimeout        time.Duration
}

// DefaultAcquisitionLimits returns the standard production limits (250 MB, 25k files, 100:1 ratio, 300s git timeout).
func DefaultAcquisitionLimits() AcquisitionLimits {
	gitTimeout := 300 * time.Second
	if envSec := os.Getenv("ANALYZER_GIT_TIMEOUT_SECONDS"); envSec != "" {
		if sec, err := strconv.Atoi(envSec); err == nil && sec > 0 {
			gitTimeout = time.Duration(sec) * time.Second
		}
	}
	return AcquisitionLimits{
		MaxExtractedBytes:     250 * 1024 * 1024, // 250 MB
		MaxFileCount:          25000,             // 25,000 files
		MaxDecompressionRatio: 100.0,             // 100:1 ratio
		GitTimeout:            gitTimeout,        // Configurable git timeout (default 5m)
		ExtractTimeout:        30 * time.Second,  // 30s extraction timeout
	}
}

// AcquisitionResult summarizes the prepared ephemeral workspace and acquisition provenance.
type AcquisitionResult struct {
	SandboxPath           string            `json:"sandboxPath"`
	SourceType            engine.SourceType `json:"sourceType"`
	RepositoryName        string            `json:"repositoryName"`
	CommitHash            string            `json:"commitHash,omitempty"`
	DefaultBranch         string            `json:"defaultBranch,omitempty"`
	ArchiveSha256         string            `json:"archiveSha256"`
	TotalFiles            int               `json:"totalFiles"`
	TotalBytes            int64             `json:"totalBytes"`
	AcquisitionDurationMs int64             `json:"acquisitionDurationMs"`
}
