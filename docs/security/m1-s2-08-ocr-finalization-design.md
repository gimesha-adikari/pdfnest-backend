# M1-S2-08 — Durable OCR finalization design

**Status: DESIGN ONLY — NOT IMPLEMENTED.** This document proposes a future correction. It does not correct the characterized runtime, authorize a migration, approve a billing policy, or authorize deployment.

Reviewed source base: `13a0e26e4f78e4d25aff244a1a7f1b3ba28192ee`, on `milestone-1/queued-pdf-publication`, including runtime correction `e427c6093242bffb6f8e24edc3d8c6a412acf41f`. Frontend reference: `0f6fa13361778917afbeeef19e977f7bd8002944`. No runtime tests were rerun for this design. M1-S2-07's isolated executions are historical evidence, distinguished below from new source inspection.

Companion documents: [verification matrix](m1-s2-08-finalization-verification-matrix.md), [migration and release contract](m1-s2-08-finalization-rollout.md), [original qualification](m1-s2-07-ocr-settlement-qualification.md).

## 1. Scope, contract and threat model

Only `POST /api/ocr/jobs`, Image to Searchable PDF from owned image references, is in scope. The result is a newly generated, readable ordinary PDF. There is no source-password or encrypted-output promise. Existing PDF structural validation, task ownership, capability authorization, input ownership and frontend result validation remain required.

The design addresses competing cancellation/finalization, duplicate finalizers, process death, unavailable dependencies and ambiguous acknowledgments. It does not promise atomic transactions between PostgreSQL, Redis and object storage; prevent a privileged storage administrator from destroying objects; make every OCR page semantically correct; or introduce automatic refunds after an infrastructure incident. Input OCR execution is not made exactly once. Retrying an uncertain creation request remains an independent idempotency risk.

**Selected contract:** for database-backed OCR, a single PostgreSQL transaction records both the successful finalization decision and existing reservation settlement. Cancellation uses the same task-row lock. Redis never independently decides the terminal state of this protocol. An application-owned committed artifact is retained until its explicit download deadline; ambiguous responses never authorize deletion. A separate free-mode protocol has no charge and requires no database access in its OCR path.

Success means a validated artifact was persisted, read back and bound to a committed descriptor at decision time. Continued availability depends on storage and service availability. Missing/corrupt committed bytes are an integrity incident, not permission to report a refund or rewrite success as cancellation.

## 2. Source-grounded current behavior

All paths below are relative to this backend at the reviewed base, unless stated otherwise.

