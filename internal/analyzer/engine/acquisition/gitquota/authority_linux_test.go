//go:build linux

package gitquota

import (
	"bufio"
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAuthorityExpirationIsDurableAndCannotRecycleOnTimer(t *testing.T) {
	m, _ := syntheticManager(t, 1)
	m.config.LeaseSeconds = 1
	a := newAuthority(m)
	grant, err := a.acquire()
	require.NoError(t, err)
	require.NotEmpty(t, grant.Token)
	require.WithinDuration(t, time.Now().Add(time.Second), grant.Deadline, time.Second)
	require.Eventually(t, func() bool { a.mu.Lock(); defer a.mu.Unlock(); return a.leases[grant.Token].expired }, 3*time.Second, 10*time.Millisecond)
	_, err = a.acquire()
	require.ErrorIs(t, err, ErrUnavailable)
	data, err := os.ReadFile(filepath.Join(m.config.StateDir, "100.lease"))
	require.NoError(t, err)
	var record journal
	require.NoError(t, json.Unmarshal(data, &record))
	require.Equal(t, "quarantined", record.Status)
	require.True(t, grant.Deadline.Equal(record.Deadline))
	require.Equal(t, grant.Token, record.LeaseID)
	require.Equal(t, grant.Slot.Generation, record.Generation)
}

func TestAuthorityRejectsExpiredWrongGenerationAndUnknownTokens(t *testing.T) {
	m, _ := syntheticManager(t, 1)
	m.config.LeaseSeconds = 30
	a := newAuthority(m)
	grant, err := a.acquire()
	require.NoError(t, err)
	for _, req := range []authorityRequest{
		{Token: "forged", Slot: grant.Slot},
		{Token: grant.Token, Slot: Slot{Path: grant.Slot.Path, Project: grant.Slot.Project, Generation: grant.Slot.Generation + 1}},
	} {
		a.mu.Lock()
		_, err := a.lookup(req)
		a.mu.Unlock()
		require.Error(t, err)
	}
	require.NoError(t, a.release(authorityRequest{Token: grant.Token, Slot: grant.Slot}))
	next, err := a.acquire()
	require.NoError(t, err, "normal verified cleanup permits a new independently identified lease")
	require.NotEqual(t, grant.Token, next.Token)
	require.NoError(t, a.release(authorityRequest{Token: next.Token, Slot: next.Slot}))
}

func TestLoadRejectsMissingProtectedExpirationAuthority(t *testing.T) {
	_, err := Load("")
	require.ErrorIs(t, err, ErrUnavailable)
}

// Actual unprivileged process/socket tests; no root authority or XFS assertion.
func TestActualSupervisorDeadlineKillsPrivateNamespaceWriters(t *testing.T) {
	slot := t.TempDir()
	args := NamespaceArgs("unused", "unused", slot, nil)
	for i, arg := range args {
		if arg == "--" {
			args = append(args[:i+1], "/bin/sh", "-c", `while :; do printf x >> "$1/heartbeat"; sleep 0.02; done`, "fixture", slot)
			break
		}
	}
	cmd := exec.Command("/usr/bin/bwrap", args[1:]...)
	cmd.Stderr = os.Stderr
	err := superviseCommand(cmd, make(chan struct{}), time.Now().Add(400*time.Millisecond))
	require.ErrorIs(t, err, ErrUnavailable)
	data, readErr := os.ReadFile(filepath.Join(slot, "heartbeat"))
	require.NoError(t, readErr, "namespace fixture must really write before deadline")
	require.NotEmpty(t, data)
	time.Sleep(100 * time.Millisecond)
	after, readErr := os.ReadFile(filepath.Join(slot, "heartbeat"))
	require.NoError(t, readErr)
	require.Equal(t, data, after, "private namespace descendants cannot continue writing after deadline")
	require.True(t, cmd.ProcessState.Sys().(syscall.WaitStatus).Signaled())
}

func TestActualSupervisorStopsWhenIndependentDaemonIsKilled(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "fixture.sock")
	daemon := exec.Command(os.Args[0], "-test.run=^TestAuthoritySocketFixtureChild$")
	daemon.Env = append(os.Environ(), "PLATEN_AUTHORITY_SOCKET_FIXTURE="+socket)
	daemonFD := -1
	daemon.SysProcAttr = &syscall.SysProcAttr{PidFD: &daemonFD}
	stdout, err := daemon.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, daemon.Start())
	defer unix.Close(daemonFD)
	defer func() { _ = unix.PidfdSendSignal(daemonFD, unix.SIGKILL, nil, 0); _ = daemon.Wait() }()
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ready\n", ready)
	conn, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	require.NoError(t, err)
	defer conn.Close()
	cred, peerFD, err := peerIdentity(conn)
	require.NoError(t, err, "kernel SO_PEERPIDFD must provide stable peer identity")
	defer unix.Close(peerFD)
	require.EqualValues(t, os.Geteuid(), cred.Uid)
	lost := make(chan struct{})
	go func() { var one [1]byte; _, _ = conn.Read(one[:]); close(lost) }()
	cmd := exec.Command("/bin/sleep", "30")
	go func() { time.Sleep(100 * time.Millisecond); _ = unix.PidfdSendSignal(daemonFD, unix.SIGKILL, nil, 0) }()
	started := time.Now()
	err = superviseCommand(cmd, lost, time.Now().Add(5*time.Second))
	require.ErrorIs(t, err, ErrUnavailable)
	require.Less(t, time.Since(started), 3*time.Second, "daemon death must stop writer before its own deadline")
	require.True(t, cmd.ProcessState.Sys().(syscall.WaitStatus).Signaled())
}

func TestAuthoritySocketFixtureChild(t *testing.T) {
	path := os.Getenv("PLATEN_AUTHORITY_SOCKET_FIXTURE")
	if path == "" {
		return
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		os.Exit(20)
	}
	defer listener.Close()
	fmt.Println("ready")
	conn, err := listener.AcceptUnix()
	if err != nil {
		os.Exit(21)
	}
	defer conn.Close()
	_, _ = io.Copy(io.Discard, conn)
}

func TestAuthorityRestartNeverResumesInterruptedLease(t *testing.T) {
	m, _ := syntheticManager(t, 1)
	m.config.LeaseSeconds = 30
	before := newAuthority(m)
	grant, err := before.acquire()
	require.NoError(t, err)
	before.mu.Lock()
	lease := before.leases[grant.Token]
	lease.timer.Stop()
	require.NoError(t, lease.lease.file.Close()) // model service death, never Release
	before.mu.Unlock()
	after := newAuthority(m)
	_, err = after.acquire()
	require.ErrorIs(t, err, ErrUnavailable)
	data, err := os.ReadFile(filepath.Join(m.config.StateDir, "100.lease"))
	require.NoError(t, err)
	var record journal
	require.NoError(t, json.Unmarshal(data, &record))
	require.Equal(t, "quarantined", record.Status)
	require.Equal(t, grant.Token, record.LeaseID)
	require.True(t, grant.Deadline.Equal(record.Deadline))
}
