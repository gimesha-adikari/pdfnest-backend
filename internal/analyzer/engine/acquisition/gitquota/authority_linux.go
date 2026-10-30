//go:build linux

package gitquota

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// The authority runs independently as an administrator-started root process.
// Worker-owned journals and caller clocks never authorize a lease or renewal.
// There is deliberately no renewal API: every grant has a finite deadline.
type authorityRequest struct {
	Operation string `json:"operation"`
	Slot      Slot   `json:"slot"`
	Token     string `json:"token"`
}
type authorityResponse struct {
	Slot     Slot      `json:"slot"`
	Token    string    `json:"token"`
	Deadline time.Time `json:"deadline"`
	Error    string    `json:"error,omitempty"`
}
type authorizedLease struct {
	lease   *Lease
	writers map[int]struct{} // stable peer pidfds, never numeric PIDs or PGIDs
	expired bool
	timer   *time.Timer
}
type authority struct {
	manager *Manager
	mu      sync.Mutex
	leases  map[string]*authorizedLease
}

func newAuthority(m *Manager) *authority {
	return &authority{manager: m, leases: map[string]*authorizedLease{}}
}

func (a *authority) acquire() (authorityResponse, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	lease, err := a.manager.acquireLocal()
	if err != nil {
		return authorityResponse{}, err
	}
	record := &authorizedLease{lease: lease, writers: map[int]struct{}{}}
	a.leases[lease.token] = record
	record.timer = time.AfterFunc(time.Until(lease.deadline), func() { a.expire(lease.token) })
	return authorityResponse{Slot: lease.slot, Token: lease.token, Deadline: lease.deadline}, nil
}
func (a *authority) lookup(req authorityRequest) (*authorizedLease, error) {
	record := a.leases[req.Token]
	if record == nil || record.expired || record.lease.released || record.lease.slot != req.Slot || !time.Now().Before(record.lease.deadline) {
		return nil, unavailable(fmt.Errorf("unknown, mismatched, released, or expired lease"))
	}
	return record, nil
}

// killAndDrain uses a kernel-held process identity and waits for exit. No slot
// is cleaned/reused while a registered supervisor remains alive or unknown.
func killAndDrain(fd int) error {
	if err := unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0); err != nil && !errors.Is(err, unix.ESRCH) {
		return err
	}
	poll := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
	for {
		count, err := unix.Poll(poll, 3000)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return err
		}
		if count == 0 || poll[0].Revents&unix.POLLIN == 0 {
			return fmt.Errorf("writer drain not verified")
		}
		return nil
	}
}
func (a *authority) stopWriters(record *authorizedLease) error {
	var err error
	for fd := range record.writers {
		err = errors.Join(err, killAndDrain(fd))
	}
	return err
}
func (a *authority) quarantine(record *authorizedLease) error {
	record.expired = true
	record.timer.Stop()
	err := a.stopWriters(record)
	if err == nil {
		// Supervisor death closes its private PID namespace. The quota accounting
		// check in Release rejects incomplete descendant drain/open unlinked files.
		err = record.lease.cleanup("quarantined")
	}
	if !record.lease.released {
		err = errors.Join(err, writeDeadlineJournal(record.lease.file, record.lease.slot, "quarantined", record.lease.token, record.lease.deadline))
		record.lease.released = true
		err = errors.Join(err, record.lease.file.Close())
	}
	return err
}
func (a *authority) expire(token string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	record := a.leases[token]
	if record == nil || record.expired || record.lease.released {
		return
	}
	// Keep a durable quarantine even when cleanup succeeds. Expiration alone is
	// never sufficient recovery authority, including after an authority restart.
	_ = a.quarantine(record)
}
func (a *authority) release(req authorityRequest) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	record, err := a.lookup(req)
	if err != nil {
		return err
	}
	record.timer.Stop()
	if len(record.writers) != 0 {
		return errors.Join(unavailable(fmt.Errorf("release with registered writers quarantined")), a.quarantine(record))
	}
	err = record.lease.Release()
	delete(a.leases, req.Token)
	return err
}