| Source and symbols inspected | Relevant finding |
| --- | --- |
| `internal/ocr/routes.go:RegisterRoutes`, `r2_jobs.go:HandleAsyncImageToTextPDFR2` | `/ocr/jobs` is outside the legacy upload/idempotency group. It checks a nonempty `user_id`, validates owned image keys, calls `ReserveWithTaskID`, registers an owner-bound Redis record, then launches a goroutine. It hardcodes database reservation kind. |
| `internal/ocr/r2_jobs.go`, `r2_jobs_test.go` | PDF generation and `uploads.CheckPDFPageLimit` precede `storage.SaveObjectFile`, billing commit, then Redis completion. The deferred `!artifactPublished` deletion depends on an acknowledged Redis write. Registration failure prevents processor start. |
| `internal/ocr/r2_billing_consistency_test.go:TestR2BillingQualificationCurrentOrdering`, `completionGate.ProcessHook` | Barriers at billing row locking and Redis completion reproduce both cancellation orderings, rejected completion and an applied completion with a lost reply, for plan and credit allocations. Assertions intentionally preserve bad outcomes. |
| `internal/tasks/tasks.go:atomicSetTaskLua`, `atomicGetCheckStaleLua`, `atomicCancelTaskLua`, registry methods | Redis terminals are immutable. Reads can convert stale processing to failure. Cancellation atomically checks owner/capability and changes Redis before invoking billing release. Public DTO omits internal metadata. |
| `internal/tasks/routes.go`, `download_controller.go`, `authorization_test.go`, `tasks_test.go` | REST status, cancellation, download and WebSocket progress reach the registry. Download may call billing commit through a callback before opening output. Ownerless legacy records require an exact capability. Generic callbacks must not settle the proposed protocol. |
| `internal/billing/service.go:ReserveWithTaskID`, `Commit`, `Release`, `ReserveAsync`, `CommitAsync`, `ReleaseAsync` | Free mode returns an empty reservation ID. Normal commit opens its own `config.DB.Transaction`, locks reservation and subscription, applies usage/credits, inserts usage, commits reservation. Release changes only `reserved` rows; it cannot reverse committed charges. Existing commit returns nil for any non-reserved status, including released. |
| `internal/billing/guest_quota.go:Commit`, `Release`, `watchReservation` | Guest accounting is Redis WATCH-based pending-to-used settlement; commit deletes the reservation. It cannot participate in a PostgreSQL transaction. Absence can mean expiration/already settled. |
| `internal/billing/init.go:Initialize`, `janitor.go:SweepAbandonedReservations` | Generic stale/cancel/download callbacks settle by reservation kind. Janitor infers release from Redis absence/staleness; database reservation age is not a durable OCR decision. |
| `internal/identity/middleware.go:Resolve`, `types.go`; route registration in `main.go` | Both guest and user identities populate `user_id`. No user-only gate is attached to `/ocr/jobs`. The synthetic guest result is not proof of a production event, but source does not enforce the controller's claimed authenticated-only requirement. |
| `config/database.go:BillingReservation`, `Subscription`, `UsageLog`, `ConnectDB`; `config/migrations.go` | Reservations persist allocation, status, task and expiry. Usage logs lack a reservation link. Managed schema is validated; the explicit migration command uses a serialized migration runner. Main startup currently connects a database even though the free billing/OCR unit path supports `config.DB == nil`. |
| `internal/storage/storage.go:SaveLocalStream`, `SaveObjectFile`, `ResolveObject`, `ObjectExists`; `r2.go:UploadFile`, `DownloadToTemp`, `DeleteObject` | Local writes sync the file and rename, but do not sync the parent directory. Remote upload can encrypt bytes at rest. Wrappers do not expose immutable object generations, conditional writes or a typed distinction between missing object and outage. Boolean existence is insufficient verification. |
| `internal/tasks/cleanup.go:StartCleanupWorker`, `sweepExpiredTempFiles` | Temporary workspaces are age swept; final artifacts must not reside in this swept workspace. Existing local storage and temporary OCR output are distinct lifecycles. |
| `internal/studio/job_coordinator.go:ReconcilePending`, `finalizePreparedStudioJobResult`; `job_reconciler.go`; `storage_cleanup.go`; `repository.go:ClaimStorageCleanupTasks` | Existing pattern: prepare outside transaction, recheck bindings under row lock, durable reconciliation and cleanup leases. Studio models depend on sessions/documents and cannot represent OCR billing directly. Cleanup leasing alone is not proof of fencing: OCR must compare claim tokens on writes. |
| `main.go` startup | Task cleanup, billing janitor, Studio storage cleanup and Studio job reconciliation actually start. A new OCR reconciler must be wired here and shut down independently of optional embedded-worker startup. No unused helper is counted as recovery. |
| Frontend `hooks/useAsyncTask.ts`, `lib/taskStorage.ts` (read only) | Task IDs are treated as strings in the inspected storage/HTTP paths. Prefix dispatch is feasible but still requires the future compatibility tests; this is not exhaustive client qualification. |

Graph tools were unavailable; this investigation used direct source and call-site reads. Related Studio and preview security branches were not merged into this base.

### Proven inconsistent outcomes

The historical S2-07 tests used disposable PostgreSQL/Redis, a controlled OCR processor and real controller/cancel/download code. They did not run a production OCR/billing account.

| Controlled ordering | Redis | Billing | Artifact/download |
| --- | --- | --- | --- |
| Cancel before commit | `CANCELLED` | Released; no usage/credit charge | Cleaned; no successful download |
| Cancel after commit, before completion | `CANCELLED` | Committed; plan counters or credits consumed once | Cleaned; 404 |
| Completion rejected before execution | Initially `PROCESSING`, then stale `FAILED` | Committed; stale release does not refund | Cleaned; 404 |
| Completion applied; reply lost | `COMPLETED` | Committed once | Cleaned; 410 |
| Normal success, duplicate settlement and late cancel | `COMPLETED` | One charge | Retained; 200 |

Related guest-store tests also show that releasing a committed guest reservation cannot undo used quota. A synthetic direct-guest controller request produced an unpersisted database reservation and failed finalization without charging. Neither is a safe guest route contract.

## 3. Alternatives and decision

**All candidate architectures in this section are DESIGN ONLY — NOT IMPLEMENTED.**

