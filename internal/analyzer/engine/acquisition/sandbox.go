package acquisition

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"pdfnest-backend/internal/analyzer/engine/acquisition/gitquota"
	"regexp"
	"strings"
	"sync"
	"time"
)

var validIDRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

// Sandbox represents an isolated ephemeral filesystem workspace bound to a specific analysis session.
type Sandbox struct {
	ID        string    `json:"id"`
	RootPath  string    `json:"rootPath"`
	CreatedAt time.Time `json:"createdAt"`
	closed    bool
	// scratchPath owns the repository and auxiliary writes for one execution.
	// It is not a quota-backed slot or an execution confinement boundary.
	scratchPath string
	quotaLease  *gitquota.Lease
	mu          sync.Mutex
}

// NewSandbox creates and initializes a secure, bounded ephemeral workspace directory.
func NewSandbox(baseDir string, analysisID string) (*Sandbox, error) {
	if !validIDRegex.MatchString(analysisID) {
		return nil, ErrInvalidAnalysisID
	}

	if baseDir == "" {
		baseDir = os.TempDir()
	}

	absBase, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, fmt.Errorf("resolve sandbox base directory: %w", err)
	}
	if err := os.MkdirAll(absBase, 0700); err != nil {
		return nil, fmt.Errorf("create sandbox base directory: %w", err)
	}

	// A repeated session must not remove or share another live execution's
	// workspace. The random generation is private and survives through analysis.
	scratchPath, err := os.MkdirTemp(absBase, "platen_repo_"+analysisID+"-")
	if err != nil {
		return nil, fmt.Errorf("create sandbox scratch directory: %w", err)
	}
	rootPath := filepath.Join(scratchPath, "repository")

	// Strict parent containment check
	baseWithSep := filepath.Clean(absBase) + string(filepath.Separator)
	if !strings.HasPrefix(rootPath, baseWithSep) {
		_ = os.RemoveAll(scratchPath)
		return nil, ErrSandboxEscape
	}

	if err := os.MkdirAll(rootPath, 0700); err != nil {
		_ = os.RemoveAll(scratchPath)
		return nil, fmt.Errorf("create sandbox directory: %w", err)
	}

	return &Sandbox{
		ID:          analysisID,
		RootPath:    rootPath,
		CreatedAt:   time.Now().UTC(),
		closed:      false,
		scratchPath: scratchPath,
	}, nil
}

// ResolvePath safely maps a relative path within the sandbox, verifying it never escapes root.
func (s *Sandbox) ResolvePath(relPath string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return "", ErrSandboxClosed
	}

	norm := filepath.ToSlash(filepath.Clean(relPath))
	norm = strings.TrimPrefix(norm, "/")

	if strings.HasPrefix(norm, "../") || norm == ".." {
		return "", ErrSandboxEscape
	}

	resolved := filepath.Join(s.RootPath, filepath.FromSlash(norm))
	cleanResolved := filepath.Clean(resolved)

	rootClean := filepath.Clean(s.RootPath)
	rootWithSep := rootClean + string(filepath.Separator)

	if cleanResolved != rootClean && !strings.HasPrefix(cleanResolved, rootWithSep) {
		return "", ErrSandboxEscape
	}

	return cleanResolved, nil
}

// Cleanup idempotently removes the sandbox workspace from disk.
func (s *Sandbox) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	if s.quotaLease != nil {
		return s.quotaLease.Release()
	}
	if s.scratchPath != "" {
		return os.RemoveAll(s.scratchPath)
	}
	if s.RootPath != "" && s.RootPath != "/" {
		return os.RemoveAll(s.RootPath)
	}
	return nil
}

// admitGitQuota replaces the as-yet-empty ordinary sandbox with one exclusive
// preprovisioned project. Ownership survives clone success until final Cleanup.
func (s *Sandbox) admitGitQuota() error {
	if os.Getenv("ANALYZER_GIT_STORAGE_POLICY") != "required" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSandboxClosed
	}
	if s.quotaLease != nil {
		return nil
	}
	manager, err := gitquota.Load(os.Getenv("ANALYZER_GIT_QUOTA_CONFIG"))
	if err != nil {
		return fmt.Errorf("%w: %w: %v", ErrGitAcquisitionFailed, ErrGitStorageUnavailable, err)
	}
	lease, err := manager.Acquire()
	if err != nil {
		return fmt.Errorf("%w: %w: %v", ErrGitAcquisitionFailed, ErrGitStorageUnavailable, err)
	}
	if err := os.RemoveAll(s.scratchPath); err != nil {
		_ = lease.Release()
		return err
	}
	s.scratchPath, s.RootPath, s.quotaLease = lease.Path(), filepath.Join(lease.Path(), "repository"), lease
	if err := os.Mkdir(s.RootPath, 0700); err != nil {
		return err
	}
	return nil
}

func (s *Sandbox) confineGit(cmd *exec.Cmd) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrSandboxClosed
	}
	if s.quotaLease == nil {
		return nil
	}
	if cmd.Path != "/usr/bin/git" {
		return fmt.Errorf("%w: required mode uses fixed system Git", ErrGitStorageUnavailable)
	}
	cmd.Args = append([]string{s.quotaLease.Helper(), "run", s.quotaLease.ConfigPath(), s.quotaLease.Path(), s.quotaLease.Token(), "--"}, cmd.Args[1:]...)
	cmd.Path = s.quotaLease.Helper()
	return nil
}

// gitScratch prepares auxiliary paths beside the empty clone destination, so
// HOME, temporary files and repository writes share the same accounting root.
func (s *Sandbox) gitScratch() (root, home, temp string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", "", "", ErrSandboxClosed
	}
	if s.scratchPath == "" {
		return "", "", "", fmt.Errorf("%w: scratch ownership unavailable", ErrGitAcquisitionFailed)
	}
	home = filepath.Join(s.scratchPath, "home")
	temp = filepath.Join(s.scratchPath, "tmp")
	for _, path := range []string{home, temp} {
		if err := os.Mkdir(path, 0700); err != nil {
			return "", "", "", err
		}
	}
	return s.scratchPath, home, temp, nil
}

// IsClosed returns true if the sandbox has already been cleaned up.
func (s *Sandbox) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
