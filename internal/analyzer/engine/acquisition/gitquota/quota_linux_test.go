//go:build linux

package gitquota

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

type syntheticKernel struct {
	retained Usage
	fail     error
}

func (k *syntheticKernel) Inspect(slot Slot) (Usage, error) {
	if k.fail != nil {
		return Usage{}, k.fail
	}
	base, err := rootBaseline(slot.Path)
	base.Bytes += k.retained.Bytes
	base.Inodes += k.retained.Inodes
	return base, err
}

func syntheticManager(t *testing.T, n int) (*Manager, *syntheticKernel) {
	t.Helper()
	state := t.TempDir()
	config := Config{Version: 1, WorkerUID: os.Geteuid(), StateDir: state}
	for i := 0; i < n; i++ {
		config.Slots = append(config.Slots, Slot{Path: t.TempDir(), Project: uint32(100 + i), Generation: 1})
	}
	k := &syntheticKernel{}
	return &Manager{config: config, kernel: k}, k
}

func TestFiniteExclusiveLeaseHeldThroughAnalysisAndCleanup(t *testing.T) {
	m, _ := syntheticManager(t, 2)
	first, err := m.Acquire()
	require.NoError(t, err)
	second, err := m.Acquire()
	require.NoError(t, err)
	_, err = m.Acquire()
	require.ErrorIs(t, err, ErrUnavailable)
	// Clone completion has no release operation: downstream analysis still owns
	// its project, and two executions cannot share it.
	require.NoError(t, os.Mkdir(filepath.Join(first.Path(), "repository"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(first.Path(), "repository", "source"), []byte("analysis still active"), 0600))
	_, err = m.Acquire()
	require.ErrorIs(t, err, ErrUnavailable)
	require.NoError(t, first.Release())
	require.DirExists(t, first.Path(), "the administrator-owned project root survives cleanup")
	reused, err := m.Acquire()
	require.NoError(t, err)
	require.Equal(t, first.Path(), reused.Path())
	require.NoError(t, reused.Release())
	require.NoError(t, second.Release())
	require.NoError(t, first.Release())
}

func TestInterruptedAndCorruptJournalQuarantineWithoutReuse(t *testing.T) {
	for _, kind := range []string{"interrupted", "corrupt"} {
		t.Run(kind, func(t *testing.T) {
			m, _ := syntheticManager(t, 1)
			lease, err := m.Acquire()
			require.NoError(t, err)
			if kind == "corrupt" {
				require.NoError(t, lease.file.Truncate(3))
			}
			require.NoError(t, lease.file.Close()) // model process death, not Release
			_, err = m.Acquire()
			require.ErrorIs(t, err, ErrUnavailable)
			data, err := os.ReadFile(filepath.Join(m.config.StateDir, "100.lease"))
			require.NoError(t, err)
			var record journal
			require.NoError(t, json.Unmarshal(data, &record))
			require.Equal(t, "quarantined", record.Status)
		})
	}
}

func TestKernelOpenUnlinkAccountingPreventsLeaseRecycle(t *testing.T) {
	m, k := syntheticManager(t, 1)
	lease, err := m.Acquire()
	require.NoError(t, err)
	// Synthetic syscall/broker response, NOT evidence of a kernel quota test.
	// An unlinked open inode must fail even if the directory scan is empty.
	k.retained = Usage{Bytes: 4096, Inodes: 1}
	require.Error(t, lease.Release())
	k.retained = Usage{}
	_, err = m.Acquire()
	require.ErrorIs(t, err, ErrUnavailable, "a quarantined generation needs independent recovery authorization")
}

func TestRecoveryRequiresNewAuthorizedGenerationAndReplacedRoot(t *testing.T) {
	m, _ := syntheticManager(t, 1)
	lease, err := m.Acquire()
	require.NoError(t, err)
	require.NoError(t, lease.file.Close()) // interrupted generation remains leased
	// Model independent administrator recovery. No production API performs
	// either privileged/configuration operation, and a generation bump alone
	// does not certify fencing of the old Landlock root.
	oldRoot := m.config.Slots[0].Path
	require.NoError(t, os.Rename(oldRoot, oldRoot+"-retired"))
	t.Cleanup(func() { os.RemoveAll(oldRoot + "-retired") })
	require.NoError(t, os.Mkdir(oldRoot, 0700))
	m.config.Slots[0].Generation++
	recovered, err := m.Acquire()
	require.NoError(t, err)
	require.NoError(t, recovered.Release())
}

func TestQuotaAuthorityAndFilesystemFailClosed(t *testing.T) {
	_, err := Load("")
	require.ErrorIs(t, err, ErrUnavailable)
	path := filepath.Join(t.TempDir(), "authority.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"version":1}`), 0600))
	_, err = Load(path)
	require.ErrorIs(t, err, ErrUnavailable, "same-UID authority cannot certify itself")
	_, err = ProbeSlot(Slot{Path: t.TempDir(), Project: 1})
	require.Error(t, err, "ordinary temporary storage is not a demonstrated project quota")
}

func TestLinuxQuotaUAPILayoutAndExactUnits(t *testing.T) {
	require.EqualValues(t, 112, unsafe.Sizeof(diskQuota{}))
	require.EqualValues(t, 160, unsafe.Sizeof(quotaStat{}))
	require.EqualValues(t, 28, unsafe.Sizeof(fsxattr{}))
	require.EqualValues(t, 0, HardBytes%512)
	require.EqualValues(t, 512000, HardBytes/512)
	require.EqualValues(t, 25000, HardInodes)
	var st unix.Statfs_t
	require.NoError(t, unix.Statfs(t.TempDir(), &st))
	t.Logf("actual fixture filesystem magic=%#x; this test does not provision or qualify XFS quotas", st.Type)
}

func TestSyntheticKernelReadbackRejectsUnenforcedOrWrongLimits(t *testing.T) {
	slot := Slot{Project: 81}
	attr := fsxattr{Project: 81, Flags: projectInherit}
	status := quotaStat{Version: 1, Flags: projectAccounting | projectEnforcement}
	quota := diskQuota{Version: 1, Flags: projectQuota, ID: 81, BlockHard: HardBytes / 512, InodeHard: HardInodes, Blocks: 8, Inodes: 1}
	usage, err := validateQuota(slot, attr, status, quota)
	require.NoError(t, err)
	require.Equal(t, Usage{4096, 1}, usage)
	for _, kind := range []string{"wrong-project", "no-inherit", "accounting-only", "zero-block-limit", "larger-block-limit", "zero-inode-limit", "larger-inode-limit", "realtime", "wrong-version", "over-limit"} {
		t.Run(kind, func(t *testing.T) {
			a, s, q := attr, status, quota
			switch kind {
			case "wrong-project":
				a.Project++
			case "no-inherit":
				a.Flags = 0
			case "accounting-only":
				s.Flags = projectAccounting
			case "zero-block-limit":
				q.BlockHard = 0
			case "larger-block-limit":
				q.BlockHard++
			case "zero-inode-limit":
				q.InodeHard = 0
			case "larger-inode-limit":
				q.InodeHard++
			case "realtime":
				q.RTBlocks = 1
			case "wrong-version":
				q.Version = 2
			case "over-limit":
				q.Inodes = HardInodes + 1
			}
			_, err := validateQuota(slot, a, s, q)
			require.Error(t, err)
		})
	}
}
