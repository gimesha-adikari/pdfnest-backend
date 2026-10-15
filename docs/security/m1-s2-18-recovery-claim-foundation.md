# M1-S2-18 — Dormant recovery-claim fencing foundation

Qualified 2026-10-16 from exact backend SHA
`1884f912f74a2b3dc0a59bf3c28f165d792ceb64`, branch
`milestone-1/ocr-terminal-replay-hardening` (open/draft PR #22), direct parent
`7f38034e4037556ab56db908dcf78dedf0fef852`. Local HEAD, advertised remote HEAD
and PR head/base matched before edits; backend worktree was clean and disk had
about 9.7 GiB free. No fetch/rebase/merge, clone or worktree was needed.

Read the current model, repository, authority, foundation/lifecycle/replay tests,
billing transaction primitives and M1-S2-08/16/17 evidence. M1-S2-15R background
was read through GitHub at documentation commit
[`79b6e404b2a8d04f1fb61ac06b99cbaf0dc81848`](https://github.com/gimesha-adikari/platen-docs/blob/79b6e404b2a8d04f1fb61ac06b99cbaf0dc81848/ai-handoffs/platen-antigravity-transition-2026-10-15/14-M1-S2-15-16-CODEX-RESUMPTION.md);
it is a read-only investigation, not cleanup implementation authority. Graph
tools were unavailable; inspection uses targeted source reads/searches without
claiming graph coverage.

## Scope and trusted authority

Only three new files are delivered: `internal/ocr/finalization_recovery.go`, its
focused test file, and this report. Existing source/model/tests remain unchanged.
There is no production adapter, HTTP route, scanner or runtime registration.

`recoveryVerifier.VerifyRecoveryTask` must independently authorize a trusted
server invocation and return its canonical task. `trustedRecovery` rejects a
missing verifier, verifier error or empty result and creates a private typed
`recoveryAuthority`. No task argument is passed to the verifier to echo. Each
operation checks verified authority for the exact requested task separately from
owner/reservation binding. Merely knowing a task ID, prefix, owner, reservation,
worker identity or token does not establish this authority. Trusted Go code in
the package remains inside the trust boundary; this is not a sandbox against
malicious same-package code. Tests use explicitly synthetic verifier adapters.

`lockRecovery` requires a caller-owned PostgreSQL transaction, locks the exact
task through the existing finalization helper and checks protocol/tool,
owner/reservation, nonzero trusted time and existing lifecycle chronology. It
requires a terminal task with matching expected state/revision and a nonzero
decision timestamp. The actual captured reservation is then locked/validated
through the unchanged billing primitive, including task/owner/tool/allocation.
Succeeded requires committed; cancelled/failed require released. Inconsistent
claim/lease pairs fail closed. No settlement, release, repricing or subscription
write occurs. Lock order remains task → reservation.

## Ownership protocol

The existing `NextAttemptAt`, `RecoveryClaim` and `RecoveryLeaseUntil` suffice.
No new column or duplicate decision revision is needed. `DownloadExpiresAt` and
`CleanupNotBefore` are preserved placeholders with no selected retention policy.

- `acquireRecoveryInTx` requires a nonzero explicitly scheduled `NextAttemptAt`
  at or before trusted `Now`, plus an explicit lease deadline after `Now`. An
  unexpired existing lease rejects acquisition. With no owner or at/after expiry,
  the existing `randomAuthority` generates 32 cryptographically random bytes,
  encoded as 64 hex characters, and persists claim and lease together.
- `renewRecoveryInTx` requires the exact nonempty current claim, trusted task
  authority and an existing lease for which `Now < RecoveryLeaseUntil`. The new
  deadline must exceed both `Now` and the previous persisted deadline.
- `relinquishRecoveryInTx` requires the same current active ownership and clears
  only claim/lease. `NextAttemptAt`, retention placeholders and all evidence stay
  unchanged. It does not assert recovery work or cleanup completed.
- Equality with lease expiry is expired. A separately authorized acquisition
  replaces the expired token; the old token cannot renew or relinquish the new
  ownership, even using its captured pre-expiry request timestamp.

Lease deadlines truncate to PostgreSQL microsecond precision before comparison;
a nanosecond-only apparent extension that would persist unchanged is rejected.
There is no default duration, retry cadence or production clock policy. `Now`
is trusted internal coordinator time, not an untrusted HTTP timestamp. Tests
use explicit synthetic times; expiry validation is against that supplied time,
not a claim of server-clock revalidation after an arbitrary lock wait.

All methods use the caller's transaction without nested independent transactions.
A returned acquisition token is provisional: it must not be used or disclosed
as committed authority until the surrounding transaction commits successfully.
Rollback discards it. An uncertain commit requires authoritative reread rather
than treating an unacknowledged token as ownership.

`updateRecoveryOwnership` uses explicit `UpdateColumns` for exactly
`recovery_claim` and `recovery_lease_until`, avoiding GORM's automatic `UpdatedAt`.
CAS matches task, owner, reservation, state, lifecycle revision, previous claim
and previous lease (including `IS NULL` for absence). One affected row is required.
The helper expects the locked/validated row; direct stale-snapshot tests also
exercise its token and lease predicates independently.

## Deterministic PostgreSQL and accounting evidence

Fresh task-owned PostgreSQL **18.6**, UTF-8 database `test_m1_s218_utf8`, role
`m1_s218`, bound only to `127.0.0.1:5558`. Server version, database, address and
port were queried directly. Existing guarded `foundationDB` creates test schema
and uses bounded primary/independent observer pools. No Redis, application,
worker, browser, frontend or production service was started.

```sh
export PLATEN_M1_BILLING_TEST_DATABASE_URL='postgres://m1_s218@127.0.0.1:5558/test_m1_s218_utf8?sslmode=disable'
go test -p 1 ./internal/ocr -run '^TestOCRRecovery' -count=1 -timeout=4m -json
go test -race -p 1 ./internal/ocr -run '^TestOCRRecovery' -count=1 -timeout=5m -json
go test -p 1 ./internal/ocr -run '^TestOCRSuccessReplayMaintenanceCharacterization$' -count=1 -timeout=3m -json
go test -p 1 ./internal/ocr -run '^TestOCRFoundation(AtomicDecisions|CompetingDecisions|CreateConstraints)$' -count=1 -timeout=4m -json
go vet ./internal/ocr
gofmt -l internal/ocr/finalization_recovery.go internal/ocr/finalization_recovery_test.go
git diff --check
```

| Final run | Top-level passed | Leaf cases passed | Pass events including groups/parents | Failed / skipped |
| --- | ---: | ---: | ---: | --- |
| Recovery | 4 | 71 | 82 | 0 / 0 |
| Recovery, race detector | 4 | 71 | 82 | 0 / 0 |
| M1-S2-17 replay | 1 | 25 | 30 | 0 / 0 |
| Foundation atomic decisions, contention, constraints | 3 | 49 | 52 | 0 / 0 |

The new API first failed compilation while absent; after adding signatures, the
positive acquisition test failed against an intentionally rejecting stub on real
PostgreSQL. Implementation then passed the full final runs above. No unexpected
test failures or race diagnostics occurred. Vet, formatting and whitespace checks
passed. A separate bounded read-only source review found no actionable defects.

Coverage includes all terminal states; plan, credit and mixed allocations; due
and unscheduled/future work; nonterminal/stale lifecycle rejection; verifier,
authority, identity and reservation-state/binding rejection; cross-task tokens;
empty/wrong/expired/superseded claims; deadline equality/precision; rollback;
and current-holder acquisition/renewal/relinquishment.

Six contention cases use PostgreSQL transactions, PID channels, observed lock
waits and `pg_blocking_pids` to prove the exact first writer blocks the second.
No sleeps establish ordering. Competing acquisitions cover commit and rollback.
Stale renewal and relinquishment each wait behind takeover, with commit and
rollback outcomes. Captured old request time is before expiry, so rejection after
committed takeover demonstrates token fencing rather than expiry alone. A
rolled-back takeover grants no new authority; old ownership remains valid under
its pre-expiry supplied time. Independent observers see no partial/uncommitted
claim. Direct CAS tests separately reject a stale lease and a superseded token
even when the other predicate is current.

Complete independent task/reservation/subscription/usage snapshots allow only
the two ownership fields to differ. State, revision, decision/lifecycle times,
`UpdatedAt`, processing claim, cancellation capability, complete artifact,
scheduling/retention placeholders, allocation, reservation status, credits,
window counters/reset times and usage logs remain unchanged. Original success
requests replay during ownership, after relinquishment and after takeover with
no second settlement. Each synthetic owner retains one task and reservation.

Plan accounting remains 7/7/7 usage, 50 credits, one log; credits-only remains
8/8/8 usage, 43 credits, one log; mixed remains 8/8/8 usage, 46 credits, one log.
Valid successful cases retain committed reservations. Cancelled/failed remain
terminal with released reservations, unchanged counters/credits and no new logs.
Synthetic reservation corruption is rejected without attempting repair.

## Preservation and remaining gates

The original M1-S2-07 known-bad characterization, M1-S2-17 replay code, billing,
model, managed schema/migrations, legacy Redis OCR, storage and configuration are
unchanged. The protected historical PostgreSQL directory was never opened or
reused. Test teardown left zero task/reservation/log/subscription/user records;
task-owned PostgreSQL is stopped and only its independently verified disposable
data directory is removed before publication. Sanitized logs stay outside Git.
Outer user changes/staging/docs/scratch, frontend `AGENTS.md` and all independent
branches are preserved. Disk remains about 9.7 GiB free.

This foundation is published as one draft stacked PR on
`milestone-1/ocr-terminal-replay-hardening`, with exact parent
`1884f912f74a2b3dc0a59bf3c28f165d792ceb64`. PR #22 and earlier branches/PRs are
unchanged. No merge, ready promotion, activation, migration or deployment occurs.
GitHub CI is separate from local PostgreSQL proof; the checked-in Actions workflow
targets master/main and does not qualify these stacked-PR tests.

An SQL recovery lease fences repository bookkeeping only. It cannot cancel or
revoke a filesystem/S3 DELETE already issued. No physical deletion/overwrite,
provider acknowledgment, cleanup outcome/tombstone/uncertainty transition,
retention decision, orphan inventory, Redis projection, background scanner,
processing takeover, reconciliation, janitor dispatch, free-mode journal or
production coordinator is implemented. Durable cleanup outcomes, provider
identity/fencing/IAM, download races, late writes, protocol discrimination,
coordinator clock policy, migration and product retention/refund decisions remain
separate gates. Relinquishment preserves an obligation; it is never deletion
proof. **The live M1-S2-07 charged-without-artifact defect remains unresolved.**
