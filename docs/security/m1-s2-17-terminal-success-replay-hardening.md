# M1-S2-17 — Dormant terminal-success replay hardening

Qualified on 2026-10-16 against exact parent
`7f38034e4037556ab56db908dcf78dedf0fef852` (draft PR #21,
`milestone-1/ocr-success-replay-qualification`), whose direct parent is
`725f99bbdcf951d810e7781bfb7d471734970d45`. Local HEAD, the advertised remote
branch and PR head matched before edits; PR #21 was open/draft with the expected
base. The backend was clean. Work uses the existing checkout on the new branch
`milestone-1/ocr-terminal-replay-hardening`; no rebase, merge or worktree.

## Fixed dormant repository behavior

Source at the parent confirms that `lockFinalization` compared every trusted
`req.Now` with `UpdatedAt` before `succeedInTx` could acknowledge a previously
committed success. M1-S2-16 case D therefore rejected an original-time replay
after synthetic maintenance advanced `UpdatedAt`. The same source allowed
`updateLifecycle` to update an already-terminal row if given its matching state
and revision. These are dormant repository contracts, not production incidents.

Only `finalization_repository.go` and its maintenance/replay test change:

- `lockFinalizationWithReplay` shares the existing task lock and binding checks.
  Only `succeedInTx` enables its replay option. For a persisted `succeeded` row
  and expected `ready`, chronology uses nonzero persisted `DecidedAt` and rejects
  `req.Now < DecidedAt`. Zero request times remain invalid. Other calls through
  `lockFinalization`, and initial success from `ready`, still use `UpdatedAt`.
- `succeedInTx` preserves the exact artifact descriptor, processing claim,
  expected-state/revision and committed-reservation checks. A matching replay
  returns without calling settlement or updating any row. Acknowledgment time
  describes the authoritative decision, so unrelated later bookkeeping cannot
  invalidate an otherwise exact retry. Time comes from a trusted internal
  coordinator; it is not assumed to be an HTTP-client value.
- `updateLifecycle` rejects `succeeded`, `cancelled` and `failed` predecessors
  before adding revision/time entries to the supplied update map or issuing SQL.
  Normal terminal creation still starts from a locked preterminal row.

Task/owner/reservation identity, protocol/tool, captured allocation, complete
artifact identity/key/hash/size/pages/MIME/validation/verification time, claim and
the existing current-revision/current-minus-one replay predicate are unchanged.
Only expected `ready` acknowledges success. Cancelled/failed decisions and
noncommitted, missing or mismatched reservations cannot authorize it. Lock order
remains task → reservation → subscription for initial settlement; acknowledgment
uses task → reservation. No billing source or arithmetic changed.

## Lifecycle revision ownership and future recovery

**Recovery bookkeeping must use independent claim/token fencing and must not
advance the lifecycle decision revision.** `Revision` belongs to authoritative
task transitions and freezes at a terminal decision. A separate coordinator
design must qualify concurrency using recovery metadata such as `RecoveryClaim`.
There is no need to duplicate the decision revision in a new column under this
contract. This selected rule supersedes M1-S2-16's speculative recommendations
for a decision-revision column or relaxed revision matching; its historical
report remains unchanged.

The Go helper is not protection against privileged direct SQL. Its predicate
cannot reconstruct decision revision history after unsupported SQL changes:
current/current-minus-one matching remains exactly as before. Retained tests
prove original revision 4 fails after SQL changes 5→6, and captured revisions
4/5 fail after 5→7; they do not prove detection of all privileged corruption.

## Characterization transition and accounting evidence

Original M1-S2-16 subtest names A–F remain, including both revision scenarios.
The request's C/E requirements correspond to historical D/C/E respectively.

| Coverage | PR #21 result | M1-S2-17 result |
| --- | --- | --- |
| A: exact replay, original or later time | Acknowledge | Preserved; resulting revision also checked |
| B: recovery metadata only, revision/time preserved | Acknowledge | Preserved; all metadata survives |
| C/E: synthetic SQL lifecycle revision increments | Reject captured old revisions | Preserved; unsupported ownership violation |
| D: later maintenance `UpdatedAt`, original decision time | Binding rejection | Corrected: acknowledge; keep newer time/recovery fields |
| F: owner/reservation/claim/artifact mismatch, cancelled/failed | Reject | Preserved |
| Time before actual decision, but after verification | Not separately covered | Reject |
| Helper update on all three terminal states | Not covered | Reject, even if caller commits afterward |
| Additional replay state/revision/task/zero-time and invalid reservation states | Partial earlier coverage | Reject without durable changes |
| Maintenance commits/rolls back while replay waits | Not covered | Acknowledge after actual row-lock barrier |