| Dimension | A. PostgreSQL authority + transactional billing + separate outbox | B. Durable manifest + independent coordinator | C. Reservation-linked compensation | D. Selected OCR-specific hybrid |
| --- | --- | --- | --- | --- |
| Source fit | Good for database reservations; requires transaction-aware billing and new durable task/outbox tables | Storage has objects, but no verified conditional manifest updates/generation interface; file manifests do not coordinate multiple instances | No existing committed reversal; would introduce an accounting subsystem | One OCR SQL row is both decision and due-work record; reuse billing transaction logic and Studio's bounded reconciliation pattern |
| Cancellation after commit | Same locked SQL decision prevents contradictory cancel | Requires a durable CAS authority spanning cancel and manifest; manifest alone does not atomically settle SQL | Can compensate later, but cancellation/charge remain split until recovery | Same SQL lock; cancel wins before success transaction, otherwise completion wins |
| Redis reject/lost reply | Outbox replay; never delete due to Redis acknowledgment | Safe only if manifest owns artifact and is discoverable without worker | Requires trigger to distinguish failed projection from actual failed task; lost ack must not refund valid success | Replay from SQL row; direct authorized SQL status/download remain possible without Redis |
| Crash/restart | Durable row and outbox discovery | Works with durable, fenced manifests plus a separate settlement ledger | Requires durable compensation intent and executor | SQL due index and leases; free mode uses Redis journal/index, not originating goroutine |
| Schema/migration | Task + outbox, possibly artifact table | Manifest store plus SQL settlement linkage still needed | Compensation ledger, balance/usage audit rules and probably migrations | One proposed SQL table; no new broker; free journal uses existing Redis |
| Redis coupling | Projection only for billable; all-SQL free path would add a dependency | Depends on chosen manifest store; not automatically eliminated | Still needs terminal authority and projection | Projection for database protocol; authority only for explicitly no-charge free protocol |
| Guest/free | Redis guest settlement cannot join SQL; all-SQL free breaks DB-nil path | Can represent free; guest settlement still needs protocol | Guest quota windows and expired reservations make reversal harder | Propose user-only `/ocr/jobs`; legacy guest routes unchanged. Free empty-ID path stays DB-independent |
| Artifact lifecycle | Descriptor in SQL; retained independent of outbox acknowledgment | Natural artifact intent, but storage CAS/list/cleanup capabilities must be added and tested | Reversal does not repair lost artifact; still needs retention | Predeclared unique output key, descriptor, deadline, cleanup tombstone in owning record |
| Operational cost | Multiple write/claim paths; extra outbox retention | Extra storage reads/CAS, consistency and discovery tooling | Accounting audit, retries, operator policy and balance reconciliation | One domain coordinator, bounded SQL/Redis scans; readback/hash cost; explicit no-charge branch |
| Backward compatibility | Version dispatch and coordinated cutover required | Same, plus new storage protocol | New financial policy and rollout risks | Opaque versioned IDs, unchanged public terminal names, drained old writers; no conversion of old charges |
| Main advantage | Strong conventional design, independent event history | Worker-independent recovery with durable intent | Could address already committed historical incidents | Smallest bounded charge/decision transaction; no generic platform migration |
| Main disadvantage/assumption | Separate outbox adds no required consumer beyond Redis/cleanup here | Unsupported CAS/manifest authority cannot be assumed | Blind counter reversal is unsafe; refund/window policy unapproved | Two authority backends are explicit; free Redis data loss is weaker than SQL durability and must fail closed |

Choose **D**, incorporating A's transactional decision and B's durable descriptor, using the same OCR row as an outbox. Separate outbox events are unnecessary because consumers need only the latest terminal decision/revision, not every progress event. Compensation C is rejected: it neither prevents deletion on lost acknowledgments nor supplies an approved reversal of historical counters/credits. D does not promise refunds for prior incidents.

No general task-registry or billing-platform rewrite is proposed. The free adapter is necessary to preserve the tested empty-ID/no-database path, not a second billable settlement system.

## 4. Authoritative record and proposed schema

**Schema proposal — DESIGN ONLY — NOT IMPLEMENTED. No SQL migration is included.**

One new table, provisionally `ocr_finalizations`, with a unique task primary key:

| Group | Proposed fields / constraints |
| --- | --- |
| Identity and protocol | `task_id` (opaque `ocrd1_` + random UUID), `protocol_version=1`, immutable owner ID/type, private random capability, `allocation_kind=database`, reservation ID/kind, tool ID. Unique non-null reservation association; require database kind and matching task/owner/tool. No raw identity in keys or logs. |
| Decision | `state` in `accepted, processing, ready, succeeded, cancelled, failed`; monotonic `revision`; safe error code; accepted, processing-deadline, committed, terminal timestamps. Decision states terminal and immutable. |
| Execution | One processor claim token, lease expiry, heartbeat; validated owned input-key references and bounded request parameters needed for cleanup. No image bytes, PDF passwords, raw request body or session credentials. |
| Artifact intent | Random `artifact_id`, predeclared private output key under dedicated OCR prefix, validation version, logical plaintext SHA-256, byte size, page count, `application/pdf`, descriptor timestamp, verified timestamp. Descriptor is immutable once ready; generation/version nullable until storage adapter actually supports it. |
| Recovery | `next_attempt_at`, reconciliation claim token/expiry, attempt count, last safe failure code, projected revision; due index on next-attempt/lease. Projection flag is not a terminal decision. |
| Retention/cleanup | `download_expires_at`, cleanup state `held, eligible, deleting, deleted`, cleanup token/lease, not-before, deleted timestamp, integrity-health flag, metadata-purge deadline. Failed/cancelled rows retain intent until cleanup verified. |