func peerIdentity(conn *net.UnixConn) (*unix.Ucred, int, error) {
	raw, err := conn.SyscallConn()
	if err != nil {
		return nil, -1, err
	}
	var cred *unix.Ucred
	pidfd := -1
	var peerErr error
	err = raw.Control(func(fd uintptr) {
		cred, peerErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
		if peerErr == nil {
			pidfd, peerErr = unix.GetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_PEERPIDFD)
		}
	})
	return cred, pidfd, errors.Join(err, peerErr)
}
func trustedPathSocket(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("invalid authority socket")
	}
	if err := trustedPath(filepath.Dir(path), true); err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Lstat(path, &stat); err != nil {
		return err
	}
	if stat.Uid != 0 || stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		return fmt.Errorf("authority socket must be root-owned")
	}
	return nil
}
func (m *Manager) authorityConnect() (*net.UnixConn, error) {
	if m.config.AuthoritySocket == "" {
		return nil, unavailable(fmt.Errorf("no protected expiration authority"))
	}
	if err := trustedPathSocket(m.config.AuthoritySocket); err != nil {
		return nil, unavailable(err)
	}
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: m.config.AuthoritySocket, Net: "unix"})
	if err != nil {
		return nil, unavailable(err)
	}
	cred, fd, err := peerIdentity(conn)
	if fd >= 0 {
		unix.Close(fd)
	}
	if err != nil || cred == nil || cred.Uid != 0 {
		conn.Close()
		return nil, unavailable(fmt.Errorf("expiration authority must be an independent root peer: %v", err))
	}
	return conn, nil
}
func exchange(conn *net.UnixConn, req authorityRequest) (authorityResponse, error) {
	var reply authorityResponse
	if err := conn.SetDeadline(time.Now().Add(4 * time.Second)); err != nil {
		return reply, err
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return reply, err
	}
	if err := json.NewDecoder(io.LimitReader(conn, 4096)).Decode(&reply); err != nil {
		return reply, err
	}
	if reply.Error != "" {
		return reply, unavailable(errors.New(reply.Error))
	}
	return reply, nil
}
func (m *Manager) authorityCall(req authorityRequest) (authorityResponse, error) {
	conn, err := m.authorityConnect()
	if err != nil {
		return authorityResponse{}, err
	}
	defer conn.Close()
	return exchange(conn, req)
}
func (m *Manager) acquireRemote() (*Lease, error) {
	reply, err := m.authorityCall(authorityRequest{Operation: "acquire"})
	if err != nil {
		return nil, err
	}
	approved := false
	for _, slot := range m.config.Slots {
		if slot == reply.Slot {
			approved = true
		}
	}
	if !approved || len(reply.Token) != 64 || !time.Now().Before(reply.Deadline) || time.Until(reply.Deadline) > time.Duration(m.config.LeaseSeconds)*time.Second {
		return nil, unavailable(fmt.Errorf("invalid authority grant"))
	}
	return &Lease{manager: m, slot: reply.Slot, token: reply.Token, deadline: reply.Deadline, remote: true}, nil
}
func (l *Lease) releaseRemote() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.released {
		return nil
	}
	l.released = true
	_, err := l.manager.authorityCall(authorityRequest{Operation: "release", Slot: l.slot, Token: l.token})
	return err
}

// ServeAuthority does not daemonize or provision anything. An administrator must
// explicitly run this under an existing service supervisor with protected config,
// root-owned private StateDir and an already-provisioned quota filesystem.
func ServeAuthority(ctx context.Context, configPath string) error {
	manager, err := load(configPath, true)
	if err != nil {
		return err
	}
	if err := manager.ProbeLocal(); err != nil {
		return err
	}
	// Never remove an existing socket, even if it appears stale: another live
	// authority may own it. Service restart cleanup is administrator-controlled.
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: manager.config.AuthoritySocket, Net: "unix"})
	if err != nil {
		return err
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	// Connection credentials authorize the configured UID; the socket is writable
	// only to permit that UID without changing host groups or credentials.
	if err := os.Chmod(manager.config.AuthoritySocket, 0622); err != nil {
		return err
	}
	manager.kernel = kernelInspector{}
	a := newAuthority(manager)
	go func() { <-ctx.Done(); listener.Close() }()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		for _, record := range a.leases {
			if !record.lease.released {
				_ = a.quarantine(record)
			}
		}
	}()
	for {
		conn, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go a.handle(conn)
	}
}
func (a *authority) handle(conn *net.UnixConn) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(4 * time.Second))
	cred, pidfd, err := peerIdentity(conn)
	if pidfd >= 0 {
		defer unix.Close(pidfd)
	}
	if err != nil || cred == nil || int(cred.Uid) != a.manager.config.WorkerUID {
		return
	}
	var req authorityRequest
	decoder := json.NewDecoder(io.LimitReader(conn, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return
	}
	var reply authorityResponse
	switch req.Operation {
	case "probe":
		err = a.manager.ProbeLocal()
	case "acquire":
		reply, err = a.acquire()
	case "release":
		err = a.release(req)
	case "watch":
		a.mu.Lock()
		record, lookupErr := a.lookup(req)
		err = lookupErr
		if err == nil {
			// Re-read hard limits before permitting every Git invocation.
			_, err = a.manager.kernel.Inspect(req.Slot)
			if err != nil {
				err = errors.Join(err, a.quarantine(record))
			}
		}
		if err == nil {
			record.writers[pidfd] = struct{}{}
			reply = authorityResponse{Slot: req.Slot, Token: req.Token, Deadline: record.lease.deadline}
		}
		a.mu.Unlock()
		if err == nil {
			var completed bool
			if err := json.NewEncoder(conn).Encode(reply); err == nil {
				_ = conn.SetDeadline(time.Time{})
				var one [1]byte
				n, readErr := conn.Read(one[:])
				completed = readErr == nil && n == 1 && one[0] == 'D'
			}
			a.mu.Lock()
			completed = completed && !record.expired && time.Now().Before(record.lease.deadline)
			if !completed && !record.lease.released {
				_ = a.quarantine(record)
			}
			delete(record.writers, pidfd)
			a.mu.Unlock()
			if completed {
				// D is emitted only after the fixed supervisor reaps bwrap;
				// acknowledge removal before the helper closes its connection.
				_ = conn.SetWriteDeadline(time.Now().Add(4 * time.Second))
				_, _ = conn.Write([]byte{'A'})
			}
			return
		}
	default:
		err = fmt.Errorf("unknown authority operation")
	}
	if err != nil {
		reply.Error = err.Error()
	}
	_ = json.NewEncoder(conn).Encode(reply)
}

