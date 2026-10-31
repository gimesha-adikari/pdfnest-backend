//go:build linux

// Package gitquota consumes administrator-provisioned XFS project quota slots.
// It never mounts filesystems, assigns projects, or changes quota limits.
package gitquota

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	HardBytes          uint64 = 262144000
	HardInodes         uint64 = 25000 // TOTAL inodes, including the slot root and directories.
	xfsMagic                  = 0x58465342
	fsGetXattr                = 0x801c581f
	projectInherit            = 0x200
	projectQuota              = 2
	projectAccounting         = 1 << 4
	projectEnforcement        = 1 << 5
)

// These layouts and 512-byte units come from Linux UAPI dqblk_xfs.h and fs.h.
type fsxattr struct {
	Flags, Extsize, Nextents, Project, Cowextsize uint32
	Pad                                           [8]byte
}
type diskQuota struct {
	Version                                                    int8
	Flags                                                      int8
	Mask                                                       uint16
	ID                                                         uint32
	BlockHard, BlockSoft, InodeHard, InodeSoft, Blocks, Inodes uint64
	ITimer, BTimer                                             int32
	IWarns, BWarns                                             uint16
	TimerHi                                                    [4]byte
	RTBlockHard, RTBlockSoft, RTBlocks                         uint64
	RTTimer                                                    int32
	RTWarns                                                    uint16
	Pad3                                                       int16
	Pad4                                                       [8]byte
}
type quotaStat struct {
	Version int8
	Pad1    uint8
	Flags   uint16
	Incore  uint32
	Files   [3]struct {
		Inode, Blocks uint64
		Extents, Pad  uint32
	}
	Timers [3]int32
	Warns  [3]uint16
	Pad3   uint16
	Pad4   uint32
	Pad2   [7]uint64
}

// Usage is kernel accounting, including allocations held by open unlinked files.
type Usage struct{ Bytes, Inodes uint64 }

type inspector interface{ Inspect(Slot) (Usage, error) }
type kernelInspector struct{}

func quotaCall(fd int, command, id uint32, out unsafe.Pointer) error {
	_, _, errno := unix.Syscall6(unix.SYS_QUOTACTL_FD, uintptr(fd), uintptr(command<<8|projectQuota), uintptr(id), uintptr(out), 0, 0)
	if errno != 0 {
		return errno
	}
	return nil
}

// Inspect verifies both enforcement status and the exact independent block/inode
// limits. EPERM, unavailable quotas, non-XFS filesystems, and unknown layouts fail.
func (kernelInspector) Inspect(slot Slot) (Usage, error) {
	fd, err := unix.Open(slot.Path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Usage{}, err
	}
	defer unix.Close(fd)
	var statfs unix.Statfs_t
	if err := unix.Fstatfs(fd, &statfs); err != nil {
		return Usage{}, err
	}
	if statfs.Type != xfsMagic {
		return Usage{}, fmt.Errorf("slot filesystem is not XFS")
	}
	var attr fsxattr
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), fsGetXattr, uintptr(unsafe.Pointer(&attr)))
	if errno != 0 {
		return Usage{}, errno
	}
	status := quotaStat{Version: 1}
	if err := quotaCall(fd, ('X'<<8)+8, 0, unsafe.Pointer(&status)); err != nil {
		return Usage{}, fmt.Errorf("query XFS quota enforcement: %w", err)
	}
	quota := diskQuota{Version: 1, ID: slot.Project}
	if err := quotaCall(fd, ('X'<<8)+3, slot.Project, unsafe.Pointer(&quota)); err != nil {
		return Usage{}, fmt.Errorf("query XFS project limits: %w", err)
	}
	return validateQuota(slot, attr, status, quota)
}

func validateQuota(slot Slot, attr fsxattr, status quotaStat, quota diskQuota) (Usage, error) {
	if attr.Project != slot.Project || attr.Project == 0 || attr.Flags&projectInherit == 0 || attr.Flags&1 != 0 {
		return Usage{}, fmt.Errorf("slot lacks its non-realtime inherited project identity")
	}
	if status.Version != 1 || status.Flags&(projectAccounting|projectEnforcement) != projectAccounting|projectEnforcement {
		return Usage{}, fmt.Errorf("project quota accounting and enforcement are not both active")
	}
	if quota.Version != 1 || quota.Flags != projectQuota || quota.ID != slot.Project || quota.BlockHard != HardBytes/512 || quota.InodeHard != HardInodes {
		return Usage{}, fmt.Errorf("kernel hard limits differ from %d allocated bytes and %d total inodes", HardBytes, HardInodes)
	}
	if quota.RTBlocks != 0 {
		return Usage{}, fmt.Errorf("unexpected realtime allocations")
	}
	if quota.Blocks > HardBytes/512 || quota.Inodes > HardInodes {
		return Usage{}, fmt.Errorf("quota usage already exceeds hard limit")
	}
	return Usage{Bytes: quota.Blocks * 512, Inodes: quota.Inodes}, nil
}

// ProbeSlot is read-only. A successful result proves kernel configuration, not
// provisioning authorization or write confinement; Load separately checks those.
func ProbeSlot(slot Slot) (Usage, error) { return (kernelInspector{}).Inspect(slot) }

func rootBaseline(path string) (Usage, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Usage{}, err
	}
	defer unix.Close(fd)
	var s unix.Stat_t
	if err := unix.Fstat(fd, &s); err != nil {
		return Usage{}, err
	}
	return Usage{Bytes: uint64(s.Blocks) * 512, Inodes: 1}, nil
}
