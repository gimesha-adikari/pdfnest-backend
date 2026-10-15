//go:build linux

package storage

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
	"pdfnest-backend/internal/temp"
)

// Mirrors internal/tasks/cleanup.go; the test checks the list for drift. Do not
// import tasks (which consumes storage), or change the sweeper's behavior here.
var artifactSweepPrefixes = []string{
	"pdfnest-", "split-", "merged-", "inserted-", "deleted-", "numbered-",
	"texted-", "reorder-", "cropped-", "rotated-", "duplicated-", "metadata-",
	"watermarked-", "pdf-page-", "html-compiled-", "md-compiled-", "img-compiled-",
	"code-compiled-", "office-compiled-", "ocr-compiled-", "r2-img-", "compressed-",
	"extracted-", "tmp-", "chromedp-runner-",
}

func resolvedArtifactDirectory(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == path
}
func artifactRootChild(base, path string) (string, bool) {
	relative, err := filepath.Rel(base, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return strings.SplitN(relative, string(filepath.Separator), 2)[0], true
}
func sweepableArtifactRoot(root, system, dedicated string) bool {
	// The root directories themselves are not durable-output locations: creating
	// ocr-finalization under the dedicated root exposes it to its child scan.
	if root == system || root == dedicated {
		return true
	}
	if child, inside := artifactRootChild(dedicated, root); inside && dedicated != system && !strings.HasPrefix(child, "pdfnest-temp") {
		return true
	}
	if child, inside := artifactRootChild(system, root); inside {
		if strings.HasPrefix(child, "pdfnest-temp") || filepath.Join(system, child) == dedicated {
			return false
		}
		for _, prefix := range artifactSweepPrefixes {
			if strings.HasPrefix(child, prefix) {
				return true
			}
		}
	}
	return false
}

// openArtifactRoot checks the current application sweeper boundary, not OS or
// administrator retention. A trusted root must not be relocated after checking.
func openArtifactRoot(root string) (int, error) {
	system := os.TempDir()
	if !resolvedArtifactDirectory(root) || !resolvedArtifactDirectory(system) {
		return -1, createError(ArtifactCreateUnsafeRoot)
	}
	dedicated := temp.GetDir()
	if !resolvedArtifactDirectory(dedicated) || sweepableArtifactRoot(root, system, dedicated) {
		return -1, createError(ArtifactCreateUnsafeRoot)
	}
	// Hold a root opened without following symlinks in ANY component. This also
	// prevents an ancestor symlink substitution between resolution and open.
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, createError(ArtifactCreateUnsafeRoot)
	}
	for _, part := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		if part == "" {
			continue
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, createError(ArtifactCreateUnsafeRoot)
		}
		fd = next
	}
	return fd, nil
}
