//go:build linux

package gitquota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

var ErrUnavailable = errors.New("verified hard Git storage unavailable")

type Slot struct {
	Path       string `json:"path"`
	Project    uint32 `json:"project"`
	Generation uint64 `json:"generation"`
}

// Config is an independent root-owned authorization record. Runtime code never
// creates it or its parent directories. The worker UID must be non-root; Git
// children cannot modify this authority or the private state outside their slot.
type Config struct {
	Version   int    `json:"version"`
	WorkerUID int    `json:"workerUid"`
	StateDir  string `json:"stateDir"`
	Helper    string `json:"helper"`
	Slots     []Slot `json:"slots"`
}

type Manager struct {
	config     Config
	kernel     inspector
	configPath string
}

// The fixed administrator-owned helper is the read-only quota broker. It may
// carry a separately approved file capability; the worker and Git never receive
// quota authority. No capability is installed by this package.
type helperInspector struct{ helper, config string }

func (h helperInspector) Inspect(slot Slot) (Usage, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.helper, "inspect", h.config, slot.Path)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	output, err := cmd.Output()
	if err != nil {
		return Usage{}, fmt.Errorf("trusted quota broker read failed: %w", err)
	}
	if len(output) > 4096 {
		return Usage{}, fmt.Errorf("invalid broker response")
	}
	var usage Usage
	if err := json.Unmarshal(output, &usage); err != nil {
		return Usage{}, err
	}
	return usage, nil
}

func unavailable(err error) error { return fmt.Errorf("%w: %w", ErrUnavailable, err) }

// trustedPath checks every ancestor to avoid symlink and writable-parent swaps.
func trustedPath(path string, directory bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("authority paths must be absolute and clean")
	}
	for current := path; ; current = filepath.Dir(current) {
		var s unix.Stat_t
		if err := unix.Lstat(current, &s); err != nil {
			return err
		}
		if s.Uid != 0 || s.Mode&0022 != 0 || s.Mode&unix.S_IFMT == unix.S_IFLNK {
			return fmt.Errorf("untrusted authority path %s", current)
		}
		if current != path || directory {
			if s.Mode&unix.S_IFMT != unix.S_IFDIR {
				return fmt.Errorf("authority ancestor is not a directory")
			}
		}
		if current == "/" {
			return nil
		}
	}
}

func privateDirectory(path string, uid int) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return fmt.Errorf("invalid private directory")
	}
	if err := trustedPath(filepath.Dir(path), true); err != nil {
		return err
	}
	var s unix.Stat_t
	if err := unix.Lstat(path, &s); err != nil {
		return err
	}
	if s.Mode&unix.S_IFMT != unix.S_IFDIR || int(s.Uid) != uid || s.Mode&0077 != 0 {
		return fmt.Errorf("directory must be private to the approved worker UID")
	}
	return nil
}

// Load validates independent authority, finite distinct slots, helper ownership,
// and the kernel confinement API. No privileged state is changed by admission.
func Load(path string) (*Manager, error) {
	if path == "" {
		return nil, unavailable(fmt.Errorf("no administrator quota configuration"))
	}
	if err := trustedPath(path, false); err != nil {
		return nil, unavailable(err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, unavailable(err)
	}
	defer f.Close()
	var config Config
	d := json.NewDecoder(io.LimitReader(f, 65537))
	d.DisallowUnknownFields()
	if err := d.Decode(&config); err != nil {
		return nil, unavailable(err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return nil, unavailable(fmt.Errorf("trailing authority configuration"))
	}
	if config.Version != 1 || config.WorkerUID == 0 || config.WorkerUID != os.Geteuid() || len(config.Slots) == 0 || len(config.Slots) > 128 {
		return nil, unavailable(fmt.Errorf("invalid authority version, worker UID, or finite slot count"))
	}
	if err := privateDirectory(config.StateDir, config.WorkerUID); err != nil {
		return nil, unavailable(err)
	}
	if err := trustedPath(config.Helper, false); err != nil {
		return nil, unavailable(err)
	}
	info, err := os.Stat(config.Helper)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&0111 == 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return nil, unavailable(fmt.Errorf("helper must be a trusted ordinary executable"))
	}
	paths, projects := map[string]bool{}, map[uint32]bool{}
	for _, slot := range config.Slots {
		if slot.Project == 0 || slot.Generation == 0 || projects[slot.Project] || paths[slot.Path] || slot.Path == config.StateDir {
			return nil, unavailable(fmt.Errorf("duplicate or invalid project slot"))
		}
		if err := privateDirectory(slot.Path, config.WorkerUID); err != nil {
			return nil, unavailable(err)
		}
		projects[slot.Project], paths[slot.Path] = true, true
	}
	if _, err := ConfinementABI(); err != nil {
		return nil, unavailable(err)
	}
	manager := &Manager{config: config, kernel: helperInspector{config.Helper, path}, configPath: path}
	if err := manager.CheckBwrap(); err != nil {
		return nil, unavailable(err)
	}
	if err := manager.CheckGit(); err != nil {
		return nil, unavailable(err)
	}
	return manager, nil
}

// ProbeLocal and InspectLocal are helper-only operations. They expose kernel
// reads, never quota writes or a caller-controlled syscall operation.
func (m *Manager) ProbeLocal() error {
	for _, slot := range m.config.Slots {
		if _, err := (kernelInspector{}).Inspect(slot); err != nil {
			return unavailable(err)
		}
	}
	return nil
}
func (m *Manager) InspectLocal(path string) (Usage, error) {
	for _, slot := range m.config.Slots {
		if slot.Path == path {
			return (kernelInspector{}).Inspect(slot)
		}
	}
	return Usage{}, unavailable(fmt.Errorf("unapproved slot"))
}

func (m *Manager) Probe() error {
	for _, slot := range m.config.Slots {
		if _, err := m.kernel.Inspect(slot); err != nil {
			return unavailable(err)
		}
	}
	return nil
}

func (m *Manager) Allows(path string) bool {
	for _, slot := range m.config.Slots {
		if slot.Path == path {
			return true
		}
	}
	return false
}
func (m *Manager) CheckGit() error { return trustedPath("/usr/bin/git", false) }
func (m *Manager) CheckBwrap() error {
	if err := trustedPath("/usr/bin/bwrap", false); err != nil {
		return err
	}
	info, err := os.Stat("/usr/bin/bwrap")
	if err != nil {
		return err
	}
	if info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return fmt.Errorf("bwrap must be unprivileged")
	}
	return nil
}

