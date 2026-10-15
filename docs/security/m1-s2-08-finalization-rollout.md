# M1-S2-08 — Migration and release compatibility contract

**DESIGN ONLY — NOT IMPLEMENTED.** No schema, task semantics, deployment flags, service configuration or runtime files are changed by this proposal. No rollout, merge or deployment is authorized.

Parent: `13a0e26e4f78e4d25aff244a1a7f1b3ba28192ee`, branch `milestone-1/queued-pdf-publication`, draft PR #13. This documentation is stacked directly on that branch. Related [architecture](m1-s2-08-ocr-finalization-design.md) and [verification matrix](m1-s2-08-finalization-verification-matrix.md) define the future behavior.

## 1. Compatibility boundary

The existing binary treats Redis as terminal authority, releases billing through generic cancellation/staleness callbacks, and deletes output when completion acknowledgment is uncertain. It cannot safely participate in new SQL-authoritative OCR tasks. A new SQL table by itself does not fix those paths.

Propose opaque protocol identifiers:

| Identifier | Authority / store | Compatibility |
| --- | --- | --- |
| Existing UUID task | Existing Redis task and existing reservation-kind handlers | Legacy behavior retained; do not silently convert or claim historical charges repaired |
| `ocrd1_` + random UUID | Durable SQL OCR decision; Redis projection | All status/cancel/download/WS writers and readers must dispatch before invoking legacy mutation/callbacks |
| `ocrf1_` + random UUID | No-charge Redis OCR journal/index; TTL public projection | Empty reservation ID, no database access in OCR path; never infer mode at replay time |

ID prefix is routing metadata, not authorization. Exact owner/capability checks still apply. The inspected frontend paths treat IDs as opaque strings; future end-to-end tests must verify storage, URLs, progress and cancellation. No new public task-status name is proposed. `ready` remains `PROCESSING`; success is exposed only from a committed authoritative decision. Private fields never enter status DTOs.

Legacy database/guest reservations, ownerless capability records and non-OCR tasks remain on their current stores. New protocol associations cannot be invented from an old Redis record. The route's creation-request idempotency gap is separate: retries after an uncertain HTTP response can still create different tasks/reservations unless a later approved idempotency design addresses them.

## 2. Proposed migration sequence

**Migration plan — DESIGN ONLY — NOT IMPLEMENTED. No executable DDL or generated migration exists in this PR.**

1. Review one additive `ocr_finalizations` table, checks and due indexes described in the architecture. Require unique task and database reservation bindings, immutable decision/descriptor enforcement through conditional repository writes, and no cascading loss of cleanup pointers. Confirm retention/account-deletion handling before approval.
2. Extend the explicit migration runner in `config/migrations.go` and managed schema version only in a future implementation. The existing runner takes a PostgreSQL advisory lock; preserve serialized migration execution. Deploying an unreviewed AutoMigrate startup is not a substitute for a migration plan.
3. Apply the additive migration to isolated qualification first. Test supported PostgreSQL version, indexes, transaction/lock behavior and rollback. New table is initially empty; no backfill of committed historical charges or adoption of running legacy tasks.
4. Future managed migration precedes binaries that validate/use this schema. Old compatible legacy binaries ignore the additive table while new acceptance is disabled. Free-mode OCR adapter must not query the new table or gate on its existence when invoked without a database; main startup retains its pre-existing separate database requirements.
5. Prove reservation helpers operate inside the caller's transaction, with job → reservation → subscription locks. Generic callbacks/janitor must recognize new IDs and return coordinator-required rather than independently settle. Prevent an old janitor instance from running during new-protocol operation.
6. Introduce the OCR coordinator, adapters, storage descriptor operations, task dispatch and startup/shutdown wiring. Configure gates initially off. Compile/unit tests alone are insufficient; complete the deterministic matrix and restart/cleanup rehearsal.

No migration alters subscription credit balances, usage history, existing reservation statuses or old task rows. No compensation/backfill is authorized. If adding the table requires incompatible user-deletion cascades or other billing migrations, stop for review.

## 3. Capability gates

**Configuration proposal — DESIGN ONLY — NOT IMPLEMENTED.** These are internal capabilities to be designed/tested, not current environment variables:

