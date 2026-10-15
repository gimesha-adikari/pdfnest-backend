# M1-S2-19 — Dormant recovery-lease fresh-clock hardening

Qualified 2026-10-16 from exact SHA
`6bba340cec1ecfb7c3cf08cb96e0ea1891825d08`, branch
`milestone-1/ocr-recovery-claim-foundation`, direct parent
`1884f912f74a2b3dc0a59bf3c28f165d792ceb64`. Local HEAD, advertised remote HEAD
and open/draft PR #23 head/base matched before edits. The backend was clean;
disk had about 9.7 GiB free. No fetch, rebase, merge, clone or worktree was used.

## Source finding and pre-change characterization

At the parent, acquisition compared due time, existing lease and proposed
deadline with `req.Now`; renewal/relinquishment also tested ownership with that
captured time after acquiring locks. This was trusted internal request time, not
an HTTP-client timestamp. It nevertheless could predate an actual lease expiry.

Before repository source changes, five characterization cases ran on real
PostgreSQL with controlled synthetic metadata and independent durable observers:

| Scenario | PR #23 observed behavior | Corrected regression |
| --- | --- | --- |
| Matching token, captured time before lease, database clock after expiry | Renewal accepted | Reject, preserve ownership metadata |
| Same expired ownership | Relinquishment accepted | Reject, preserve obligation and metadata |
| Actually expired claim appears active to old request | Acquisition rejected | Accept valid takeover with a fresh token |
| Proposed deadline after request time but before database time | Acquisition accepted with expired deadline | Reject |
| Future obligation appears due to future request time | Acquisition accepted | Reject until actually due |

All five historical assertions passed before the fix. Replacing them with the
corrected expectations made all five fail against unchanged repository source;
they then passed with the correction. These are dormant source findings, not
production incidents. Historical S2-18 evidence remains unchanged.

## Fresh-clock implementation

Only `finalization_recovery.go`, `finalization_recovery_test.go` and this report
change. `recoveryNowInTx` executes a separate `SELECT clock_timestamp()` on the
same validated caller transaction, after `lockRecovery` returns holding both the
task and actual captured reservation locks. The three ownership methods call it
before evaluating eligibility. No independent transaction or early lock release
is introduced.

