//go:build linux && amd64

package gitquota

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

// ABI 6 supplies filesystem REFER/TRUNCATE and signal/abstract-socket scoping.
// Older kernels fail closed instead of silently losing a confinement right.
func ConfinementABI() (int, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, fmt.Errorf("Landlock unavailable: %w", errno)
	}
	if version < 6 {
		return int(version), fmt.Errorf("Landlock ABI 6 or later required")
	}
	return int(version), nil
}

// Confine locks the current OS thread for the subsequent Exec. It is intended
// only in the single-purpose helper, never inside the long-lived backend.
// Read and TCP access remain available for the existing guarded HTTPS proxy.
func Confine(slot string) error {
	if _, err := ConfinementABI(); err != nil {
		return err
	}
	if !filepath.IsAbs(slot) || filepath.Clean(slot) != slot || slot == "/" {
		return fmt.Errorf("invalid confinement root")
	}
	if os.Geteuid() == 0 {
		return fmt.Errorf("Git confinement requires a non-root worker UID")
	}
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no-new-privileges: %w", err)
	}
	// Remove effective/permitted/inheritable and ambient capabilities before Git.
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return fmt.Errorf("clear ambient capabilities: %w", err)
	}
	capHeader := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	capData := [2]unix.CapUserData{}
	if err := unix.Capset(&capHeader, &capData[0]); err != nil {
		return fmt.Errorf("clear process capabilities: %w", err)
	}
	fd, err := unix.Open(slot, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	const handled = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_SYM | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	// Device/FIFO/socket creation and device ioctl are never granted, even inside
	// a charged project. The only persistent writable files are beneath the slot.
	const allowed = handled &^ (unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV)
	attr := unix.LandlockRulesetAttr{Access_fs: handled, Scoped: unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET | unix.LANDLOCK_SCOPE_SIGNAL}
	ruleset, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("create confinement: %w", errno)
	}
	defer unix.Close(int(ruleset))
	path := unix.LandlockPathBeneathAttr{Allowed_access: allowed, Parent_fd: int32(fd)}
	_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&path)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("add slot rule: %w", errno)
	}
	// /dev/null is an unallocated sink used by Git subprocess plumbing. It is
	// the only writable path outside the charged slot; it cannot retain bytes.
	nullFD, err := unix.Open("/dev/null", unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(nullFD)
	nullRule := unix.LandlockPathBeneathAttr{Allowed_access: unix.LANDLOCK_ACCESS_FS_WRITE_FILE, Parent_fd: int32(nullFD)}
	_, _, errno = unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, ruleset, unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&nullRule)), 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("add null sink: %w", errno)
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, ruleset, 0, 0)
	if errno != 0 {
		return fmt.Errorf("restrict writes: %w", errno)
	}
	// Landlock intentionally permits already-open files. Never carry an outside
	// writable regular FD into Git, including malicious inherited standard FDs.
	for standard := 0; standard < 3; standard++ {
		var stat unix.Stat_t
		if err := unix.Fstat(standard, &stat); err != nil {
			return err
		}
		kind := stat.Mode & unix.S_IFMT
		if kind != unix.S_IFIFO && !(standard == 0 && kind == unix.S_IFCHR && stat.Rdev == unix.Mkdev(1, 3)) {
			return fmt.Errorf("standard streams must be pipes with read-only null input")
		}
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return fmt.Errorf("close inherited descriptors: %w", err)
	}
	return restrictSyscalls()
}

// DropPrivileges prepares the outer helper to start an ordinary unprivileged
// bwrap. It changes only this process's capabilities, never the host UID/setup.
func DropPrivileges() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("non-root worker required")
	}
	runtime.LockOSThread()
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return err
	}
	if err := unix.Prctl(unix.PR_CAP_AMBIENT, unix.PR_CAP_AMBIENT_CLEAR_ALL, 0, 0, 0); err != nil {
		return err
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	return unix.Capset(&header, &data[0])
}

