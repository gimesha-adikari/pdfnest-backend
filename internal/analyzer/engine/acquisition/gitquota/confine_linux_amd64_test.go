//go:build linux && amd64

package gitquota

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestActualKernelConfinement(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		t.Run(fmt.Sprintf("readonly_namespace_%t", wrapped), func(t *testing.T) { actualConfinement(t, wrapped) })
	}
}

func actualConfinement(t *testing.T, wrapped bool) {
	abi, err := ConfinementABI()
	if err != nil {
		t.Skipf("BLOCKED kernel confinement: %v", err)
	}
	t.Logf("actual Landlock ABI %d", abi)
	inside, outside := t.TempDir(), t.TempDir()
	source := t.TempDir()
	for _, args := range [][]string{{"init", source}, {"-C", source, "add", "script.sh"}, {"-C", source, "-c", "user.name=fixture", "-c", "user.email=fixture@example.test", "-c", "core.hooksPath=/dev/null", "commit", "-m", "fixture"}} {
		if args[0] == "-C" && args[2] == "add" {
			require.NoError(t, os.WriteFile(filepath.Join(source, "script.sh"), []byte("#!/bin/sh\nexit 0\n"), 0700))
		}
		out, err := exec.Command("/usr/bin/git", args...).CombinedOutput()
		require.NoError(t, err, "%s", out)
	}
	protected := filepath.Join(outside, "protected")
	require.NoError(t, os.WriteFile(protected, []byte("preserved"), 0600))
	fd, err := os.OpenFile(protected, os.O_WRONLY, 0)
	require.NoError(t, err)
	defer fd.Close()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			conn.Close()
		}
		accepted <- err
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestConfinementChild$")
	cmd.Env = append(os.Environ(), "PLATEN_CONFINEMENT_CHILD=prepare", "PLATEN_SLOT="+inside, "PLATEN_OUTSIDE="+outside, "PLATEN_PROXY="+listener.Addr().String(), "PLATEN_FIXTURE_REPO="+source)
	if wrapped {
		cmd.Env = append(cmd.Env, "PLATEN_NAMESPACE_WRAPPED=1")
		for i, value := range cmd.Env {
			if value == "PLATEN_CONFINEMENT_CHILD=prepare" {
				cmd.Env[i] = "PLATEN_CONFINEMENT_CHILD=namespace"
			}
		}
	}
	cmd.ExtraFiles = []*os.File{fd}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.Contains(t, string(output), "kernel confinement checks passed")
	data, err := os.ReadFile(protected)
	require.NoError(t, err)
	require.Equal(t, "preserved", string(data))
	if wrapped {
		record, err := os.ReadFile(filepath.Join(inside, "descendant.stat"))
		require.NoError(t, err)
		pid, err := strconv.Atoi(strings.Fields(string(record))[0])
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			return os.IsNotExist(err) || strings.Contains(string(data), ") Z ")
		}, time.Second, 10*time.Millisecond, "namespace descendants must be gone before slot reuse")
	}
	select {
	case err := <-accepted:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal("confinement blocked guarded loopback transport")
	}
}