The barrier cases use separate transactions, PID channels, observed PostgreSQL
lock waits and `pg_blocking_pids` to identify the maintenance blocker. Bounded
polling/timeouts observe the barrier; no sleeps order the transactions. The
independent observer sees no uncommitted maintenance. Commit preserves all
maintenance values; rollback preserves the prior committed decision/accounting.

Full task/reservation/subscription/usage snapshots, read through the independent
observer pool after each relevant replay/rejection, verify no changes to decision
time/revision, artifact, recovery fields, allocation, credits, window reset times
or usage records. Each synthetic owner retains exactly one task and reservation.
The plan fixture remains succeeded/committed, revision 5, usage 7/7/7, 50 credits
and one log. Preexisting atomic tests additionally retain credits-only 8/8/8 and
43 credits, and mixed 8/8/8 and 46 credits, each with one log. Cancellation/failure
retain released reservations and zero new usage/logs. No physical storage proof
is inferred from these synthetic descriptors.

## PostgreSQL verification

Fresh task-owned PostgreSQL **18.6**, UTF-8, role `m1_s217`, database
`test_m1_s217_utf8`, bound only to `127.0.0.1:5557`; server version, database,
address and port were queried directly. Guarded harness schema creation and
bounded primary/independent observer pools are unchanged. No Redis, application,
worker, frontend or production service was started.

```sh
export PLATEN_M1_BILLING_TEST_DATABASE_URL='postgres://m1_s217@127.0.0.1:5557/test_m1_s217_utf8?sslmode=disable'
go test -p 1 ./internal/ocr -run '^TestOCRSuccessReplayMaintenanceCharacterization$' -count=1 -timeout=3m -json
go test -race -p 1 ./internal/ocr -run '^TestOCRSuccessReplayMaintenanceCharacterization$' -count=1 -timeout=4m -json
go test -p 1 ./internal/ocr -run '^TestOCRFoundationAtomicDecisions$' -count=1 -timeout=3m -json
go test -p 1 ./internal/ocr -run '^TestOCR(Lifecycle|Foundation(CompetingDecisions|ReleaseAndReserveContention|EarlyCancellation|ReservationStateRejections))' -count=1 -timeout=3m -json
go vet ./internal/ocr
gofmt -l internal/ocr/finalization_repository.go internal/ocr/finalization_maintenance_replay_test.go
git diff --check
```

| Final test run | Top-level passes | Leaf cases | Pass events including groups/parents | Failed / skipped |
| --- | ---: | ---: | ---: | --- |
| Maintenance/replay | 1 | 25 | 30 | 0 / 0 |
| Maintenance/replay, race | 1 | 25 | 30 | 0 / 0 |
| Atomic decisions, plan/credits/mixed | 1 | 30 | 31 | 0 / 0 |
| Affected lifecycle, decisions and contention | 14 | 102 | 113 | 0 / 0 |

Before source edits, the new expectations failed specifically on historical D,
all three terminal helper updates and the maintenance-commit barrier replay;
the other leaf cases passed. The final runs above passed with no race warnings.
Vet and whitespace checks exited 0; gofmt reported no files. A bounded read-only
review found no actionable defect. Graph tools were unavailable; source evidence
uses targeted reads/searches and makes no graph coverage claim.

## Preservation and release limits

Only the two Go files and this report are authorized changes. Model, schema,
migrations, billing, HTTP/Redis legacy route, guest/free policy, storage, worker,
frontend and startup/configuration stay unchanged. M1-S2-07 known-bad assertions
are unchanged; that legacy suite was not rerun because these dormant changes
do not affect its runtime and would require unrelated Redis setup.

Test fixture teardown left zero tasks, reservations, logs, subscriptions and
users in the disposable database. The task-owned PostgreSQL instance is stopped
and only its verified disposable data directory is removed before publication.
The historical stopped M1-S2-07 directory was never opened, reused or modified.
Outer dirty gitlinks/staging/docs/scratch, unrelated local branches and frontend
unstaged `AGENTS.md` are preserved. Disk availability was about 9.8 GiB initially
and 9.7 GiB after test execution. Sanitized test logs remain outside Git.

**Fixed dormant repository behavior** does not implement **future recovery
behavior**. There is no recovery coordinator, arbitrary recovery-field update
API, cleanup/deletion lease, new column or migration. Janitor protocol
discrimination, cleanup-state durability, external storage fencing, download
races, late writes/orphan inventory, real R2 qualification, managed migration and
free-mode recovery remain release gates. The live M1-S2-07 charged-without-artifact
defect remains unresolved. This branch is published only as a new draft stacked
PR targeting PR #21's branch, with no merge, activation or deployment. GitHub
checks are separate from the local PostgreSQL results; the checked-in Actions
workflow targets master/main, so absent checks are not a CI pass.
