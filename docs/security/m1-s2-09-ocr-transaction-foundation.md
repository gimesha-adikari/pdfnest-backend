# M1-S2-09 — Dormant OCR transaction foundation

## Scope and result

This is a development-only PostgreSQL foundation based directly on design commit `b48df9910322a89c38a0e6afcdef7867f53704be`. It implements neither the durable OCR protocol nor a fix for the charged-without-artifact runtime bug. The accepted design documents remain unchanged.

Only opt-in isolated tests use the new OCR model/repository. No handler, startup service, migration registry, task callback, Redis operation, storage path, guest policy, retention policy or feature flag adopts it. `/api/ocr/jobs` continues to use the legacy runtime. M1-S2-07's known-bad characterization assertions still pass as characterization, not remediation.

## Original billing contract and baseline

Reviewed `internal/billing/service.go`, `init.go`, `janitor.go`, guest quota and free-mode tests; `config/database.go` and `migrations.go`; OCR R2 controller/tests; task registry and authorization; and Studio transaction/locking patterns as reference only.

- `ReserveWithTaskID` opens its own transaction, locks the registered user's subscription, synchronizes windows and accounts for still-active reservations before capturing plan/credit allocation. Task reservations expire after thirty minutes. Non-task reservations use six hours. Free mode returns an empty ID before database access.
- Legacy `Commit` locks the reservation, returns nil for any non-reserved status, conditionally commits it, then locks the subscription. It synchronizes all three windows, increments each plan counter, deducts credit allocation with the existing zero clamp, and inserts one usage log. Work count prefers pages and otherwise images.
- Repeated legacy commit is a no-op. Commit after release is also a no-op. Missing legacy commit returns record-not-found. Legacy release updates only reserved rows and is a no-op for missing/committed/released records; it does not refund.
- Async wrappers continue to dispatch by recorded reservation kind, including legacy blank-kind resolution. Guest quota remains Redis-backed. Mode changes do not reinterpret nonempty reservations. Empty-ID commit/release remain no-ops without database/quota access.

Before modifying shared billing source, the existing relevant suites plus new literal legacy characterization passed against disposable PostgreSQL: billing **51 top-level / 58 including subtests**, tasks **29 / 45**, OCR **15 / 32**; zero failures/skips. `TestLegacyTransactionFoundationBaseline` ran unchanged before and after extraction. It checks plan-only, credit-only and mixed allocations, exact counters/credits, usage count/credit flag/work count, duplicate settlement, commit-after-release and missing-reservation behavior.

## Changed-file inventory

| File | Change |
| --- | --- |
| `internal/billing/service.go` | Replace only the existing settlement arithmetic block inside legacy Commit with a call to the shared locked-reservation implementation. Legacy transaction opening, reservation lock, state checks and public return behavior remain. |
| `internal/billing/transaction.go` | Caller-owned transaction validation, exact reservation binding/locking, strict settlement/release outcomes, and the single shared settlement arithmetic implementation. |
| `internal/billing/transaction_baseline_test.go` | Literal before/after legacy behavior characterization. |
| `internal/billing/transaction_test.go` | Strict binding, state, allocation, window, transaction, rollback and release qualification. |
| `internal/billing/service_test.go`, `free_mode_test.go` | Bound existing test pools to four connections/two idle, close them and restore prior globals. No assertion weakening. |
| `internal/ocr/finalization_model.go` | Unexported dormant model and artifact metadata descriptor; no production schema registration. |
| `internal/ocr/finalization_repository.go` | Unexported transaction-only creation, locking, successful decision and cancellation/failure primitives. |
| `internal/ocr/finalization_repository_test.go` | Guarded test-only schema, independent observer connection, constraints, atomicity, deterministic lock races and SQL failure injection. |
| `internal/ocr/guest_async_integration_test.go` | Bound/close the existing database test pool. Guest integration assertions and runtime are unchanged. |
| This document | Evidence, stage boundary and remaining gates. |

## Dormant model and schema constraints

`ocrFinalization` has a task primary key, fixed protocol version, owner UUID, unique database reservation binding, exact captured tool/allocation, decision state/revision, processing claim/lease, artifact metadata, safe code and timestamps. Recovery/retention fields are nullable placeholders with no active policy values.

SQL constraints cover protocol, reservation kind, tool, state, positive revision, internally consistent nonnegative allocation, nonempty task ID, terminal timestamp, bounded safe code, and complete ready/succeeded metadata. A partial unique output-key index excludes the empty pre-artifact key. No cascading ownership relation or account-deletion policy is introduced.

The descriptor stores private artifact ID/key, SHA-256, byte size, pages, MIME, validation version and verification time. The repository does not upload, parse, read back or independently establish those bytes. Tests supply synthetic metadata. A future validator/coordinator must supply genuine evidence before any activation; a matching metadata record alone is not artifact-publication qualification.