- **understand-v1:** all task/status/WS/download/cancel dispatch and safe janitor/callback behavior installed, with new acceptance still off.
- **reconcile-v1:** SQL and no-charge Redis discovery/projection/cleanup active. Multiple instances may run; per-record leases/tokens provide ownership. Not a single elected process whose death strands work.
- **accept-v1:** enables selected `/ocr/jobs` creation only after all readers, writers, janitors and cleaners are compatible; authentication/retention/storage prerequisites approved.
- **accept-free-v1:** separate explicit readiness for the DB-independent journal adapter, including catastrophic Redis-loss behavior. It does not enable guest quota billing.

Flags never downgrade an existing task's protocol. Turning off acceptance must leave reconciliation, owner checks, immutable descriptors and existing-result download available. Free versus normal adapter selection is recorded at creation, not chosen from a later global billing-mode change.

## 4. Required coordinated cutover

**The original binary and new active protocol are an unsafe mixed-version combination.** This design does not claim an ordinary rolling activation is safe.

Use a reviewed maintenance window for the selected endpoint unless a separate compatibility-only release is first deployed and verified everywhere:

1. Pause new `/api/ocr/jobs` acceptance at a controlled routing boundary; do not redirect documents elsewhere. Keep existing ownership/status/download available where safe.
2. Drain original OCR worker goroutines. Inspect nonterminal owner-bound Redis tasks and reservations from this route. A status TTL alone is not evidence that a worker is gone. Reconcile uncertain legacy outcomes manually under a separate accounting policy; never silently refund or mark them new-protocol success.
3. Stop/drain original instances and their background janitors/cleanup workers. Verify no old process can receive cancellation/WS/status for new tasks or delete new output. Other tool availability during this operation is a release-engineering decision, not promised by the design.
4. Run the reviewed additive migration. Start compatible binaries with acceptance disabled. Verify instance revision/process identity, storage topology, due scans, lease fencing, metrics and cleanup exclusions. Wire shutdown independently of optional embedded worker initialization.
5. Start compatible reconcilers. In an isolated canary environment, create synthetic database/free tasks, fault Redis, restart a process and verify exact charge/artifact association. No production fault injection is required or authorized here.
6. Enable acceptance only after confirming every instance/routing path and background actor is compatible. Record protocol capability and schema readiness per instance through private operational evidence, not a new public attestation endpoint.
7. Monitor charged-but-unavailable incidents, pending decisions, reservation deadlines, projection conflicts, unauthorized attempts and cleanup/storage budgets. Disable **new acceptance** on unsafe drift; continue recovery for existing records.

If zero-downtime rollout is mandatory, first deliver a compatibility-only release that understands new IDs and refuses old Redis mutation/cleanup for them while creating only legacy tasks. Complete its rollout and remove original binaries before enabling new acceptance. Merely deploying new readers alongside original cancellation handlers is unsafe. Version-affine routing alone is insufficient while an original janitor shares the same reservations/Redis/storage.

## 5. Mixed-version decision table

| Combination | Safe? / required restriction |
| --- | --- |
| Old binary + additive empty schema, new acceptance off | Schema compatibility candidate; qualify migration/old startup behavior. Does not fix old races |
| Original task/Redis writers + active `ocrd1_` tasks | **Unsafe:** cancel/stale/release can contradict SQL; generic callbacks or cleanup can invalidate output |
| New coordinator + original download handler | **Unsafe:** trusts Redis, may call independent billing and serve unverified bytes |
| New coordinator + original billing janitor | **Unsafe:** Redis absence/staleness can release a live reservation |
| New task readers + original processor goroutines for legacy UUIDs | Legacy draining only; no promise to repair old ordering; do not adopt those tasks |
| Compatible release on all instances + v1 acceptance enabled | Candidate only after full matrix, storage and operational prerequisites pass |
| Free adapter selected for a database task after global mode change | **Forbidden:** stored protocol/kind controls dispatch |
| SQL protocol on instance-local storage without shared access | **Unsupported** for multi-instance recovery/download; require qualified shared storage or restricted single-instance deployment with durable volume |
| Preview owner PR #11 / worker #1 absent | No OCR finalization dependency; their separate preview risk remains |
| Studio snapshot PR #12 absent | No OCR schema/coordinator dependency; do not import its commits to make this proposal work |

## 6. Rollback and lifecycle drain

Before any v1 acceptance, rollback may use the prior binary with acceptance paused and the additive table empty, after normal compatibility verification. Do not drop the table as part of a routine binary rollback.

**After the first v1 record, rollback to the original Redis-only implementation is prohibited while any v1 task, artifact, reservation or cleanup obligation remains.** Supported rollback means:

