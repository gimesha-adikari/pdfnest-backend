# M1-S2-16 — Dormant OCR Success-Replay and Maintenance-Revision Qualification

## Executive Summary and Scope Boundary

**Status:** COMPLETE (Deterministic PostgreSQL characterization verified; zero production mutations)  
**Starting Baseline:** `725f99bbdcf951d810e7781bfb7d471734970d45` (PR #20 HEAD on `milestone-1/ocr-private-namespace-guard`)  
**Parent Commit:** `bf471963ac83bc4da302b2f0de8ab37117e84e6a` (PR #19)  

> [!IMPORTANT]
> **This task does not fix the live M1-S2-07 charged-without-artifact defect or activate the new OCR protocol.**
> The SQL finalization repository remains strictly dormant. No production code, migration, HTTP route, background worker, or configuration flag was introduced or modified.

The mission of M1-S2-16 was to establish, using executable loopback PostgreSQL tests, whether the existing dormant `ocrFinalization` repository preserves successful-decision idempotency when subsequent maintenance operations modify recovery metadata, advance row revisions, or advance update timestamps.

---

## Current Implementation's Replay Contract

In [`internal/ocr/finalization_repository.go`](file:///home/gimesha/My_Projects/platen/pdfnest-backend/internal/ocr/finalization_repository.go#L194-L232), `succeedInTx` handles retries of an already-successful task under row lock (`SELECT ... FOR UPDATE`):

```go
if job.State == "succeeded" {
    if req.ProcessingClaim == "" || job.ProcessingClaim != req.ProcessingClaim {
        return errFinalizationBinding
    }
    // Only a retry of this same decision (original or resulting revision).
    if req.ExpectedState != "ready" || (req.ExpectedRevision != job.Revision && req.ExpectedRevision != job.Revision-1) {
        return errFinalizationRevision
    }
    res, err := billing.Default.LockReservationInTx(tx, job.billingBinding())
    if err != nil {
        return err
    }
    if res.Status != "committed" {
        return billing.ErrReservationState
    }
    return nil
}
```

Additionally, [`lockFinalization`](file:///home/gimesha/My_Projects/platen/pdfnest-backend/internal/ocr/finalization_repository.go#L170-L185) enforces chronology:
```go
if job.ProtocolVersion != 1 || job.OwnerID != req.OwnerID || job.ReservationID != req.ReservationID || job.ToolName != billing.ImageToTextPDF.Name || req.Now.Before(job.UpdatedAt) {
    return nil, errFinalizationBinding
}
```

To achieve successful acknowledgment on a replay, a request must satisfy **five concurrent constraints**:
1. Exact task binding (`TaskID`, `OwnerID`, `ReservationID`, `ToolName`).
2. Exact matching processing claim (`job.ProcessingClaim == req.ProcessingClaim`).
3. Exact matching artifact descriptor (`validArtifact` and `sameArtifact`).
4. Revision restriction: `req.ExpectedRevision == job.Revision` OR `req.ExpectedRevision == job.Revision - 1`.
5. Chronology constraint: `!req.Now.Before(job.UpdatedAt)`.

---

## Deterministic Test Scenarios and Observed Behavior

All scenarios were executed in [`internal/ocr/finalization_maintenance_replay_test.go`](file:///home/gimesha/My_Projects/platen/pdfnest-backend/internal/ocr/finalization_maintenance_replay_test.go) against a disposable PostgreSQL 18.6 instance with independent observer pools inspecting durable committed state.

### Characterization Matrix

| Scenario | Injected Synthetic Maintenance | Replay Request Parameters | Observed Replay Result | Durable SQL State & Accounting |
| :--- | :--- | :--- | :--- | :--- |
| **Case A: Baseline Retry** | None. Row is settled at Revision 5, `UpdatedAt = T_succeed`. | Original request with `ExpectedRevision = 4`, `ExpectedState = "ready"`, `Now = T_succeed`. | **PASS (ACK: `nil`)** | `State: succeeded`, `Res: committed`, Units: 7, Credits: 50, Logs: 1, Rev: 5 |
| **Case B: Metadata-Only Maintenance** | Updated dormant fields: `NextAttemptAt`, `RecoveryClaim`, `RecoveryLeaseUntil`, `CleanupNotBefore`, `DownloadExpiresAt`. `Revision = 5`, `UpdatedAt = T_succeed` untouched. | Original request (`ExpectedRevision = 4`, `Now = T_succeed`). | **PASS (ACK: `nil`)** | `State: succeeded`, `Res: committed`, Units: 7, Credits: 50, Logs: 1, Rev: 5. Recovery fields preserved. |
| **Case C: Revision Advanced by Maintenance** | Maintenance acquired recovery lease: `Revision` bumped from 5 to 6. `UpdatedAt = T_succeed`. | Original request (`ExpectedRevision = 4`, `Now = T_succeed`). | **REJECTED (`errFinalizationRevision`)** | `State: succeeded`, `Res: committed`, Units: 7, Credits: 50, Logs: 1, Rev: 6. Settlement uncorrupted. |
| **Case D: UpdatedAt Advanced Independently** | Maintenance updated `UpdatedAt` to `T_maint = T_succeed + 1m`. `Revision = 5`. | Original request with captured timestamp `Now = T_succeed < T_maint`. | **REJECTED (`errFinalizationBinding`)** | `State: succeeded`, `Res: committed`, Units: 7, Credits: 50, Logs: 1, Rev: 5. (Retry with `Now > T_maint` passes). |
| **Case E: Multiple Housekeeping Revisions** | Two successive maintenance lease acquisitions: `Revision` bumped from 5 to 7. `UpdatedAt = T_succeed`. | 1) `ExpectedRevision = 4` (pre-decision)<br>2) `ExpectedRevision = 5` (post-decision) | **REJECTED (`errFinalizationRevision`)** for both revisions | `State: succeeded`, `Res: committed`, Units: 7, Credits: 50, Logs: 1, Rev: 7. Settlement uncorrupted. |
| **Case F: Strict Rejection Invariants** | Various invalid permutations on settled task. | 1) Wrong Owner<br>2) Wrong Reservation<br>3) Wrong Claim<br>4) Conflicting Artifact<br>5) Cancelled Task<br>6) Failed Task | 1) `errFinalizationBinding`<br>2) `errFinalizationBinding`<br>3) `errFinalizationBinding`<br>4) `errFinalizationBinding`<br>5) `errFinalizationTerminal`<br>6) `errFinalizationTerminal` | In all cases, zero billing corruption, zero double deductions, terminal states immutable. |