Descriptor/binding immutability is enforced through the repository API: no update operation replaces these fields; success compares every descriptor component. Direct privileged SQL can still change valid metadata. A future reviewed migration/authority boundary must consider stronger database protection; no trigger or production migration is registered here.

### Constraint finding during qualification

A direct SQL negative test showed that a CHECK expression with nullable metadata could evaluate to NULL and accept an invalid ready record. Explicit NOT NULL fields and a nonzero verified timestamp now close that path. The negative test failed, then passed against the final fresh test schema.

An intermediate rerun also demonstrated that GORM AutoMigrate did not tighten the already-created test table's nullable columns/check definition. Only the empty foundation table in the new task-owned database was recreated under an explicit database/port/empty-table guard. Final creation and negative tests passed, and information-schema inspection confirmed all artifact fields non-nullable. This is not a production migration strategy; future managed DDL requires separate review.

## Transaction ownership and APIs

`RequirePostgresTransaction` rejects nil, errored, non-PostgreSQL and autocommit handles; the connection pool must implement GORM's transaction commit interface. It does not begin, commit or roll back anything. Callers must propagate errors to their surrounding transaction. A closed transaction fails through normal database errors.

`ReservationBinding` captures reservation ID, task, owner, database kind, tool, total/plan/credit units. `LockReservationInTx` locks that exact row and compares every binding/allocation field. It distinguishes missing, mismatched, reserved, committed, released and unknown state. New strict methods never consult `config.DB` or the current billing mode. A test sets `config.DB=nil` after acceptance and successfully settles with the supplied transaction.

- `SettleReservationInTx`: requires an eligible reserved allocation and nonzero decision time before reservation expiry. Committed and released are distinct errors, not a new success. Calls the same private arithmetic function as legacy Commit.
- `ReleaseReservationInTx`: changes only that exact reserved allocation inside the caller transaction. Committed/released are explicit outcomes; it is not compensation.
- `commitLockedReservation`: the sole settlement arithmetic implementation. Its conditional-update result is retained so legacy Commit preserves its old no-op behavior while strict settlement can fail closed.

No transaction-aware reservation-creation protocol is implemented. Tests create reservations through existing `ReserveWithTaskID`, then create dormant rows. Atomic production acceptance, idempotent transport creation and schema activation remain later stages.

## Joint decision semantics

All OCR repository operations accept a caller-owned PostgreSQL transaction; none opens an independent transaction.

Creation validates a permitted nonterminal model and locks/verifies its exact reserved allocation before insertion. `ready` creation in tests represents an already-prepared descriptor; it is not a live processing pipeline.

Successful decision ordering:

1. Lock task using `FOR UPDATE`.
2. Verify protocol, owner, task, reservation, processing claim, expected state/revision, time and exact descriptor.
3. Lock/validate reservation and captured allocation.
4. Lock subscription and apply existing settlement arithmetic/usage insertion.
5. Conditionally update task state/revision/decision timestamp.
6. Caller commits both, or rolls both back.

Cancellation/failure locks the same task, verifies expected state/revision/binding, releases the exact reserved allocation, and conditionally updates the decision in the same transaction. Terminal decisions cannot conflict. Late cancellation rejects succeeded state. A successful retry with the same descriptor and original/resulting revision verifies committed reservation state without repeating billing, even across a later usage-window boundary.

Lock order for decisions is **task → reservation → subscription**. Creation has no existing task row to lock and verifies its reservation before insertion; it is not a public adoption path.

## Executable atomicity and concurrency evidence

All durable assertions use a fresh independent observer pool after transaction completion; selected tests also confirm that uncommitted changes remain invisible. Pools are bounded. No SQLite is used.

| Case | Observed result |
| --- | --- |
| Plan / credit / mixed success | Succeeded + committed; exact expected counters/credit deductions; one usage row; descriptor unchanged |
| Cancellation or failure first | Cancelled/failed + released; zero charge; later success rejected |
| Success first, cancel waiting | PostgreSQL lock wait observed; successful transaction commits; late cancellation rejected |
| Cancel first, success waiting | Lock wait observed; cancellation commits; success rejected without charge |
| Finalizer rolls back while cancel waits | Cancellation then commits; released allocation and no usage/credit effects |
| Two finalizers | One decision/charge; waiting identical retry succeeds without another update/usage row |
| SQL error after settlement, at task update | Test-only SQL trigger observes the changed reservation and raises; task, reservation, counters, credits and log all roll back |
| SQL error during cancellation decision | Release and task update roll back together |
| Conflicting descriptor retry | Rejected; original successful metadata remains |
| Missing / committed / released / mismatched reservation | No new successful decision or second charge |
| Reserve concurrent with finalization | Real legacy reserve waits on subscription lock; after success, allocates the remaining one plan unit plus six credits correctly |
| Legacy release concurrent with finalization | If success holds the lock first, release becomes a no-op. If legacy release wins first, strict finalization rejects released state; dormant row remains non-successful |

