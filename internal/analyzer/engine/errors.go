package engine

import "errors"

var (
	// ErrInvalidSourceRoot is returned when the provided root path does not exist or is not a directory.
	ErrInvalidSourceRoot = errors.New("analyzer: invalid or non-existent source root directory")

	// ErrContextCancelled is returned when the execution context was cancelled or timed out.
	ErrContextCancelled = errors.New("analyzer: operation cancelled by context")

	// ErrSecurityExclusion is returned when an operation attempts to process a forbidden security file.
	ErrSecurityExclusion = errors.New("analyzer: access denied by mandatory security exclusion rule")

	// ErrInvalidScope is returned when scope configuration contains invalid glob syntax or empty rules.
	ErrInvalidScope = errors.New("analyzer: invalid scope configuration")

	// ErrUnsupportedRepositorySize is returned when repository size or file count exceeds maximum operational ceilings.
	ErrUnsupportedRepositorySize = errors.New("analyzer: repository exceeds maximum supported operational limits")

	// ErrZipSlipDetected is returned when an archive entry attempts directory traversal outside the sandbox root.
	ErrZipSlipDetected = errors.New("analyzer: zip slip directory traversal attack detected")

	// ErrDecompressionRatioExceeded is returned when archive compression ratio exceeds security limits.
	ErrDecompressionRatioExceeded = errors.New("analyzer: decompression ratio exceeds maximum safe threshold (zip bomb protection)")

	// ErrSymlinkEscapeDetected is returned when a symlink resolves to a target outside the sandbox root.
	ErrSymlinkEscapeDetected = errors.New("analyzer: symlink resolves outside repository sandbox root")
)