func TestConfinementChild(t *testing.T) {
	stage := os.Getenv("PLATEN_CONFINEMENT_CHILD")
	if stage == "" {
		return
	}
	if stage == "namespace" {
		if err := DropPrivileges(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(25)
		}
		wrapper := NamespaceArgs(os.Args[0], "unused", os.Getenv("PLATEN_SLOT"), nil)
		for i, arg := range wrapper {
			if arg == "--" {
				wrapper = append(wrapper[:i+1], os.Args[0], "-test.run=^TestConfinementChild$")
				break
			}
		}
		os.Setenv("PLATEN_CONFINEMENT_CHILD", "prepare")
		if err := syscall.Exec("/usr/bin/bwrap", wrapper, os.Environ()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(26)
		}
		return
	}
	if stage == "prepare" {
		if os.Getenv("PLATEN_NAMESPACE_WRAPPED") == "1" {
			if err := CheckNamespace(os.Getenv("PLATEN_SLOT")); err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(24)
			}
		}
		if err := Confine(os.Getenv("PLATEN_SLOT")); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(21)
		}
		os.Setenv("PLATEN_CONFINEMENT_CHILD", "payload")
		env := os.Environ()
		if err := syscall.Exec(os.Args[0], []string{os.Args[0], "-test.run=^TestConfinementChild$"}, env); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(22)
		}
		return
	}
	inside, outside := os.Getenv("PLATEN_SLOT"), os.Getenv("PLATEN_OUTSIDE")
	check := func(ok bool, what string) {
		if !ok {
			fmt.Fprintln(os.Stderr, "confinement failure:", what)
			os.Exit(23)
		}
	}
	null, err := os.OpenFile("/dev/null", os.O_RDWR, 0)
	if err != nil {
		fmt.Fprintln(os.Stderr, "direct null open", err)
	} else {
		null.Close()
	}
	check(os.WriteFile(filepath.Join(inside, "allowed"), []byte("charged"), 0600) == nil, "inside write")
	check(os.WriteFile(filepath.Join(outside, "protected"), []byte("bad"), 0600) != nil, "outside truncate")
	check(os.Mkdir(filepath.Join(outside, "escape"), 0700) != nil, "outside mkdir")
	check(os.Symlink(filepath.Join(outside, "protected"), filepath.Join(inside, "link")) == nil, "inside symlink create")
	check(os.WriteFile(filepath.Join(inside, "link"), []byte("bad"), 0600) != nil, "symlink escape")
	check(os.Link(filepath.Join(outside, "protected"), filepath.Join(inside, "hardlink")) != nil, "outside hardlink")
	check(os.Rename(filepath.Join(inside, "allowed"), filepath.Join(outside, "moved")) != nil, "rename escape")
	if os.Getenv("PLATEN_NAMESPACE_WRAPPED") == "1" {
		check(os.Chmod(filepath.Join(outside, "protected"), 0640) != nil, "read-only namespace rejects outside metadata mutation")
	}
	_, err = unix.Write(3, []byte("bad"))
	check(err == unix.EBADF, "inherited writable FD closed on exec")
	_, err = unix.IoctlGetInt(0, unix.TCGETS)
	check(err == unix.EPERM, "ioctl denied")
	check(unix.Unshare(unix.CLONE_NEWUSER) == unix.EPERM, "namespace escape denied")
	_, err = unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	check(err == unix.EPERM, "pathname Unix FD import denied")
	_, _, errno := unix.RawSyscall(unix.SYS_CLONE, unix.CLONE_NEWUSER|uintptr(unix.SIGCHLD), 0, 0)
	check(errno == unix.EPERM, "clone user namespace denied")
	conn, err := net.DialTimeout("tcp", os.Getenv("PLATEN_PROXY"), time.Second)
	check(err == nil, "guarded loopback proxy remains accessible")
	conn.Close()
	git := exec.Command("/usr/bin/git", "-c", "core.hooksPath=/dev/null", "-c", "init.templateDir=", "clone", "--depth", "1", "--", "file://"+os.Getenv("PLATEN_FIXTURE_REPO"), filepath.Join(inside, "repository"))
	git.Env = append(os.Environ(), "PATH=/usr/bin:/bin", "HOME="+inside, "TMPDIR="+inside, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	output, err := git.CombinedOutput()
	if err != nil {
		fmt.Fprintln(os.Stderr, err, string(output))
		check(false, "real Git fixture checkout")
	}
	if os.Getenv("PLATEN_NAMESPACE_WRAPPED") == "1" {
		descendant := exec.Command("/bin/sh", "-c", `IFS= read -r record < /proc/self/stat; printf '%s\n' "$record" > "$PLATEN_SLOT/descendant.stat"; exec /bin/sleep 30`)
		descendant.Stdout, descendant.Stderr = os.Stdout, os.Stderr
		if err := descendant.Start(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			check(false, "start surviving descendant fixture")
		}
		deadline := time.Now().Add(time.Second)
		for {
			_, err := os.Stat(filepath.Join(inside, "descendant.stat"))
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				check(false, "descendant fixture marker")
			}
			time.Sleep(time.Millisecond)
		}
	}
	fmt.Println("kernel confinement checks passed")
	os.Exit(0)
}