// RunSupervised holds the authority connection for the complete bwrap lifetime.
// Authority loss kills bwrap through its stable pidfd; supervisor death triggers
// bwrap's --die-with-parent and the private PID namespace's descendant teardown.
// The helper stays outside Git's Landlock domain and drops all capabilities.
func (m *Manager) RunSupervised(slotPath, token string, gitArgs []string) error {
	var slot Slot
	for _, candidate := range m.config.Slots {
		if candidate.Path == slotPath {
			slot = candidate
		}
	}
	if slot.Project == 0 {
		return unavailable(fmt.Errorf("unapproved slot"))
	}
	conn, err := m.authorityConnect()
	if err != nil {
		return err
	}
	defer conn.Close()
	reply, err := exchange(conn, authorityRequest{Operation: "watch", Slot: slot, Token: token})
	if err != nil {
		return err
	}
	if reply.Slot != slot || reply.Token != token || !time.Now().Before(reply.Deadline) {
		return unavailable(fmt.Errorf("invalid writer authorization"))
	}
	_ = conn.SetDeadline(time.Time{})
	lost := make(chan struct{})
	authorityReply := make(chan byte, 1)
	go func() {
		var one [1]byte
		n, err := conn.Read(one[:])
		if err != nil || n != 1 {
			one[0] = 0
		}
		authorityReply <- one[0]
		close(lost)
	}()
	helper, err := os.Executable()
	if err != nil {
		return err
	}
	if err := m.CheckBwrap(); err != nil {
		return err
	}
	if err := DropPrivileges(); err != nil {
		return err
	}
	cmd := exec.Command("/usr/bin/bwrap", NamespaceArgs(helper, m.configPath, slotPath, gitArgs)[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	result := superviseCommand(cmd, lost, reply.Deadline)
	// A graceful completion handshake prevents successful Git helpers from
	// being mistaken for vanished writers during their own process exit.
	_ = conn.SetWriteDeadline(time.Now().Add(4 * time.Second))
	if _, err := conn.Write([]byte{'D'}); err != nil {
		return errors.Join(result, unavailable(err))
	}
	select {
	case ack := <-authorityReply:
		if ack != 'A' {
			return errors.Join(result, unavailable(fmt.Errorf("writer completion was not acknowledged")))
		}
		return result
	case <-time.After(4 * time.Second):
		return errors.Join(result, unavailable(fmt.Errorf("writer completion acknowledgement timed out")))
	}
}

// superviseCommand is also exercised with unprivileged local fixtures. It
// proves process termination only, separately from kernel storage enforcement.
func superviseCommand(cmd *exec.Cmd, lost <-chan struct{}, deadline time.Time) error {
	// Pdeathsig is tied to the spawning thread; keep it alive through child Wait.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	pidfd := -1
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, PidFD: &pidfd}
	select {
	case <-lost:
		return unavailable(fmt.Errorf("expiration authority disconnected before spawn"))
	default:
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer unix.Close(pidfd)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(time.Until(deadline))
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-lost:
		killErr := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
		return errors.Join(unavailable(fmt.Errorf("expiration authority disconnected")), killErr, <-done)
	case <-timer.C:
		killErr := unix.PidfdSendSignal(pidfd, unix.SIGKILL, nil, 0)
		return errors.Join(unavailable(fmt.Errorf("lease expired")), killErr, <-done)
	}
}