---

## Core Findings and Architectural Answers

### 1. Does an original success request remain idempotent when only recovery metadata changes?
**YES.**  
As demonstrated by Case B, if background maintenance updates only the dormant recovery fields (`NextAttemptAt`, `RecoveryClaim`, `RecoveryLeaseUntil`, `CleanupNotBefore`, `DownloadExpiresAt`) without advancing `Revision` or `UpdatedAt`, legitimate success replays remain 100% idempotent and succeed without error.

### 2. Does advancing `Revision` invalidate it?
**YES.**  
As characterized in Case C and Case E, the current replay logic strictly requires `req.ExpectedRevision == job.Revision` OR `req.ExpectedRevision == job.Revision - 1`. If any maintenance worker advances `Revision` (e.g. from 5 to 6 or 7), legitimate client retries carrying the original revision (4) are rejected with `errFinalizationRevision`.

### 3. Does advancing `UpdatedAt` invalidate it?
**YES.**  
As characterized in Case D, `lockFinalization` rejects any request where `req.Now.Before(job.UpdatedAt)`. In distributed networks or gateway retries where a client retransmits its original request timestamp, advancing `UpdatedAt` during maintenance causes the retry to fail with `errFinalizationBinding`.

### 4. Can a future cleanup scheme avoid this incompatibility without compromising decision fencing?
**YES.**  
There are two defensible patterns:
- **Pattern 1 (Decoupled Generation Fencing):** Keep the task row's primary `revision` strictly bound to authoritative decision transitions (`accepted → processing → ready → succeeded/cancelled/failed`). Maintenance and recovery lease acquisitions should increment a dedicated `recovery_generation` column instead of the task's decision `revision`.
- **Pattern 2 (Terminal Decision Replay Exemption):** In `succeedInTx`, when `job.State == "succeeded"`, verify owner, reservation, processing claim, and exact artifact descriptor, but do not reject solely because maintenance advanced `job.Revision > req.ExpectedRevision + 1`. Since the reservation is already committed and the decision is terminal, the client is only asking for acknowledgment of a finalized decision.