type journal struct {
	Version    int       `json:"version"`
	Project    uint32    `json:"project"`
	Path       string    `json:"path"`
	Status     string    `json:"status"`
	Updated    time.Time `json:"updated"`
	Generation uint64    `json:"generation"`
	RootInode  uint64    `json:"rootInode"`
}
type Lease struct {
	manager  *Manager
	slot     Slot
	file     *os.File
	mu       sync.Mutex
	released bool
}

func (l *Lease) Path() string       { return l.slot.Path }
func (l *Lease) Helper() string     { return l.manager.config.Helper }
func (l *Lease) ConfigPath() string { return l.manager.configPath }

func writeJournal(file *os.File, slot Slot, status string) error {
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	inode, err := rootInode(slot.Path)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(file).Encode(journal{Version: 1, Project: slot.Project, Path: slot.Path, Status: status, Updated: time.Now().UTC(), Generation: slot.Generation, RootInode: inode}); err != nil {
		return err
	}
	return file.Sync()
}

func rootInode(path string) (uint64, error) {
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return 0, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return 0, fmt.Errorf("slot root changed type")
	}
	return stat.Ino, nil
}

func (m *Manager) clean(slot Slot) error {
	entries, err := os.ReadDir(slot.Path)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return fmt.Errorf("slot contains residual entries")
	}
	baseline, err := rootBaseline(slot.Path)
	if err != nil {
		return err
	}
	usage, err := m.kernel.Inspect(slot)
	if err != nil {
		return err
	}
	if usage != baseline {
		return fmt.Errorf("slot retains allocations or open unlinked inodes: usage=%+v root=%+v", usage, baseline)
	}
	return nil
}

// Acquire holds a cross-process flock until final cleanup. A crash, corrupt
// journal, changed quota, dirty slot, or retained unlinked allocation quarantines
// the slot. It never silently recycles an interrupted execution.
func (m *Manager) Acquire() (*Lease, error) {
	for _, slot := range m.config.Slots {
		name := filepath.Join(m.config.StateDir, strconv.FormatUint(uint64(slot.Project), 10)+".lease")
		fd, err := unix.Open(name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		created := err == nil
		if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Open(name, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if err != nil {
			continue
		}
		file := os.NewFile(uintptr(fd), name)
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || int(stat.Uid) != m.config.WorkerUID || stat.Mode&0077 != 0 || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
			file.Close()
			continue
		}
		usable := created
		if !created {
			var record journal
			decoder := json.NewDecoder(io.LimitReader(file, 4096))
			inode, inodeErr := rootInode(slot.Path)
			valid := decoder.Decode(&record) == nil && record.Version == 1 && record.Project == slot.Project && record.Path == slot.Path && inodeErr == nil
			usable = valid && record.Status == "clean" && record.Generation == slot.Generation && record.RootInode == inode
			// Recovery requires TWO independent administrator changes: a newer
			// root-owned authorized generation and a replaced slot root inode.
			// Old Landlock domains cannot write the replacement inode. The clean
			// kernel baseline below also rejects old mounted/open-unlinked roots.
			if valid && slot.Generation > record.Generation && record.RootInode != 0 && record.RootInode != inode {
				usable = true
			}
		}
		if !usable || m.clean(slot) != nil {
			_ = writeJournal(file, slot, "quarantined")
			file.Close()
			continue
		}
		if err := writeJournal(file, slot, "leased"); err != nil {
			file.Close()
			continue
		}
		// Persist creation before exposing a lease to a process that can write.
		dir, err := os.Open(m.config.StateDir)
		if err == nil {
			err = dir.Sync()
			dir.Close()
		}
		if err != nil {
			_ = writeJournal(file, slot, "quarantined")
			file.Close()
			continue
		}
		return &Lease{manager: m, slot: slot, file: file}, nil
	}
	return nil, unavailable(fmt.Errorf("all finite slots are busy, dirty, or quarantined"))
}

// Release removes only descendants of the retained slot. An open unlinked file
// remains charged by XFS and prevents the clean journal transition and reuse.
func (l *Lease) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	defer l.file.Close()
	root, err := os.OpenRoot(l.slot.Path)
	if err == nil {
		entries, readErr := os.ReadDir(l.slot.Path)
		err = readErr
		for _, entry := range entries {
			if removeErr := root.RemoveAll(entry.Name()); removeErr != nil {
				err = errors.Join(err, removeErr)
			}
		}
		root.Close()
	}
	if err == nil {
		err = l.manager.clean(l.slot)
	}
	status := "clean"
	if err != nil {
		status = "quarantined"
	}
	return errors.Join(err, writeJournal(l.file, l.slot, status))
}
