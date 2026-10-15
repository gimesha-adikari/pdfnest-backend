# M1-S2-10 — Dormant OCR lifecycle and cancellation foundation

## Scope and outcome

Development-only lifecycle qualification based directly on `5a25755e867f87c989ac3d5f60e0350609fcfa0a` (PR #15). This extends the dormant SQL foundation; it does **not** activate the M1-S2-08 protocol or fix the live M1-S2-07 charged-without-artifact failure.

No HTTP handler, Redis Lua/status/cancellation route, public DTO, download controller, billing arithmetic/callback/janitor, OCR processor, storage upload/cleanup, startup service, feature flag or managed migration changes. No new task-ID prefix, free-mode journal, reconciler, guest restriction, retention default or refund is introduced. Only guarded tests create this model's schema.

Artifact evidence in these tests is **synthetic trusted metadata**. The repository does not parse a PDF, perform OCR, upload an object, read it back or prove physical artifact durability. Those remain separately gated.

## Source investigation and pre-change evidence

Reviewed the dormant model/repository/tests; `internal/billing/transaction.go` and the existing Reserve/Commit/Release contract; `internal/ocr/r2_jobs.go`; task registry/cancellation Lua, routes, download controller and owner/capability tests; the M1-S2-09 report; and all three accepted M1-S2-08 design documents. Graph tools were unavailable, so evidence uses direct source and bounded call-site searches.

Before modifying repository source, `TestOCRLifecyclePreImplementationLimits` ran against disposable PostgreSQL with two passing characterization subtests:

1. A directly created ready fixture had `VerifiedAt=T0`. Recreating it with `VerifiedAt=T0+1s` returned `errFinalizationBinding` and inserted no row. The old ready-creation guard rejected `VerifiedAt.After(CreatedAt)`.
2. A synthetic processing row with a processor claim remained processing/reserved when cancellation omitted that claim: `endInTx` returned `errFinalizationBinding`; counters, credits and usage remained unchanged.

No accepted-to-claimed-processing, intent, or processing-to-ready repository transitions existed. This was a missing dormant implementation boundary, not a reproduced active-route vulnerability. The characterization test was subsequently replaced by the lifecycle safety tests below. Its initial temporary log was lost when the temporary environment disappeared across session resume; the successful execution was observed before source changes.

Direct creation in ready or processing is now deliberately unsupported, including equal-time and later-time ready shortcuts. Existing foundation fixtures use accepted → processing → intent → ready instead of manufacturing a prepared row. Historical design and S2-09 evidence documents remain unchanged.

## State machine and repository contracts

Every method requires a caller-owned PostgreSQL/GORM transaction and propagates errors. None opens, commits or rolls back an independent transaction. Callers must roll back on error; returned claims/capabilities and mutated fixture values from a rolled-back transaction must be discarded.

| Operation | Allowed predecessor | Binding/authority | Durable change |
| --- | --- | --- | --- |
| `createInTx` | No task row | Exact reserved, unexpired database reservation; owner, task, tool and captured allocation | Accepted, revision 1, acceptance time, no claim or artifact; server-generated cancellation capability digest |
| `claimInTx` | Accepted at expected revision | Trusted internal processor invocation, exact owner/task/reservation; eligible reservation | Processing, revision +1; random server-generated claim, processing timestamp and explicit lease deadline |
| `recordIntentInTx` | Processing without an intent | Current unexpired processor claim and expected revision | Immutable synthetic descriptor with zero verification timestamp; intent time; revision +1, state remains processing |
| `readyInTx` | Processing with intent | Current unexpired claim/revision; descriptor matches intent; complete verification metadata and chronology | Ready, verification/ready times, revision +1 |
| `succeedInTx` | Ready | Current unexpired claim/revision, exact complete descriptor and database reservation | Joint task success, reservation settlement, subscription updates and usage insertion; revision +1 |
| `cancelInTx` | Accepted, processing or ready | Trusted verified owner **or** exact task capability; expected state/revision | Joint cancellation and exact reserved-allocation release; clear processor claim/lease; revision +1 |
| `failWorkerInTx` | Processing or ready | Current unexpired worker claim, expected revision, bounded safe code | Joint failure/release; clear claim/lease; revision +1 |
| `failDeadlineInTx` | Accepted, processing or ready | Separate trusted task-specific deadline authority; expected revision; stored lease or reservation deadline reached | Joint failed/released decision with `DEADLINE_EXCEEDED`; clear claim/lease |
| Same-result success replay | Succeeded | Same claim, exact descriptor, original/resulting decision revision; exact reservation already committed | No update, window reset, usage insertion or second charge |

No terminal reset exists. Cancelled/failed tasks cannot acquire another claim, replace intent, become ready or succeed. A second ready intent cannot replace the first. Same-result successful replay remains possible after the lease/reservation deadline because it observes a previously committed decision rather than settling anew. A conflicting descriptor or claim is rejected.

### Chronology

Separate fields record acceptance (`CreatedAt`), processing (`ProcessingStartedAt`), intent (`ArtifactIntentAt`), verification (`Artifact.VerifiedAt`), readiness (`ReadyAt`) and terminal decision (`DecidedAt`). `UpdatedAt` is monotonic across repository operations; inputs older than the stored update are rejected.

- Claim time is at or after acceptance. Its explicit deadline must be later than claim time and no later than the existing reservation expiry. Tests supply one minute; there is no production lease default or reclaim policy.
- Intent follows processing and contains a nonempty artifact ID/key, lowercase SHA-256, positive size/pages, PDF MIME and validation version 1; verification remains zero.
- Verification is strictly after acceptance, at or after intent, and no later than readiness. Readiness/decision cannot move time backward.
- Expiration never grants a new processor claim. There is no automatic renewal/reclaim operation. A trusted deadline invocation can fail/release an expired record, but no deadline worker is started.
- Times and evidence must come from a trusted future coordinator, not client-submitted timestamps. Future clock/storage validation is an activation gate.

## Trusted cancellation boundary

`finalization_authority.go` separates three kinds of authority:

1. **Worker:** the current randomly generated 256-bit processor claim, exact task/owner/reservation and revision; required for readiness, successful settlement and worker failure.
2. **Owner/capability cancellation:** `ownerCancellation` invokes a trusted `cancellationIdentityVerifier` before entering the SQL operation. A future server adapter must verify the authenticated request and return its canonical identity; merely echoing a client owner string is not authentication. The private authority is then compared with the locked task's owner. Alternatively, a task-specific 256-bit random capability is checked using SHA-256 and constant-time digest comparison under the lock. Only its digest is stored. The raw token is returned transiently at acceptance and is never logged, added to a DTO or used as a worker claim.
3. **Deadline/recovery failure:** `trustedDeadline` uses a separate trusted adapter and task-specific authority. The repository checks that authority and the stored deadline under the task lock. It cannot perform ordinary owner cancellation or successful settlement.

There is **no production identity/deadline adapter** in this stage. Tests use explicit synthetic trusted adapters; they do not qualify JWT/HTTP authentication. An arbitrary owner value in an unverified authority, empty authority, rejected verifier, wrong owner, wrong token or another task's token is rejected. Package-internal trusted Go code remains part of the trust boundary; these types are not a sandbox against malicious code in the same package.

The cancellation request has no processor claim, owner-ID argument or reservation-ID argument. It carries task ID, internal expected state/revision, time and authority. The row supplies the exact reservation binding after authorization. Internal revisions and private metadata are not exposed to existing clients. A future coordinator must re-read/retry an authorized cancellation if a nonterminal transition changes its expected revision; stale requests do not silently override it.

Cancellation and success lock the same task first. If cancellation commits first, release and cancellation are atomic and the claim is cleared; stale worker operations fail. If success commits first, the reservation is committed once and cancellation returns a terminal conflict without release or refund. A rollback wins no decision.

## Billing transaction and concurrency

Billing implementation is unchanged from PR #15. Success uses task → exact reservation → subscription locks inside one caller-owned transaction. It preserves captured plan/custom-credit allocation, three usage windows, credit deduction, usage insertion and successful-replay idempotency. Cancellation/failure uses task → exact reservation and strict release in that same transaction. Missing, released, committed or mismatched reservations cannot authorize a new success.

Existing plan-only, credits-only and mixed-allocation tests now prepare through the real dormant lifecycle. Their literal expected totals remain: seven units charged as plan (7/7/7 usage, 50 credits), credits (8/8/8 prior usage, 43 credits), or mixed (8/8/8 usage, 46 credits), with one usage log. Cancellation/failure leaves original usage and 50 credits with zero logs. No pricing or refund semantics change.

Concurrency tests use separate PostgreSQL connections, channels, explicit transactions and observed `pg_stat_activity.wait_event_type='Lock'`, not sleeps to create a race. Timeouts bound deadlock/failure detection. Independent observer pools verify committed task state/revision, reservation, all usage counters, credits, usage-log count and every descriptor component; selected tests also verify uncommitted work is invisible.

Evidence covers competing claims (commit and rollback), readiness versus cancellation in both orders, readiness rollback while cancellation waits, success versus cancellation in both orders, finalization rollback while cancellation waits, two finalizers, and legacy reserve/release contention. Expected revision conflicts require a fresh authorized retry, never an implicit reset. No unresolved deadlock was observed.

Test-only SQL triggers abort at readiness update or after settlement/release but before the terminal task update. Task, reservation, counters, credits and logs remain at their preceding committed state after rollback. No runtime fault-injection hook is introduced.

## Dormant model and SQL checks

Added processing/intent/ready chronology fields and a non-null cancellation capability digest. Existing task primary key, unique reservation binding and partial unique nonempty artifact-key index remain. New checks enforce legal state shapes, complete intent/ready metadata, valid chronology and invalidated claims in cancelled/failed rows.

Nullable timestamp checks explicitly require the relevant timestamp to be non-NULL in processing/ready/succeeded states. Artifact fields and capability digest are NOT NULL. Direct SQL negative tests cover NULLs, zero/early verification, invalid MIME/version/count/size/digest/key, missing lifecycle timestamps, invalid state, duplicate task/reservation and conflicting artifact key. PostgreSQL CHECK accepting NULL is not treated as validation. Actual PostgreSQL constraint inspection confirmed `ocr_lifecycle_shape`, `ocr_chronology`, `ocr_intent_artifact`, existing ready checks and NOT NULL columns.

Repository APIs enforce transition/descriptor immutability. These CHECKs validate row shape, not historical transitions; privileged direct SQL can still replace otherwise valid metadata. Future production DDL, privileges and immutability enforcement require review. This task adds no trigger, migration or production table registration.

## Test environment and results (2026-10-15)

Loopback PostgreSQL **18.6**, new disposable UTF-8 database `test_m1_s210_utf8` at `127.0.0.1:5549`, synthetic role/accounts/reservations; bounded pools (four primary/two observer connections). Task-owned Redis 7 Alpine at `127.0.0.1:6389`, databases 0 and 15 for existing compatibility/characterization tests. Existing Redis on 6379 was untouched. No Go HTTP server, OCR worker, frontend, production storage, billing provider or database was used.

The initial temporary PostgreSQL environment disappeared across session resume. Its replacement initially used SQL_ASCII; the focused run failed at the existing billing schema helper with `simple protocol queries must be run with client_encoding=UTF8` (129 failed events including parents, two passed). A separate fresh UTF-8 test database corrected the environment; no existing database was wiped or repurposed. A subsequent expanded assertion used structural time equality rather than instant equality and failed across UTC/local representations (32 failed events including parents, 274 passed across the three packages); correcting the assertion preserved chronology and produced the passing final runs below. Neither failed run is counted as passing.

Final command environment, containing only synthetic local configuration:

```sh
export DATABASE_URL='postgres://m1_s210@127.0.0.1:5549/test_m1_s210_utf8?sslmode=disable'
export GIM6_TEST_DATABASE_URL="$DATABASE_URL"
export PLATEN_M1_BILLING_TEST_DATABASE_URL="$DATABASE_URL"
export REDIS_URL='redis://127.0.0.1:6389/0'
export PLATEN_M1_BILLING_TEST_REDIS_URL='redis://127.0.0.1:6389/15'
export BILLING_MODE=normal

go test ./internal/ocr -run '^TestOCR(Lifecycle|Foundation)' -count=1 -json
go test -p 1 ./internal/billing/... ./internal/tasks/... ./internal/ocr/... -count=1 -json
go test -race -p 1 ./internal/billing/... ./internal/tasks/... ./internal/ocr/... -count=1 -json
go vet ./internal/billing/... ./internal/tasks/... ./internal/ocr/...
git diff --check
```

| Final check | Top-level passed | Passed including subtests | Failed / skipped |
| --- | ---: | ---: | --- |
| Focused lifecycle + accepted foundation | 16 | 152 | 0 / 0 |
| Full billing, normal and race runs each | 55 | 77 | 0 / 0 |
| Full tasks, normal and race runs each | 29 | 45 | 0 / 0 |
| Full OCR, normal and race runs each | 31 | 184 | 0 / 0 |
| Each full run total | 115 | 306 | 0 / 0 |

Race detector: no race diagnostics. Vet: exit 0, no diagnostics. Diff checks: exit 0. Opt-in PostgreSQL and Redis characterization tests actually executed; no environmental skip is counted as qualification. Original M1-S2-07 known-bad assertions remain unchanged and pass as characterization. This is not a fix for their live-runtime outcomes.

No frontend/browser tests were rerun because no public behavior, protocol or frontend source changed. Synthetic descriptors are not independently checked PDF artifacts, and the tests do not exercise a real OCR/storage pipeline or process-death reconciliation.

## Runtime inactivity, preservation and release boundary

Bounded all-Go call-site searches found non-test references to the new repository/model/authority only in the three dormant `internal/ocr/finalization_*.go` files. Calls and schema creation occur only in foundation/lifecycle test files. Exact diffs against the parent are empty for `internal/billing`, `internal/tasks`, `internal/ocr/r2_jobs.go`, `controller.go`, `r2_billing_consistency_test.go`, `config` and `main.go`.

`schemaModels`, managed schema version, migration runner/validator and startup remain unchanged. No feature flag, handler adapter, public task prefix or background process can activate the new path. Free-mode empty reservation IDs, guest accounting and all legacy billing callbacks retain their existing tests and implementations. This SQL repository remains database-reservation-only; no free/guest protocol is assumed.

Frontend HEAD remains `0f6fa13361778917afbeeef19e977f7bd8002944`, including its preserved unrelated unstaged `AGENTS.md` edit. Worker remains `09067d0a18985f791de27c142d8cc1a750bde598`. Outer Platen gitlinks, documentation, staging and scratch files are untouched. Only task-owned test infrastructure is stopped/removed. The preserved M1-S2-07 PostgreSQL directory is not inspected, deleted, reused or modified.

The implementation is published as a separate draft stacked branch based on PR #15, targeting `milestone-1/ocr-finalization-transaction-foundation`. PRs #11–#15, frontend #24 and worker #1 retain their independent branches. Nothing is merged, made ready or deployed. The backend Actions workflow only targets master/main and contains a build step, not these PostgreSQL tests; absent stacked-PR Actions are not a CI pass.

## Changed files and remaining gates

| File | Purpose |
| --- | --- |
| `internal/ocr/finalization_model.go` | Dormant lifecycle/capability fields and test-schema constraints |
| `internal/ocr/finalization_repository.go` | Accepted-only creation, claim, immutable intent, readiness, separate cancellation/worker/deadline failure, existing joint settlement |
| `internal/ocr/finalization_authority.go` | Private trusted identity/deadline adapters and exact task capability verification; no runtime adapter |
| `internal/ocr/finalization_repository_test.go` | Existing billing/atomicity assertions retained, realistic fixtures and stronger durable descriptor/time checks |
| `internal/ocr/finalization_lifecycle_test.go` | Chronology, malformed evidence, claim fencing, authority isolation, SQL NULL checks and deterministic lock races |
| This report | Pre-change evidence, contracts, actual results, limitations and dormant-stage proof |

Still unimplemented: genuine storage/PDF validation and readback, immutable object identity, artifact retention/cleanup ownership, atomic production acceptance, trusted production auth/clock adapters, schema migration, Redis projection/download authority, SQL reconciliation/recovery leases, free-mode journal, guest-route decision, generic janitor/callback exclusion, process restart/lost-ack handling, transport idempotency and coordinated rollout/rollback guards. Preview owner-isolation PRs #11/worker #1 remain interdependent and unreleased; Studio PR #12 is separate. F-01 platform coverage and broader F-02 workflows remain unqualified beyond prior bounded evidence.

**One next recommendation:** independently review this dormant lifecycle/authority contract and PostgreSQL evidence before authorizing a separately scoped storage-evidence adapter stage. No next stage is implemented here.