1. Disable new v1 acceptance.
2. Retain a compatible reader/cancel/download/coordinator/reconciler build, even if other unrelated changes are rolled back.
3. Finish/fail pending tasks through their authority, retain succeeded objects through their deadlines, perform cleanup and preserve billing/audit evidence.
4. Verify no live v1 work or cleanup obligation remains. Retain version-aware handling for old opaque IDs until the agreed metadata lifetime expires, so old clients do not fall through into legacy mutation.
5. Only a separately reviewed retirement migration may remove protocol support/schema. A Redis TTL or zero current goroutines does not establish safe retirement.

If a transaction's outcome is uncertain during rollback, query the authoritative row by task ID; do not infer rollback from lost connection. Recovery never resets a succeeded row or refunds counters. A committed output that becomes unavailable is escalated for integrity/accounting review without fabricating cancellation.

## 7. Storage, Redis and recovery prerequisites

- Storage output prefix is private and not a source-upload presign scope. Verify output object cannot be overwritten by clients. Each task has one registered producer/key; no takeover reprocessing into the same key.
- Readback, byte hash and parse operate on logical PDF after optional at-rest decryption. Verify typed absence versus outage and bounded request contexts. Do not assume object version IDs or bucket lifecycle exist.
- Local final storage is outside generic temporary cleanup, with directory fsync. Multi-instance readers/reconcilers require shared durable access. Actual permissions/topology are qualification evidence, not names in an environment file.
- Confirm dedicated prefix listing/pagination permissions and bounded orphan-inventory operation. Missing list capability, deletion retention locks or an unbounded cleanup backlog block acceptance until a bounded operational plan exists.
- Database protocol can reconstruct Redis status and tokens before deadline from SQL. Redis TTL cannot release reservations or delete committed outputs. Explicit public deadlines prevent replay from extending access indefinitely.
- Free mode retains private journal/discovery beyond public TTL, until cleanup acknowledgment. Surviving Redis data supports backend restart; catastrophic Redis data loss does not. Approve this weaker no-charge availability contract or require a separately reviewed durable free journal; do not smuggle in mandatory PostgreSQL.
- Retention proposals require review: one-hour public download lifetime, 15-minute physical grace, bounded processing no later than reservation expiry, and seven-day post-deletion tombstones. Verify legal/account-deletion policies and storage budgets. Persistent billing audit retention remains unchanged.
- Reconciler retry failures remain discoverable; use safe dead-letter/incident markers and alerting without deleting pointers. A dead-letter is not a successful refund, cleanup or completion.

## 8. Independent work and publication boundaries

This design is based directly on backend PR #13's characterization parent; the stacked diff must contain only these three documents. It does not modify frontend PR #24 or backend PR #13 descriptions, source or draft status.

Independent unreleased work remains separate:

- Backend preview-owner isolation PR #11, `e1d7734ee03ffeaa0041b51b593db56aa9c3b712`, and worker PR #1, `09067d0a18985f791de27c142d8cc1a750bde598`, retain their own coordinated compatibility review. This design must not be presented as deploying them or resolving mixed-version preview traffic.
- Backend Studio snapshot PR #12, `b7e825d252018ceb2733a873c74fc67528444ab4`, is not a base/dependency for OCR. Studio patterns are read as source context only.
- Frontend milestone `0f6fa13361778917afbeeef19e977f7bd8002944` is a read-only reference. Existing F-01/F-02 acceptance remains bounded; task-ID/protocol changes need future frontend compatibility qualification.

Implementation approval should authorize one bounded stage first: additive durable model and transaction-aware repository/billing primitives with deterministic PG tests, without enabling new acceptance. Approval of this design alone does not authorize even that stage.

## 9. Design publication validation

For this documentation task, lightweight Git checks must establish exact parent, Markdown-only staged diff, preserved frontend `AGENTS.md` and outer work, remote branch SHA and draft PR base/head. No schema command, database process, broad suite or runtime build is needed. GitHub CI, if triggered, is a separate observed result and cannot establish that this unimplemented architecture works.

Final approval gates remain: identity policy, free-mode catastrophic-loss contract, retention/deadline values, account-deletion cleanup, storage capabilities, coordinated release ownership, and the complete future matrix. If those cannot be approved, implementation is blocked at that boundary rather than silently choosing new billing or infrastructure policy.