The last case deliberately does not redirect the legacy janitor/release path. It demonstrates why this foundation must remain dormant until coordinated adoption. No inconsistent successful row is accepted, but this stage does not reconcile such externally released dormant rows.

Channels identify transaction connections; `pg_stat_activity.wait_event_type='Lock'` confirms that the competing writer is actually blocked before the winner commits/rolls back. Bounded polling observes this barrier; sleeps do not create the race. No deadlock or race-detector finding occurred in the exercised cases. This is bounded concurrency evidence, not proof against every future caller/lock pattern.

## Isolated runtime and exact checks

New task-owned PostgreSQL **18.6**, loopback `127.0.0.1:5548`, database `test_m1_s209`, synthetic role `m1_s209`; verified with server-reported database/address/port. The preserved S2-07 data directory was not opened or repurposed.

Real Redis from the already-installed `redis:7-alpine` image ran in a new task-owned container mapped only to `127.0.0.1:6388`, with persistence disabled. Database 0 served existing guest integration; database 15 served S2-07 fault-injection tests. No application server, real OCR worker, external store, production billing or customer account was used.

Both task-owned services were stopped after verification, and the Redis container was removed. Only the newly created PostgreSQL data directory is eligible for guarded removal; the earlier S2-07 directory remains preserved. Sanitized local test logs stay outside Git. Disk availability was 9.9 GiB before work and approximately 9.8 GiB after verification.

The new OCR harness requires explicit loopback IP and a `test_` database name, rejects query overrides, and uses the existing guarded PostgreSQL qualification harness. Test-only AutoMigrate creates the dormant table. Existing pool helpers now close connections and limit them to four/two idle; the independent observer uses two/one idle. Packages run serially.

Environment for local test execution (synthetic, non-production):

```sh
export DATABASE_URL='postgres://m1_s209@127.0.0.1:5548/test_m1_s209?sslmode=disable'
export GIM6_TEST_DATABASE_URL="$DATABASE_URL"
export PLATEN_M1_BILLING_TEST_DATABASE_URL="$DATABASE_URL"
export REDIS_URL='redis://127.0.0.1:6388/0'
export PLATEN_M1_BILLING_TEST_REDIS_URL='redis://127.0.0.1:6388/15'
export BILLING_MODE=normal

go test -p 1 ./internal/billing/... ./internal/tasks/... ./internal/ocr/... -count=1 -json
go test -race -p 1 ./internal/billing/... ./internal/tasks/... ./internal/ocr/... -count=1 -json
go vet ./internal/billing/... ./internal/tasks/... ./internal/ocr/...
git diff --check
```

Final regular and race runs: billing **55 top-level / 77 including subtests**, tasks **29 / 45**, OCR **21 / 96**; zero failures/skips. The 6 new OCR foundation top-level tests account for 64 events including subtests. Existing guest, free-mode, mode-transition, callback and S2-07 characterization cases executed without opt-in skips. Go vet passed; formatting and whitespace checks passed. Initial new-API build failures and the deliberate constraint-red experiment are development evidence, not passing checks; the final gates supersede them.

No frontend/browser suite was repeated because no frontend code, public status, HTTP request or task protocol changed. Actual PDF artifact validation/publication remains covered by unchanged legacy tests, not by these metadata-only foundation tests. No full OCR algorithm or external object-store integration is claimed.

## Runtime-inert and migration proof

Source occurrence/call-site checks locate new OCR types/repository only in their defining files and `finalization_repository_test.go`. Strict billing entry points are called only from that dormant repository or tests. The only new helper reached by legacy runtime is the behavior-preserving shared arithmetic extracted from Commit.

Diff checks against the design parent confirm no change to `main.go`, `config/database.go`, `config/migrations.go`, OCR submission/routes, task package, storage package, billing initialization, janitor or guest quota implementation. No new task prefixes are generated. The model is absent from `schemaModels`, managed version and validation. Accepted design documents and unrelated branches remain unchanged.

The checked-in CI workflow triggers only pushes/PRs involving `master`/`main`; its Go step compiles rather than running these PostgreSQL tests. The stacked PR's actual checks must be reported separately. A neutral external analyzer or absent Actions run is not PostgreSQL/race-test evidence.

## Remaining stages and limits

Unimplemented: production migration; atomic acceptance/reservation creation; protocol dispatch; prepared-artifact persistence/readback; coordinator-only settlement adoption; janitor/callback guards; Redis projection; free journal; reconciliation and leases; retention/orphan cleanup; HTTP authorization integration; mixed-version cutover and rollback qualification.

The model's immutable API is not a defense against arbitrary privileged SQL. Processing/recovery lease fields have no executor. No policy is selected for guest routing, retention, account deletion, refunds or catastrophic Redis loss. The original charged-without-artifact defect remains present and characterized. Independent preview-owner and Studio snapshot branches remain separate unreleased work.

Next approval should review this foundation before authorizing a separate migration/adoption stage. No activation, rollout, merge or deployment is authorized by these results.