Use constraints, not conventions alone: succeeded requires committed timestamp, verified descriptor, nonempty valid size/pages and deadline; non-free reservation unique; no success from terminal failure/cancellation; cleanup cannot become eligible while a live successful download is within its deadline. Do not cascade-delete artifact bookkeeping when an account/reservation is deleted. Account deletion integration must preserve a cleanup tombstone before removing private ownership data; qualify this before enabling the protocol.

Only **one processor is allowed per task**, and it may write its unique output key once. No takeover reruns OCR into that key. Processor retries/new OCR runs create a new task; finalization retries for an existing key only verify/replay. This removes the need for an attempts table while avoiding overwrite races. Duplicate workers must fail the processor claim before touching input/output. An expired claim authorizes recovery of existing output or failure, never another writer.

Capability must have at least the existing random UUID entropy, be stored privately for rebuilding the existing authorized status response, compared in constant time, and excluded from logs/metrics. A protected database column or approved at-rest database controls are required; do not log it through GORM debugging. Retain the existing token response contract only after owner/capability authorization. Cancellation accepts capability through the existing documented header, not a URL. No internal descriptor/reservation fields enter the public DTO.

The existing cancellation header is `X-Download-Token`. Status/download currently also accept `token` or that header; preserve those legacy contracts without introducing new token-bearing URLs or request logging. Future browser qualification must check permitted CORS headers on the actual cross-origin request path. Do not weaken identity checks to work around a preflight failure.

## 5. State machine and linearization

**State-machine specification — DESIGN ONLY — NOT IMPLEMENTED.** In the table, SQL means the single OCR row; `R` is its associated database reservation. Every mutation checks expected revision and the applicable lease token. Internal trusted recovery acts on the stored owner binding; it does not invent a requester identity. HTTP reads/cancels/downloads authenticate independently.

| Transition | Allowed predecessor / atomic boundary | Owner, reservation and artifact effects | Redis / retry / visible result |
| --- | --- | --- | --- |
| Accept | Absent → `accepted`; one SQL transaction creates reservation and OCR row | Authenticated user required; owned references validated first. R=`reserved`; declare unique key/capability, no output yet | Project owned `PENDING` before launching processor. Return existing task ID only after confirmed registration. On uncertain SQL commit, read by chosen ID; do not reserve again |
| Registration fails/unknown | `accepted` → `failed` under SQL row lock, release R in same transaction; or retain pending recovery if DB outcome unknown | No processor starts. Cleanup owned inputs via durable record | 503 creation response. Any later Redis PENDING cannot authorize work; reconciliation projects authoritative failure |
| Processor claims | `accepted` → `processing` under row lock, one claim only | R still reserved; validate deadline and registration acknowledgment | Public `PROCESSING`, progress capped below 100. Heartbeat lease; no reprocessing after claim expiry |
| Prepare output descriptor | `processing` → `processing`; store expected digest/size/pages/key before upload, using processor token | Local PDF parse/page-limit validation, then hash. Descriptor intent owns potential object even if PUT reply lost | No success; persistent recovery can inspect declared key after crash |
| Artifact becomes ready | `processing` → `ready` under row lock after readback verification outside transaction | Persisted bytes match descriptor and parse; lease still current; R reserved, deadline valid | Still public `PROCESSING`; enqueue due finalization. A stale processor cannot promote after cancellation |
| Successful finalization | `ready` → `succeeded`, **single SQL transaction** described below | Lock job, R and subscription; commit R + usage/credits + decision + deadline + next replay together | Public success only after committed read. Redis projection may lag or be absent; retain object regardless of reply |
| Cancel | `accepted/processing/ready` → `cancelled`, SQL row lock + reservation release in same transaction | Check owner or exact header capability inside locked decision; invalidate processor token, R released, object cleanup eligible after outstanding I/O grace | Project `CANCELLED`. A response is confirmed cancellation only after committed read; on uncertainty return safe 503, not a refund claim |
| Fail / deadline | Same nonterminal states → `failed` under row lock + release R | Safe error, no charge; invalidate processor token; durable cleanup intent | Public `FAILED`; recoverable dependency outage stays nonterminal until retry/deadline, not falsely terminal |
| Replay | Terminal decision unchanged; fenced bookkeeping update | Billing never repeated by projector; artifact remains owned | Conditional projection/reconstruction; same revision is acknowledgment-equivalent; lost reply schedules read/retry |
| Integrity incident | `succeeded` decision unchanged; health becomes unavailable/corrupt | No deletion or new charge; retain evidence; no automatic reversal | No bytes served. 503 for outage, 410 for confirmed missing/corrupt artifact; alert. Status cannot imply a currently usable artifact while health is bad |
| Retention/cleanup | Terminal unchanged; row-locked cleanup eligibility, then fenced claim and I/O outside transaction | Expired successful or failed/cancelled output may be deleted; R untouched | Download after deadline denied, even if Redis TTL extended. Missing DELETE is idempotent success only if absence is confirmed |

