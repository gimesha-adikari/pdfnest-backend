# Git hard storage adapter

Linux amd64 required mode consumes independently approved, preprovisioned XFS
project slots. It never provisions mounts, assigns projects, sets quotas, grants
capabilities, installs helpers, changes UIDs, or changes host setup. Monitored
mode remains the default.

Set `ANALYZER_GIT_STORAGE_POLICY=required` and `ANALYZER_GIT_QUOTA_CONFIG` to an
administrator-owned JSON record. Example only, not existing resources:

```json
{"version":1,"workerUid":1000,"stateDir":"/var/lib/platen-git-state/worker","helper":"/usr/local/libexec/git-quota-exec","slots":[{"path":"/srv/platen-git-slots/slot-1","project":41001,"generation":1}]}
```

Authority/helper ancestors must be root-owned, not group/world writable, and
contain no symlink. State and slot roots are private 0700 directories owned by
the approved non-root worker, below protected parents. Slots/project IDs are
distinct and finite (maximum 128). The worker does not hold quota authority.
The ordinary trusted helper exposes only approved project reads and fixed Git
execution. Linux project quota reads may require a separately approved helper
file capability; this implementation never installs it. Setuid/setgid helpers
and bwrap are rejected. Query refusal fails closed.

Kernel FS_IOC_FSGETXATTR and quotactl_fd XFS Q_XGETQSTATV/Q_XGETQUOTA require
matching inherited nonzero project IDs, active accounting AND enforcement,
exactly 512000 512-byte hard blocks (262144000 allocated bytes), and 25000 TOTAL
hard inodes. Root, directories, Git metadata, HOME/XDG/tmp and open unlinked
inodes are charged. Realtime allocation and unknown layouts are rejected.
Scans remain defense only and never certify quota enforcement.

The helper verifies quotas, drops all capabilities, sets no-new-privileges, then
starts ordinary unprivileged bwrap. Process-private user/PID/mount/IPC/UTS
namespaces give a read-only root, /tmp, /dev/shm and /proc, with one writable
charged slot bind. Original networking retains the guarded HTTPS loopback proxy.
The inner helper checks mount flags and applies Landlock ABI >=6 write, REFER,
TRUNCATE and signal restrictions plus seccomp. It closes inherited FDs on exec
and requires standard pipes/null input before fixed /usr/bin/git execution.
/dev/null is the only outside writable sink and cannot retain allocations.
Project ioctls, new namespaces/mounts, ptrace/remote writes, io_uring, Unix
sockets and SCM_RIGHTS imports are denied. The read-only root also protects
metadata operations Landlock does not mediate.

An exclusive flock-held journaled lease attaches to the Sandbox before clone
and survives downstream trusted Go/HTTP analysis until final Cleanup. Repository
code is not executed by those analyzers. Git descendants are terminated before
release; the PID namespace kills descendants when its initial process exits.
Cleanup uses os.Root, retains the project root, and requires no entries plus
kernel usage exactly equal to the root's allocated blocks and one inode.
Open unlinked allocations prevent recycling. Interrupted/corrupt/dirty slots
are quarantined. Recovery needs independent administrator fencing, replacement
of the slot root and a newer generation in the root-owned record. The adapter
checks changed inode, newer generation and clean kernel baseline before reuse.
It performs none of those external recovery/setup actions itself.

"Finite" describes the fixed maximum number of concurrent slot owners. Leases
have no wall-clock expiration: clone execution retains its existing timeout and
the caller owns the lease through downstream analysis. A caller/process failure
quarantines its generation rather than releasing storage on a timer. An expired
timer alone would not prove old writers stopped, so this adapter promises no
time-bounded recycling or downstream analysis deadline.

The built `git-quota-exec capability` reports BLOCKED/exit 125 without approved
configuration. A successful kernel query says KERNEL_QUOTAS_VERIFIED and marks
confinement runtime NOT_EXERCISED; it does not claim allocation qualification.
Tests distinguish actual temporary-directory bwrap/Landlock/seccomp/loopback
proof from synthetic quota readback and lease/journal tests. Actual XFS EDQUOT
byte/inode/open-unlink boundary qualification remains BLOCKED until approved
preprovisioned slots and broker authority are provided. No hosting/deployment
or privileged host modification is implied.