### 5. Would independently tracking a decision revision or stable decision fingerprint be necessary?
**YES, highly recommended.**  
Tracking a `decision_revision` (the revision at the instant `succeeded` was committed) ensures that client retries can be compared against the authoritative decision revision rather than being disrupted by background housekeeping revisions.

### 6. Could a separate recovery generation preserve more reliable semantics?
**YES.**  
Separating task lifecycle revisions (`revision INT`) from maintenance fencing (`recovery_generation INT` or `recovery_claim UUID`) prevents maintenance operations (such as lease claiming, lease extension, or retry rescheduling) from interfering with task-level optimistic concurrency.

### 7. What exact implementation acceptance test should be required before cleanup methods may be added?
Before any cleanup repository methods (`claimCleanupLeaseInTx`, `completeCleanupInTx`) are introduced in M1-S2-17 or beyond, the test suite must assert that:
- A task settled at revision $R_{decision}$ can successfully acknowledge client replays with `ExpectedRevision = R_{decision}-1` even after $N \ge 1$ cleanup lease acquisitions have occurred.
- Housekeeping updates do not prevent idempotent replay of terminal decisions.

---

## Test Verification and Infrastructure Details

- **Test Infrastructure:** Isolated loopback PostgreSQL 18.6 on port 5556, database `test_m1_s216_utf8`, role `m1_s216`.
- **Test File:** [`internal/ocr/finalization_maintenance_replay_test.go`](file:///home/gimesha/My_Projects/platen/pdfnest-backend/internal/ocr/finalization_maintenance_replay_test.go)

### Commands Executed:
```bash
# 1. Focused test run
PLATEN_M1_BILLING_TEST_DATABASE_URL="postgres://m1_s216@127.0.0.1:5556/test_m1_s216_utf8?sslmode=disable" \
go test -p 1 ./internal/ocr -run '^TestOCRSuccessReplayMaintenanceCharacterization$' -count=1 -v

# 2. Race detector execution
PLATEN_M1_BILLING_TEST_DATABASE_URL="postgres://m1_s216@127.0.0.1:5556/test_m1_s216_utf8?sslmode=disable" \
go test -race -p 1 ./internal/ocr -run '^TestOCRSuccessReplayMaintenanceCharacterization$' -count=1 -v

# 3. Static analysis and formatting
go vet ./internal/ocr
git diff --check
```

### Verification Results:
- Top-level test: 1 passed.
- Subtests: 11 passed (Cases A, B, C, D, E, and 6 Case F subtests), 0 failed, 0 skipped.
- Execution time: 1.58s (standard), 2.19s (race detector).
- Race detector: **Zero race warnings or memory leaks detected.**
- Go vet: **Passed cleanly (exit code 0).**
- Git diff check: **Passed cleanly (exit code 0).**

Following verification, the task-owned PostgreSQL server was stopped via `pg_ctl stop` and its disposable data directory was removed. The historical M1-S2-07 test database directory remains preserved.