**Who wins concurrent cancellation?** The first transaction to acquire the task row lock and successfully commit a terminal decision wins. If finalization rolls back, it did not win. If cancellation commits first, finalizer observes cancelled/released and cannot charge. If successful finalization commits first, late cancellation returns current `COMPLETED` with no release/refund. Redis cannot overrule either result. Storage or network work does not hold this lock.

### Exact successful transaction

**Go-like pseudocode — DESIGN ONLY — NOT IMPLEMENTED:**

```text
tx := begin PostgreSQL transaction
job := SELECT OCR row FOR UPDATE
if terminal: return its committed outcome (verify duplicate descriptor identity)
require state == ready, expected revision, descriptor match, valid deadline
require verified artifact proof fresh enough and cleanup == held
reservation := lock exact reservation FOR UPDATE
require task, owner, kind, tool and captured allocation all match
require reservation.status == reserved; released/missing is NOT success
CommitReservedInTx(tx, reservation, now)  // locks subscription; existing arithmetic
UPDATE job SET state=succeeded, revision=revision+1, committed_at=now,
    download_expires_at=now+approved_download_lifetime, next_attempt_at=now
commit tx
// Any commit error is UNKNOWN until a fresh authoritative read resolves it.
// Never delete an output, rerun settlement, or return cancellation from that error alone.
```

No storage/Redis calls inside this transaction. The proof refers to a unique application-immutable key which cleanup is prohibited from deleting while held. Readback cannot prevent an external storage failure immediately afterward; that is an availability incident, not a distributed-transaction guarantee.

Proposed proof freshness is 60 seconds: if older, perform bounded readback outside the transaction and conditionally refresh the same descriptor/revision before trying again. Finalization checks the stored reservation expiry and workflow deadline, not just wall-clock freshness. These timeout values are qualification inputs, not deployed settings. On SQL commit uncertainty, recovery reads from the authoritative primary, not a potentially lagging replica. Failure to read leaves the outcome unknown and the artifact held.

## 6. Billing interface and invariants

Propose transaction-aware internal methods `ReserveWithTaskIDInTx`, `CommitReservedInTx`, and `ReleaseReservedInTx` accepting a caller-owned `*gorm.DB`, expected task/owner/tool/kind and a single decision timestamp. Names are provisional. They must never open a nested independent `config.DB.Transaction`. Existing public methods wrap these primitives and retain legacy semantics for unrelated callers; strict OCR outcomes distinguish reserved/committed/released/missing/mismatched.

For new protocol reservations, generic `Commit`, `Release`, download callbacks and stale callbacks must refuse independent settlement (typed coordinator-required outcome). Dispatch by versioned task ID/reservation association, not current billing mode. The OCR coordinator is the sole settlement path. Janitor selects such reservations through the coordinator, never interprets missing Redis as release permission. Handle the lock ordering consistently: job → reservation → subscription. Reservation creation already locks subscription; its aggregate reservation read does not lock existing reservation rows. Add concurrent reserve/finalize/janitor tests for deadlocks; bounded transaction retry applies only after whole rollback.

Avoid a package cycle: define a narrow protocol-handler interface at the existing task boundary, implement it in OCR, and inject it from `main.go`. Billing's new-ID guard returns coordinator-required without importing OCR; the janitor delegates through an injected callback. The coordinator calls only the transaction-aware billing primitives with its locked record. A missing handler for a recognized new prefix is 503, never legacy fallback.

Preserve allocation already captured by reservation: do not recalculate prices or redistribute plan/credit units during finalization. Existing `syncWindows` semantics at the successful commit timestamp remain: plan units increment three-hour/daily/monthly counters, custom-credit units deduct credits, and the existing usage log is inserted in the same transaction. An aborted transaction applies none. Two finalizers serialize on job; only the winner executes these effects. Existing reservation committed-idempotency remains for legacy tasks. A committed reservation with a non-succeeded new-protocol job is an invariant violation requiring quarantine, not silent success; it cannot be created by a correct atomic transaction.

Cancellation releases only the task's still-reserved allocation in the same transaction as cancellation. It never adjusts previously committed counters. Expired reservations cannot be committed: janitor/coordinator acquire the job lock, atomically fail/release if the decision deadline is exceeded. Do not let a competing ordinary release bypass that lock. Reconciliation must not prolong reservation holds without a bound. Proposed processing/finalization deadline is no later than the existing async reservation expiry (currently 30 minutes); the precise OCR timeout requires review. Once succeeded, replay never calls billing.

No guest reservation is put into this SQL transaction. No new compensation policy is proposed. Historical cancelled-but-committed reservations need a separately reviewed accounting investigation; neither a retry nor `Release()` repairs them.

## 7. Artifact durability, authenticity and cleanup

