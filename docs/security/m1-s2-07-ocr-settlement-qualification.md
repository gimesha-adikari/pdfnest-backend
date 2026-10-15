# M1-S2-07 — OCR settlement qualification (PARTIAL/WIP)

## Decision and application source

Application source tested: `e427c6093242bffb6f8e24edc3d8c6a412acf41f`, branch `milestone-1/queued-pdf-publication`, draft backend PR #13. This task adds test-only qualification and this report. It does **not** fix the reproduced billing inconsistency or change runtime behavior. Passing characterization assertions reproduce the current bad outcomes; they are not security acceptance tests for a corrected protocol.

The selected `/api/ocr/jobs` implementation validates and persists a generated PDF, commits the database reservation, then performs the Redis `COMPLETED` compare-and-set. PostgreSQL and Redis are separate transaction domains. No appropriate durable settlement/replay contract was found in the inspected task, OCR, billing, or reservation-model code.

## Actual reservation contract

- Database `Service.Commit` locks the reservation, conditionally changes `reserved` to `committed`, updates all three plan-usage counters, deducts reserved credits, and inserts one usage log in a single PostgreSQL transaction. A repeated commit, or commit of a released reservation, returns nil without charging again.
- `Service.Release` only changes `reserved` to `released`. It does not refund credits or reverse usage after commit. Cancellation and stale-task callbacks call this release operation; neither repairs a committed charge.
- Guest `CommitAsync` uses the reservation kind to dispatch to Redis-backed quota. Guest commit transfers pending units to used units and deletes the reservation. Release after that deletion is a no-op. Commit/release are individually idempotent, but they are not atomic with the task's terminal decision.
- Free mode returns an empty reservation ID before database/quota access. Empty-ID commit/release are no-ops. Existing nonempty reservations still settle by their recorded kind across mode changes.
- `/api/ocr/jobs` calls `ReserveWithTaskID` and records kind `database`; it does not select `ReserveAsync` using guest quota identity. The normal UI uses `requireAuth`, but backend `identity.Resolve` supplies `user_id` for guests too. A direct synthetic guest-context request is accepted, obtains an unpersisted database reservation, then fails billing finalization safely with no charge. This is an existing API/UI compatibility gap, not proof that the selected route supports guest quota billing. The separate guest-store tests qualify the related service contract, not guest routing through `/api/ocr/jobs`.
- The billing reservation has `TaskID` but no immutable artifact descriptor or durable terminal/replay decision. Usage logs do not directly identify the reservation being settled. No committed-reservation refund/reconciliation mechanism is provided by these operations.
- The download handler's existing billing callback ignores commit errors and runs before checking artifact availability. It cannot be used as a reliable repair protocol for this issue.

## Deterministic observations

All PDF-producing cases use the actual R2 OCR controller, real PostgreSQL reservation/usage operations, real Redis Lua transitions, the actual cancellation/download handlers, task-owned local storage, and a fake OCR processor returning a valid synthetic PDF. No worker, production billing, external object store, or customer file is used. One image costs seven units under the current tool estimate.

The database query callback pauses immediately before the locked reservation read in `Commit`. A go-redis process hook pauses immediately before the terminal EVAL, after the real SQL transaction has committed. The failure hook can reject that write, or execute it and then return an error to simulate a lost acknowledgement. Channel barriers establish ordering; bounded condition polling only waits for asynchronous cleanup, not for the race window.

| Case | Observed task | Reservation / usage | Artifact and download | Classification |
| --- | --- | --- | --- | --- |
| Cancellation before billing commit | CANCELLED | released; zero charge / usage logs | removed; 404 | Inconsistency not reproduced in this ordering |
| Cancellation after commit, before completion | CANCELLED | committed; seven plan units or seven credits; one usage log | removed; 404 | CONFIRMED inconsistency |
| Redis rejects terminal write after SQL commit | PROCESSING, then FAILED on forced stale recovery | committed; charge remains after release callback | removed; 404 | CONFIRMED inconsistency and ineffective existing recovery |
| Redis applies completion but acknowledgement is lost | COMPLETED | committed; one charge | cleanup removes the completed artifact; 410 | CONFIRMED additional availability/settlement inconsistency |
| Successful completion | COMPLETED | committed exactly once | retained and downloadable; 200 | Expected behavior confirmed |
| Two competing initial billing/terminal store calls | one COMPLETED transition accepted | one charge / one usage log | retained | Duplicate-charge defect not reproduced; this is store-level competition, not two supported worker executions |
| Cancellation after authoritative completion | remains COMPLETED | committed once | retained; 200 | Expected terminal immutability confirmed |
| Free mode, database unavailable | COMPLETED | empty reservation ID, no database operation | retained | Expected compatibility confirmed |
| Related guest cancellation before commit | CANCELLED | pending/used zero; reservation removed | no publication | Expected service ordering confirmed |
| Related guest cancellation after commit | CANCELLED | seven used quota units remain; reservation already deleted | no publication | Same irreversibility confirmed for guest service contract |

