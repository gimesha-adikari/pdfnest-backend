package acquisition

import (
	"fmt"
	"os"
	"path/filepath"
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
	mu        sync.Mutex
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

	folderName := "platen_repo_" + analysisID
	rootPath := filepath.Join(absBase, folderName)

	// Strict parent containment check
	baseWithSep := filepath.Clean(absBase) + string(filepath.Separator)
	if !strings.HasPrefix(rootPath, baseWithSep) {
		return nil, ErrSandboxEscape
	}

	// Remove any existing leftover directory for this ID
	_ = os.RemoveAll(rootPath)

	if err := os.MkdirAll(rootPath, 0700); err != nil {
		return nil, fmt.Errorf("create sandbox directory: %w", err)
	}

	return &Sandbox{
		ID:        analysisID,
		RootPath:  rootPath,
		CreatedAt: time.Now().UTC(),
		closed:    false,
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
	if s.RootPath != "" && s.RootPath != "/" {
		return os.RemoveAll(s.RootPath)
	}
	return nil
}

// IsClosed returns true if the sandbox has already been cleaned up.
func (s *Sandbox) IsClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