1. Preserve current generated-PDF parse/page-limit validation. A structural parse establishes readability/pages, not OCR accuracy or source equivalence. Hash/size bind the specific validated logical PDF.
2. Allocate a dedicated random output key in the durable record before PUT. Do not accept a worker/client-selected path. Do not use raw local-path fallback for this protocol. Local final objects must live outside `temp.GetDir()` and age-swept prefixes.
3. Store the expected descriptor before upload. PUT has a bounded context and unique-key/no-overwrite application contract. No presigned client PUT is issued for output keys. A duplicate upload into the same key is forbidden.
4. On successful or ambiguous PUT, resolve that exact key, decrypt at-rest bytes if applicable, compare logical plaintext byte count/SHA-256, and parse pages again before `ready`. Do not use ciphertext ETag, MIME, extension or `ObjectExists()` as evidence. The current storage wrappers require narrow context-aware, typed-error/readback interfaces. If an object generation is available later, record and pin it; do not assume current wrappers supply one.
5. For local persistence, add parent-directory fsync after atomic rename; qualify the actual filesystem. Multi-instance deployment cannot use instance-local storage unless every reader/reconciler accesses the same durable volume. Otherwise enable the protocol only with qualified shared object storage.
6. Downloads authorize the requester and decision first, enforce deadline, materialize/read/hash against descriptor before sending bytes. A corrupt PDF with a changed digest fails without sending it. This adds one sequential read/hash to an existing remote-to-temp download; the normal path need not parse the same unchanged digest again. No public signed storage URL/key is returned.

Storage succeeds but SQL fails: leave the object owned by its intent. Re-read decision, retry finalization if ready/reserved, or cleanup only after an authoritative failed/cancelled decision. SQL commits but Redis fails: SQL status/download remain available; replay later. Redis reply lost: verify projection or replay, never delete. Worker dies after upload: reconciler checks recorded expected descriptor, promotes ready if valid; absent/unverifiable output eventually fails/releases without charge. Worker dies after SQL commit: SQL discovery replays without recharging.

The row's artifact key cannot change after descriptor preparation; a second finalizer with a different key/hash fails a conflict check. Missing/corrupt output before settlement fails/releases. Missing/corrupt committed output is quarantined, never replaced by rerunning OCR under the same task ID. Storage outage is retryable, not proof of absence.

An authenticated status request for a succeeded row with a known unavailable/corrupt artifact returns a safe dependency/integrity error rather than presenting a fresh usable success. It does not mutate the immutable billing decision to failed. A download materialization has a proposed two-minute upper bound; the physical-retention grace exceeds that bound. Once the authorized PDF has been verified into task-owned download temporary storage, deletion of the remote object cannot alter that response's bytes.

**Retention proposal (requires approval before implementation activation):** download lifetime one hour from authoritative success; existing `TaskTTL` is one hour but currently refreshed by writes, so an explicit deadline is a new clarification. Keep physical output at least 15 minutes beyond that deadline for bounded in-flight downloads. Failed/cancelled object cleanup waits for processor lease and bounded storage operations to quiesce. Proposed ordinary metadata/tombstone lifetime seven days after verified deletion, with permanent billing audit left under existing policy. These are proposed bounds, not claims about configured bucket lifecycle or approved legal retention.

Cleanup first locks the job and makes it ineligible for future success, or confirms successful retention expiry. It claims deletion with a fencing token; performs DELETE outside transaction; acknowledges only with the matching token. A stale cleaner cannot mark a new claim complete. No cleaner deletes `held` succeeded output before expiry. Never purge a row/cleanup pointer because dependency errors exhausted retries.

Late PUTs after cancellation are an orphan risk even with a cancelled client context. Keep tombstones and revisit deletion. Add a bounded paginated inventory of the dedicated output prefix (local scan or object-store list) and compare to authoritative rows before deleting old orphan keys. Listing permission is a future activation prerequisite, not an assumed current wrapper capability. Inventory must stop on authority outage; key age alone cannot override a committed row. Sweep each page with a durable cursor and recheck each candidate; periodic full cycles cover insertions before a cursor. Provider retention locks/backpressure require escalation and admission pause, not unbounded silent uploads. Artifact retention is bounded by policy; actual deletion during provider outage cannot have an absolute time guarantee. Capacity budgets and alerts are release gates.

## 8. Recovery and Redis projection

Use a small OCR coordinator/repository and an independently started reconciler. Reuse Studio's prepare/recheck/row-lock and bounded batch pattern, not Studio session/version tables or a new generic workflow engine.

Proposed worker parameters: tick 10 seconds, batch 20, lease two minutes, bounded I/O under 45 seconds with renewal where needed; exponential retry 5 seconds to five minutes with jitter. Values require load/timeout tests. Claim due rows in a short transaction using `FOR UPDATE SKIP LOCKED`, set a random claim token and lease, commit, perform I/O, then update only where token/revision still match. This is per-row coordination, not a global leader. Expired leases are reclaimable. Claim tokens fence bookkeeping; processor uniqueness/key immutability prevents a stale claimant writing a different artifact.