Database cases cover both plan allowance and exhausted allowance with custom credits: credits remain 50 before charge and become 43 after charge; each charged case has exactly one usage row. The Redis failure test explicitly ages the task timestamp and invokes real stale recovery; it does not wait for a timeout. Stale recovery changes the task to FAILED but cannot undo the charge. A synthetic lost-acknowledgement result is a controlled failure injection, not an observed production Redis incident.

The current cancel UI says the task was cancelled; inspected task UI/API text does not claim that a committed charge was refunded. Such a claim must not be added without authoritative settlement confirmation.

## Why no runtime reorder/refund patch was made

Moving billing after `COMPLETED` exposes a downloadable success before a potentially failing charge. A Redis FINALIZING flag alone remains subject to process death, stale-task handling, TTL expiry and Redis loss; no replay coordinator currently owns it. Retrying the final Redis write or retaining output in only the worker process cannot establish recovery after restart. Blindly subtracting counters/refunding credits changes billing policy and is unsafe across usage-window changes and subsequent account activity. No such patch is included.

A sound next design is a durable OCR finalization intent/decision associated with exact owner, task, reservation kind/ID, and immutable artifact metadata. Cancellation and settlement must share one authoritative durable decision; billing settlement and the durable completion/outbox decision should commit in the same database transaction for this database-backed route. Redis can then be repaired as an idempotent projection, while output is retained until replay is confirmed. This requires an explicit schema/retention/reconciliation and mixed-version rollout review; it is beyond a narrow reorder and was not implemented or published as billing infrastructure. Related guest routes need their own state-store mapping rather than assuming the quota and task Redis stores are identical.

M1-S2-07 is PARTIAL/WIP until that contract is approved and the bad-outcome characterizations become fail-closed recovery regressions. The accepted M1-S2-06 task authorization and artifact-validation controls are unchanged.

## Reproducible isolated commands and evidence

Use a disposable PostgreSQL database whose name begins `test_`, and a task-owned Redis on a nondefault loopback port. The tests reject non-loopback PostgreSQL/Redis targets. Optional real Redis is selected by `PLATEN_M1_BILLING_TEST_REDIS_URL`; otherwise the test uses Miniredis. Database tests explicitly skip without their opt-in URL.

```sh
export PLATEN_M1_BILLING_TEST_DATABASE_URL='postgres://m1_s207@127.0.0.1:5547/test_m1_s207?sslmode=disable'
export PLATEN_M1_BILLING_TEST_REDIS_URL='redis://127.0.0.1:6387/15'
export DATABASE_URL="$PLATEN_M1_BILLING_TEST_DATABASE_URL"
export GIM6_TEST_DATABASE_URL="$PLATEN_M1_BILLING_TEST_DATABASE_URL"
export REDIS_URL='redis://127.0.0.1:6387/0'
export BILLING_MODE=normal
go test -race ./internal/ocr -run '^TestR2BillingQualification' -count=1 -v
go test -p 1 ./internal/tasks/... ./internal/ocr/... ./internal/billing/... -count=1
go vet ./internal/tasks/... ./internal/ocr/... ./internal/billing/...
```

Observed: five focused top-level tests / sixteen leaf scenarios passed as characterization, with no race-detector findings. The full targeted run passed tasks (29 top-level / 45 including subtests), OCR (15 / 32), and billing (50 / 54), with zero failed or skipped tests. Go vet passed. PostgreSQL was a task-owned local 18.x process on 5547; Redis was the existing `redis:7-alpine` image in a task-owned container mapped only to loopback 6387. No image build, dependency reinstall or ambient service configuration was used. The real OCR worker was not run. Frontend application code, requests and public statuses are unchanged, so the frontend browser suite was not repeated. The earlier unrelated broad Studio-suite qualification gap remains separate.

## Independent residual risks

`/api/ocr/jobs` still creates a fresh task for each accepted POST; uncertain-response creation retries are an independent idempotency issue. Process termination after billing commit was not forcibly induced; the deterministic Redis failures prove the gap, while missing durable recovery after process loss is source-supported. Other OCR/conversion producer settlement orderings were reviewed only as compatibility references and were not changed. F-01 and F-02 remain open globally. Backend preview-owner PR #11, Studio snapshot PR #12, and worker PR #1 remain independent, unreleased drafts; their compatibility/release requirements are unaffected.
