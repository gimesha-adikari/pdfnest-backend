# Git hard storage adapter

Linux amd64 required mode consumes independently approved, preprovisioned XFS
project slots. It never provisions mounts, assigns projects, sets quotas, grants
capabilities, installs helpers, changes UIDs, or changes host setup. Monitored
mode remains the default.

Set `ANALYZER_GIT_STORAGE_POLICY=required` and `ANALYZER_GIT_QUOTA_CONFIG` to an
administrator-owned JSON record. Example only, not existing resources:

```json
{"version":2,"workerUid":1000,"stateDir":"/var/lib/platen-git-state/authority","authoritySocket":"/run/platen-git-authority/control.sock","leaseSeconds":900,"helper":"/usr/local/libexec/git-quota-exec","slots":[{"path":"/srv/platen-git-slots/slot-1","project":41001,"generation":1}]}
```

Authority/helper ancestors must be root-owned, not group/world writable, and
contain no symlink. StateDir is a root-owned private 0700 directory; slot roots
are private 0700 directories owned by the approved non-root worker, below
protected parents. The independently started `git-quota-exec authority CONFIG`
process must run as root with that protected state, outside the backend service
lifecycle. This code never installs or starts such a host service. Its Unix
socket uses kernel SO_PEERCRED and SO_PEERPIDFD: clients require a root peer;
the authority admits only the configured worker UID. Same-UID worker state or a
boolean setting cannot substitute for the protected authority. Slots/project IDs are
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

An exclusive root-authority flock-held journaled lease attaches to the Sandbox before clone
and survives downstream trusted Go/HTTP analysis until final Cleanup. Repository
code is not executed by those analyzers. Every Git invocation must register its supervisor with the authority before
spawn, after fresh kernel quota verification. The outer helper supervises bwrap
using a stable pidfd. The PID namespace kills descendants when its initial
process exits; bwrap uses die-with-parent and its launch has a parent-death
signal. Release with a still-registered writer quarantines the slot.
Cleanup uses os.Root, retains the project root, and requires no entries plus
kernel usage exactly equal to the root's allocated blocks and one inode.
Open unlinked allocations prevent recycling. Interrupted/corrupt/dirty slots
are quarantined. Recovery needs independent administrator fencing, replacement
of the slot root and a newer generation in the root-owned record. The adapter
checks changed inode, newer generation and clean kernel baseline before reuse.
It performs none of those external recovery/setup actions itself.

The authority durably stores a random lease identity, slot generation, root
inode and absolute deadline before granting use. `leaseSeconds` must be 1..3600;
leases cannot renew. An independent authority timer (with a monotonic running
clock) expires the generation even after backend death or SIGSTOP. It kills
registered supervisors by stable peer pidfd, attempts descendant cleanup and
checks kernel accounting; expiration always records quarantine. Authority
connection loss or deadline also kills the supervised bwrap process. Authority
restart never resumes a leased generation; its interrupted journal quarantines
it. Expired or unknown ownership never permits timer-only slot reuse. Ordinary
reuse requires completed writer supervision, cleanup and exact clean kernel
accounting; interrupted/expired recovery still needs independent root replacement
and a newer authorized generation. Socket replacement/stale socket cleanup is
administrator-controlled; the service never unlinks an existing authority socket.

The built `git-quota-exec capability` reports BLOCKED/exit 125 without approved
configuration. A successful kernel query says KERNEL_QUOTAS_VERIFIED and marks
confinement runtime NOT_EXERCISED; it does not claim allocation qualification.
Tests distinguish actual unprivileged deadline/daemon-kill process contracts
and temporary-directory bwrap/Landlock/seccomp/loopback proof from synthetic quota readback and lease/journal tests. Actual XFS EDQUOT
byte/inode/open-unlink boundary qualification remains BLOCKED until approved
preprovisioned slots, the protected running expiration service and broker
authority are provided. The unprivileged process tests do not qualify the root
authority service or actual kernel storage limits. No hosting/deployment
or privileged host modification is implied.