Discovery is an indexed due query, not a process-local queue. It covers accepted registration uncertainty, expired processing leases, prepared descriptors with possibly uploaded objects, ready jobs, terminal unprojected decisions and cleanup. No permanent discovery cursor is required for due rows: every due row stays eligible until fenced acknowledgment. Bound and monitor oldest due age; separate terminal cleanup from processing recovery so one outage cannot starve all work.

Recovery order: read/authorize stored binding → check authoritative terminal/deadline → inspect prepared object if necessary → finalize or fail/release transaction → project terminal → schedule cleanup. A dead processor with no usable prepared output is failed/released after lease/deadline; it is not silently restarted. A prepared valid object can be finalized by recovery without original image refs or originating goroutine. Source refs are retained only until processing recovery no longer needs cleanup; remove through tracked cleanup, not arbitrary worker defer after an uncertain decision.

Store `next_attempt_at` as the earliest outstanding projection, decision, integrity retry, cleanup or metadata-purge deadline. After each fenced operation recompute it from remaining obligations; acknowledging a projection cannot remove a cleanup obligation. Claimed work is never removed from discovery before the corresponding durable acknowledgment. During repeated pre-settlement outages, the deadline decision still atomically fails/releases once PostgreSQL returns; a worker cannot settle late merely because cleanup was delayed.

Redis projection has protocol and revision internally. New protocol reads must not call the old stale-mutating Lua. A conditional projection script accepts missing record, same immutable decision/revision, or a higher revision with the same binding; rejects owner/token/descriptor mismatch and contradictory terminal decision. Rejection is an incident requiring authoritative repair through the new coordinator, never ordinary Redis cancellation. Redis cannot overwrite SQL. Use the existing public `PENDING/PROCESSING/COMPLETED/FAILED/CANCELLED` names; ready maps to processing below 100. No new public finalizing state.

REST status/cancel/download and WebSocket status must dispatch on protocol before touching legacy registry mutation/callbacks. For `ocrd1_` IDs, SQL is consulted even if Redis is missing. Unknown ID is not adopted as a legacy task. Successful status can be served from SQL during Redis outage; download still checks storage. Rebuild an expired projection only within the explicit public deadline, with TTL equal to remaining lifetime; reads cannot extend retention. Do not reconstruct an expired public success.

Repeated dependency failure remains durable due work with capped retry frequency, a safe code, metrics and operator escalation; do not discard it after N attempts. Alert on committed output unavailable, unmatched billing state, projection conflicts, oldest due age, deletion backlog and storage budget. Logs use opaque task/attempt correlation and safe codes, excluding owner IDs, tokens, reservation IDs, keys, PDF bytes and OCR text. A database outage means new charged work/cancel decisions/download authorization fail safely with 503; it is never permission to trust Redis instead.

## 9. Free mode and guest compatibility

### Free protocol: no hidden database dependency

Use immutable `ocrf1_` IDs and `allocation_kind=free`, empty reservation ID, zero settlement calls. Do not infer this from the billing mode on a later request. Free tasks use a private Redis OCR journal plus a due sorted set; the ordinary TTL status record is only a projection. Journal contains the same minimum owner, descriptor, decision, cleanup and lease fields. Register journal and discovery entry atomically before processor start. No mandatory SQL table access, SQL migration check or database janitor call occurs in this path. Main startup's existing global database dependency is not removed or newly claimed solved.

Free cancellation and success compare the same journal decision in Lua. Successful CAS installs terminal decision + descriptor + deadline + due entry atomically. No billing operation can follow. A lost reply is unknown: re-read the journal, retain output and replay, rather than cleaning on error. Nonterminal lease recovery and cleanup use a token-checked journal claim and sorted-set due entry in one script. Do not expire the journal before cleanup acknowledgment; after deletion apply bounded metadata TTL. Public record expiry cannot lose discovery. Proposed Redis hash-tagged OCR keys must be tested against actual standalone Redis; do not assume cross-slot atomicity if cluster support is later added.

Backend restart is recoverable while Redis data survives. Redis outage postpones free decisions/cleanup, retains objects, returns 503, and resumes from due entries. **Catastrophic Redis data loss is not equivalent to a process restart and cannot be reconstructed authoritatively from PDF bytes.** Unknown free IDs fail closed; no charge exists; no fabricated success/token/owner is reconstructed. Inventory quarantines keys until the maximum download and upload grace has elapsed, then removes unowned outputs. Infrastructure durability/backup expectations must be explicit at activation; this design does not claim the current Redis deployment is persistent or that a local test proves it. Guaranteed free-task survival of total Redis data loss would need an additional approved durable journal, and is outside the selected minimal contract.

