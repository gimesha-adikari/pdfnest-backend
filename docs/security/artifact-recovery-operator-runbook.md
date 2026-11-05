# Artifact recovery holds: operator workflow

This workflow is for expired asynchronous outputs placed in `artifact_recovery_hold` because the durable record contains an unsafe or invalid object key. Redis status is not an authority for this workflow.

## Safety contract

- The case APIs are registered under `/admin` and require both an authenticated session and the `admin` role.
- Every action requires an `Idempotency-Key` and a written reason. The PostgreSQL recovery ledger records the acting admin, exact task and original artifact identity, action, outcome, attempts, and timestamps.
- `verify` may inspect only the original canonical task key or an owner-matched successful Markdown Worker result. The candidate must match the durable SHA-256 and byte length.
- `recover` may copy only that verified Worker result to a deterministic opaque recovery key derived from the task and action identity. The key does not embed caller-controlled task text. It never changes the original task key, task state, billing reservation, or expiry. A recovery copy remains unavailable through task download routes.
- Recovery copying is disabled unless `ARTIFACT_RECOVERY_COPY_ENABLED=true` is explicitly configured after the applicable retention policy and provider behavior are approved. With the setting absent, the action is audited as `recovery_blocked_by_policy` and performs no storage operation.
- `close` records a non-destructive case decision. It does not mark physical deletion, delete an object, make an expired artifact downloadable, or change retention.
- This workflow never issues a refund or credit.

## Endpoints

- `GET /admin/artifact-recovery?limit=50&before=<RFC3339Nano>` lists at most 100 held cases.
- `GET /admin/artifact-recovery/{task_id}` returns a sanitized case summary and action history; raw storage keys and content hashes are omitted.
- `POST /admin/artifact-recovery/{task_id}` accepts `{"action":"verify|recover|close","reason":"..."}` and requires an idempotency header. Reasons must contain 8–500 characters.

The POST response states are `verified_owner_source`, `recovered_pending_retention_decision`, `recovery_blocked_by_policy`, a deferred or failed verification state, and `case_closed_non_destructive`. These are operator outcomes; none changes the completed billing decision.

## Schema release gate

`config/migrations/20261105_artifact_recovery.up.sql` is a separate additive migration. It is not run by application startup or the existing migrate command. Apply it only after the existing backend durability migration has been independently approved and qualified. `ValidateArtifactRecoverySchema` is read-only; missing or drifted schema makes only these case APIs unavailable. The down migration refuses to remove a non-empty audit ledger.

Before enabling recovery copying, the owner must approve the retention treatment and the storage-policy scope for the new `outputs/tasks/recovery/<opaque-digest>.md` objects. No physical-deletion deadline is implied by this workflow.