PostgreSQL documents `clock_timestamp()` as actual current time, while `now()` /
`transaction_timestamp()` retain transaction-start time. See the official
[PostgreSQL 18 current-time semantics](https://www.postgresql.org/docs/18/functions-datetime.html#FUNCTIONS-DATETIME-CURRENT).
A separate query after both locks avoids sampling at the start of a blocked
locking statement. No coordinator wall clock, skew allowance or test clock is
substituted for that database lookup.

- Acquisition uses fresh time for obligation due status, active-lease exclusion
  and the proposed deadline. An expired lease permits a fresh random token.
- Renewal requires exact current ownership unexpired at that fresh check, and a
  deadline later than both fresh time and the previous persisted deadline.
- Relinquishment requires current unexpired ownership and clears only claim and
  lease. It does not acknowledge work or cleanup completion.
- Deadline truncation to microseconds, exact-expiry rejection for ownership,
  task-specific trusted authority, terminal state/revision, exact identity and
  reservation binding/status, and token-plus-lease CAS remain intact.

`req.Now` remains required, nonzero and trusted for the existing lifecycle
chronology checks. It no longer establishes recovery scheduling or lease
eligibility. No lifecycle, processing, cancellation or billing-time behavior
changes. No duration, retry cadence or retention policy is added.

## Fixture and contention qualification

The shared foundation fixture deliberately advances lifecycle timestamps by
seconds; S2-18 additionally moved recovery request time a minute ahead. That
cannot represent a stale request predating real expiry without waiting or
rewriting shared tests. Only the local recovery fixture now uses the same real
accepted → claim → intent → ready → terminal repository APIs with explicit
microsecond logical steps. It verifies terminal chronology precedes actual
database time. Recovery scheduling/expiry is separate, explicit fixture state.
The shared fixture file, database harness and all other tests remain unchanged.
No clocks are shifted, no mutable clock is installed and no standard API bypass
is used.

Old expiry/time-travel setups now seed a genuinely expired lease using test-only
SQL, preserving token and lifecycle. The existing takeover rollback tests roll
back both that expiry setup and takeover, restoring a genuinely active old lease;
their valid-holder success assertions remain. Authority/binding, stale-token,
rollback, cross-task, allocation and independent snapshot assertions are retained.

Sixteen new lock-wait cases exercise task and reservation blockers, each with
commit and rollback, for renewal, relinquishment, expired proposed deadline and
valid expired-claim takeover. PostgreSQL lock waits plus `pg_blocking_pids` prove
the exact blocker. Sampled transaction/pre-lock clocks and the blocked-writer
observation precede expiry. Bounded polling then observes actual database expiry
before releasing the blocker, without changing the row. The resulting rejection
or takeover therefore requires post-lock time, including after reservation waits.
No sleeps establish lock ordering; the two-second deadlines are synthetic test
inputs, not product policy.

Precision tests verify persisted microsecond truncation and reject a nanosecond-
only renewal extension. Exact equality is checked at the pure ownership predicate;
real repository expiry checks always read PostgreSQL time. The existing six
token-fencing contention cases and direct stale-token/stale-lease CAS tests also
remain, giving 22 row-lock contention cases in the recovery suite.

## Test environment and results

Fresh task-owned PostgreSQL **18.6**, UTF-8 database `test_m1_s219_utf8`, synthetic
role `m1_s219`, bound only to `127.0.0.1:5559`. Version/database/address/port were
queried directly. Existing guarded schema setup and bounded primary/independent
observer pools were reused. No Redis, application, worker, frontend or browser
service was started.

```sh
export PLATEN_M1_BILLING_TEST_DATABASE_URL='postgres://m1_s219@127.0.0.1:5559/test_m1_s219_utf8?sslmode=disable'
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
| Recovery | 8 | 94 | 107 | 0 / 0 |
| Recovery, race detector | 8 | 94 | 107 | 0 / 0 |
| M1-S2-17 replay | 1 | 25 | 30 | 0 / 0 |
| Foundation atomic decisions, contention and constraints | 3 | 49 | 52 | 0 / 0 |

No unexpected failures or race diagnostics occurred. Vet, formatting and
whitespace checks passed. Bounded read-only source review found no actionable
defects. Graph tools were unavailable; no graph coverage claim is made.

Full independent snapshots preserve terminal state/revision, all lifecycle and
decision timestamps including `UpdatedAt`, processing claim, complete artifact,
cancellation capability, obligation/retention metadata, captured allocation,
reservation state, window counters/reset times, credits and usage logs. Valid
success cases retain one task, one committed reservation and one log: plan
7/7/7 usage with 50 credits; credits-only 8/8/8 with 43 credits; mixed 8/8/8 with
46 credits. Cancelled/failed retain released reservations and no new usage.
Exact success replay after ownership transitions and takeover causes no charge
or durable mutation. Synthetic invalid reservation state is rejected, not repaired.

## Commit gap, preservation and release limits

A dedicated delayed-commit test acquires while eligible, observes actual expiry
with the transaction still open, then commits the now-expired token. Subsequent
renewal/relinquishment reject it and a new acquisition can take over. Thus the
fix establishes fresh eligibility after locks, **not validity at commit or at
future consequential work**. Returned tokens stay provisional until successful
commit; uncertain outcomes require authoritative reread. Future work must
independently revalidate committed ownership and current expiry. PostgreSQL's
wall clock is the selected authority; no stronger monotonic-clock guarantee or
clock-adjustment policy is claimed.

Teardown left zero task/reservation/log/subscription/user records. Task-owned
PostgreSQL is stopped and only its independently verified disposable data
directory is removed before publication. Synthetic logs remain outside Git.
The protected historical S2-07 directory was never opened or reused. Outer dirty
gitlinks/staging/docs/scratch, frontend `AGENTS.md` and independent branches are
preserved. Disk remains about 9.7 GiB free. Publication is one new draft stacked
PR on PR #23's branch, with direct parent `6bba340cec1ecfb7c3cf08cb96e0ea1891825d08`.
Returned GitHub checks are separate from local PostgreSQL proof.

No model, schema, migration, billing, legacy OCR/Redis/guest/free behavior,
storage interface, scheduler or production adapter changes. No physical DELETE
fencing, cleanup-state/uncertain-result acknowledgment, orphan discovery,
janitor protocol discrimination or free-mode journal is implemented. SQL leases
cannot revoke external storage operations already issued. No activation or
deployment occurs. **This does not fix the active M1-S2-07 charged-without-artifact
inconsistency.**