Inventory must distinguish the SQL and free namespaces. For a missing free journal, only an available authoritative Redis lookup can establish absence. With the proposed 30-minute maximum processing window, one-hour download lifetime and 15-minute grace, use a conservative proposed minimum two-hour quarantine after the object's server modification time, plus an approved clock-skew margin, before treating it as an orphan. A present journal always overrides age. If reliable timestamps/listing or maximum request durations cannot be qualified, orphan deletion and acceptance remain gated; do not guess. SQL namespace inventory similarly requires a successful primary lookup and never treats a database outage as absence.

### Guest route policy

Source confirms identity resolution permits guests into the route; frontend auth is not backend enforcement. Recommend requiring resolved `Identity.TypeUser` before reservation/job acceptance for **this endpoint only**, including free mode, matching its authenticated-only UI. Normal mode also requires an actual registered user/subscription reservation. Reject guests with safe 401 and no new reservation/output. This policy requires independent approval; do not treat it as already enforced. Guest support via `ReserveAsync` would require a distinct Redis guest-settlement protocol coupling quota and task decision, which is not justified by the chosen flow. Existing guest producers, reservation-kind resolution, ownerless legacy tokens and non-OCR tasks remain unchanged.

## 10. Future source-level change plan

**Implementation plan — DESIGN ONLY — NOT IMPLEMENTED.** Sequence is specified in the rollout document; no listed file is modified by this design PR.

| Future file / seam | Exact intended responsibility |
| --- | --- |
| `config/database.go`, `config/migrations.go`; new `internal/ocr/finalization_repository.go` | One durable model, constraints/indexes, transactional repository, strict job/owner/reservation binding, leases and cleanup tombstones. Serialized reviewed migration; no auto-backfill of old tasks. |
| `internal/billing/service.go` | Extract transaction-aware reservation/settlement/release preserving existing arithmetic; strict OCR coordinator-only outcomes and ownership checks. Legacy wrappers retained. |
| `internal/billing/janitor.go`, `init.go` | Exclude/route new-protocol reservation release/commit through coordinator. Never release from stale Redis metadata. Keep other kinds/callbacks unchanged. |
| New `internal/ocr/finalization.go`, `free_finalization.go`, `finalization_reconciler.go` | OCR state machine, SQL and no-charge Redis adapters, bounded recovery, projection and cleanup. No generic workflow framework. |
| `internal/ocr/r2_jobs.go`, `routes.go` | Explicit approved identity gate, atomic acceptance, initial owned registration before work, one processor claim, validated descriptor/PUT/readback, coordinator settlement; remove acknowledgment-based artifact deletion for new protocols only. |
| `internal/tasks/tasks.go`, `routes.go`, `download_controller.go`; progress handlers | Narrow protocol-dispatch seam for status/cancel/download/WS; safe DTO unchanged, header cancel capability preserved; no legacy billing callbacks for new IDs. |
| `internal/storage/storage.go`, `r2.go` | Bounded contexts, typed absence/outage/corruption, descriptor readback, local directory durability, private key-only downloads, bounded inventory/cleanup. No assumption of backend object versioning. |
| `main.go` | Start OCR recovery/inventory after dependencies; feature gates; cancel context and bounded shutdown independent of optional embedded worker. DB adapter disabled in free path. |
| Tests in OCR/tasks/billing/storage plus future HTTP/process harness | Implement companion deterministic matrix. Preserve S2-06 authorization/artifact tests; convert bad-outcome characterization assertions only after protocol is active in tests. |

## 11. Performance, limitations and approval gates

One SQL row per database OCR task, no broker/outbox table, short finalization/cancel transactions and indexed batch scans. One additional output readback and hash at persistence, plus digest verification on download, cost O(PDF bytes); current remote storage already buffers/encrypts uploads and materializes downloads. Apply bounded OCR size/page limits and stream hash where possible; do not multiply PDF parse passes inside retries. Reconciliation may reuse a matching immutable descriptor; corruption/missing data must never be waved through for performance.

Independent approval is required for: the user-only route policy; explicit one-hour download/retention and processing timeout bounds; weaker free-mode behavior under catastrophic Redis data loss; storage topology/list permission/durability qualification; account-deletion cleanup; and a coordinated cutover. These are activation gates, not permission to implement a different refund policy. Source does not verify object generations, bucket lifecycle, Redis persistence or production filesystem guarantees.

If the requirement is strengthened to refund every unavailable artifact after a committed success, or to survive total Redis loss for free tasks without any additional durable store, this design must return for a separate policy/infrastructure decision. No such promise is assumed here. Existing historical inconsistencies remain unresolved until an approved implementation and recovery policy ship.

Remaining independent scope: transport request idempotency, other async producers, platform-wide F-01 qualification, queued non-OCR artifact producers, broader Studio test-environment gaps, and AES-128/AES-256 output-policy equivalence. Preview owner isolation (#11 + worker #1) and Studio snapshot publication (#12) remain separate unreleased branches; this OCR design has no artificial dependency on them.