// NamespaceArgs gives Git a read-only view of all existing host paths and one
// writable charged project. /tmp, /dev/shm and /proc stay read-only. Networking
// is retained for the pre-existing guarded loopback HTTPS transport.
func NamespaceArgs(helper, config, slot string, gitArgs []string) []string {
	args := []string{"bwrap", "--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--ro-bind", "/", "/", "--ro-bind", "/proc", "/proc", "--dev-bind", "/dev/null", "/dev/null", "--bind", slot, slot, "--cap-drop", "ALL", "--die-with-parent", "--", helper, "confined", config, slot, "--"}
	return append(args, gitArgs...)
}

func CheckNamespace(slot string) error {
	for _, path := range []string{"/", "/proc", "/tmp", "/dev/shm"} {
		var stat unix.Statfs_t
		if err := unix.Statfs(path, &stat); err != nil {
			return err
		}
		if stat.Flags&unix.ST_RDONLY == 0 {
			return fmt.Errorf("uncharged path %s is writable", path)
		}
	}
	var stat unix.Statfs_t
	if err := unix.Statfs(slot, &stat); err != nil {
		return err
	}
	if stat.Flags&unix.ST_RDONLY != 0 {
		return fmt.Errorf("charged slot is read-only")
	}
	return nil
}

func restrictSyscalls() error {
	// Prevent mutation of project IDs/flags, namespace or mount escape, acquiring
	// another process's FDs/capabilities, ptrace writes, and io_uring operations
	// that would otherwise evade syscall-specific seccomp checks.
	denied := []uint32{unix.SYS_IOCTL, unix.SYS_PTRACE, unix.SYS_PROCESS_VM_WRITEV, unix.SYS_PROCESS_VM_READV, unix.SYS_PIDFD_GETFD, unix.SYS_KCMP, unix.SYS_IO_URING_SETUP, unix.SYS_IO_URING_REGISTER, unix.SYS_IO_URING_ENTER, unix.SYS_MOUNT, unix.SYS_UMOUNT2, unix.SYS_PIVOT_ROOT, unix.SYS_CHROOT, unix.SYS_UNSHARE, unix.SYS_SETNS, unix.SYS_QUOTACTL, unix.SYS_QUOTACTL_FD, unix.SYS_CAPSET, unix.SYS_BPF, unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_NAME_TO_HANDLE_AT, unix.SYS_USERFAULTFD, unix.SYS_SETSID, unix.SYS_SETPGID, unix.SYS_FSOPEN, unix.SYS_FSCONFIG, unix.SYS_FSMOUNT, unix.SYS_MOVE_MOUNT, unix.SYS_OPEN_TREE, unix.SYS_MOUNT_SETATTR, unix.SYS_RECVMSG, unix.SYS_RECVMMSG}
	filters := []unix.SockFilter{
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 4}, // seccomp_data.arch
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AUDIT_ARCH_X86_64, Jt: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		// Reject the x32 syscall namespace as well as a foreign architecture.
		{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: 0x40000000, Jf: 1},
		{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_KILL_PROCESS},
	}
	// No Unix-domain socket or SCM_RIGHTS import may introduce an outside
	// writable FD after the inherited descriptor sweep. Git needs TCP only.
	for _, nr := range []uint32{unix.SYS_SOCKET, unix.SYS_SOCKETPAIR} {
		filters = append(filters,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr, Jf: 4},
			unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.AF_UNIX, Jf: 1},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
			unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0})
	}
	filters = append(filters,
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_CLONE, Jf: 4},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, K: unix.CLONE_NEWUSER | unix.CLONE_NEWNS | unix.CLONE_NEWPID | unix.CLONE_NEWNET | unix.CLONE_NEWIPC | unix.CLONE_NEWUTS | unix.CLONE_NEWCGROUP, Jf: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: unix.SYS_CLONE3, Jf: 1},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS)})
	for _, nr := range denied {
		filters = append(filters, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: nr, Jf: 1}, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)})
	}
	filters = append(filters, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ALLOW})
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	_, _, errno := unix.Syscall(unix.SYS_SECCOMP, unix.SECCOMP_SET_MODE_FILTER, 0, uintptr(unsafe.Pointer(&program)))
	if errno != 0 {
		return fmt.Errorf("seccomp confinement: %w", errno)
	}
	return nil
}
