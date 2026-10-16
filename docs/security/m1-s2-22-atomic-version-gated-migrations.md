# M1-S2-22 — Version-gated, atomic migration safety

Development qualification, 2026-10-16. No production execution or release approval.

## Baseline and preservation

Direct parent: `296838852195f96ca5590b3466a33ec5ae3e39d9`, independently verified local/remote master. Branch `milestone-1/managed-migration-atomic-version-gate`, targeting master. Held F-09 PR #25 (`f870124a8e921d666056c82efa6d44a6db332356`) and OCR PR #24 (`5b01d3a04f53d45397a55f4e7bb3577263fd03a0`) are separate and preserved. No models, schema version, startup call sites, workflow, Railway settings, billing, OCR or authentication implementation changed.

Read checkpoint `58b00b1bd19f3d598c85a62ef3b6b618e7c9f017`, document 19, and M1-S2-21 report/commands/evidence. All three earlier artifacts remained in `/tmp/platen-m1-s2-21-3c9lghih/`. Sanitized durable copies and a verified SHA-256 manifest are in `/home/gimesha/Documents/Codex/2026-10-16/m1-s2-22-ovy2myju/m1-s2-21-sanitized/`. Its selected archive has 66 members; raw database backup, binary, server/SQL logs and preservation diffs were excluded. Originals were unchanged. These are prior evidence, separate from this task's experiments.

## Finding and historical compatibility contract

Baseline `RunManagedMigrations` unconditionally called `RunSchemaMigrations`. It acquired/released a session advisory lock through separate pool operations, ran AutoMigrate regardless of the marker, then recorded the version, without an encompassing transaction. Startup checked only tables/marker. Before runtime edits, five new regressions reproduced acceptance of incompatible columns, nullability, unique index, FK and sequence.

The unchanged version is `20260918_01_admin_subscription_mutation`. The frozen contract and literal schema-only fixture were captured from a fresh execution of **unchanged baseline source**, before runtime modification, on PostgreSQL 18.6. They contain no customer data. The fixture's only record is a fixed synthetic ledger timestamp; historical expectations are independent of future models/AutoMigrate.

Inventory: **27 tables** (26 application models plus ledger), **367 columns, 101 indexes, 19 FKs, five sequences**. Required public ordinary tables/columns must match types/lengths, nullability, defaults, identity and generated status. Required indexes must match name/definition/uniqueness/primary status and be valid/ready. Required FKs must match actions/columns/deferrability, be validated and have all four normal enforcement triggers enabled. Sequence declared type/start/increment/bounds/cache/cycle/ownership must match; live sequence counters are not read/reset.

Extra columns on required tables must be nullable built-in pg_catalog base types without defaults/identity/generated behavior. Extra indexes must be nonunique plain-column btrees without expressions/predicates, using default built-in opclasses. Extra CHECK/UNIQUE/EXCLUSION constraints, FKs involving either end of a required table (including other schemas), enabled application triggers/rewrite rules, RLS and non-permanent tables reject. Applied ledger user write triggers are allowed because the gate is SELECT-only; a rejecting ledger trigger proves there are no ledger writes. Unconnected extra tables/harmless additions are allowed. No automatic repair or implicit adoption.

| Observed state | Managed migrate | Managed startup |
| --- | --- | --- |
| Exactly historical marker + compatible contract | Read-only validation; success | Same validation; success |
| Recorded version + incompatible definitions | Reject without DDL/repair | Reject |
| Unknown/additional markers or empty ledger | Reject; approved migration/adoption plan required | Reject |
| Missing ledger on populated schema | Reject even with initialization permission | Reject |
| Empty schema | Reject by default; explicit permission can initialize | Reject; never initialize |

Empty eligibility checks non-system relations, nondefault schemas, functions, types, namespace-owned objects including collations, and non-plpgsql extensions. This is not an attestation of roles, global settings, replication, event triggers or privileged administrators.

## Transaction and deadline design

The applied path and `ValidateManagedSchema` use PostgreSQL **READ ONLY, REPEATABLE READ** transactions. Only catalogs/ledger and transaction-local settings are accessed: no AutoMigrate, schema/ledger writes or migration advisory lock. A held ACCESS SHARE reader on a required table does not block a clean repeat; an incompatible AccessExclusive request would hit the bounded test deadline.

Fresh managed initialization now requires `PLATEN_ALLOW_SCHEMA_INITIALIZATION=true`; unset/false denies, invalid values reject. This justified behavior change affects new managed installations; existing compatible installations need no new setting. Runtime guards remain. `ConnectDatabase` still calls the same validation function in managed environments. Unmanaged development retains repeat model synchronization, now atomic/bounded.

One connection lease is reserved for the operation. One write transaction sets local timeouts and calls `pg_advisory_xact_lock(hashtext('pdfnest:managed-schema'))`. Eligibility is rechecked **after locking**, at READ COMMITTED. A concurrent initializer's committed win causes rollback/release, then a new genuine read-only validation. All ledger creation, actual GORM DDL, sequence creation and version recording use that transaction. Contract validation precedes commit. Nested transaction handles reject; there is no pooled-session unlock.

Defaults: **5s lock, 30s statement, 2m overall**, with earlier caller deadlines honored. Managed overrides: `PLATEN_MIGRATION_LOCK_TIMEOUT`, `PLATEN_MIGRATION_STATEMENT_TIMEOUT`, `PLATEN_MIGRATION_TIMEOUT`; minimum 1ms, lock/statement within overall. SQL rounding cannot produce zero/unlimited. These are finite safety bounds, not production timing qualification. Managed opening disables automatic unbounded Ping; transaction/connection acquisition uses the operation context.

Client cancellation may return before PostgreSQL processes a physical disconnect. Tests prove zero borrowed pool leases, then observe backend transaction disappearance with a bounded local barrier before checking zero locks/catalog rollback. No instantaneous or universal network-failure cleanup bound is claimed. Advisory exclusion is cooperative: do not run the old session-lock runner concurrently during rollout. Direct privileged DDL can race a read-only snapshot.

## Real PostgreSQL qualification

Fresh **PostgreSQL 18.6**, `127.0.0.1:5562`, task-owned data path/roles (`m1s222_test`, `m1s222_admin`). Guarded harness verifies loopback address, explicit port and `test_` name; creates/drops random task-owned databases. Small pools/bounded deadlines; no stress tests. Admin URL is used only to disable internal FK triggers on the same verified disposable server. No production URL, environment file or customer data; protected M1-S2-07 directory unused.

```sh
export PLATEN_MIGRATION_TEST_DATABASE_URL='postgres://m1s222_test@127.0.0.1:5562/test_m1s222_root?sslmode=disable'
export PLATEN_MIGRATION_TEST_ADMIN_DATABASE_URL='postgres://m1s222_admin@127.0.0.1:5562/test_m1s222_root?sslmode=disable'
go test -p 1 ./config -count=1 -timeout=4m -json
go test -race -p 1 ./config -count=1 -timeout=5m -json
go vet ./config .
gofmt -l config/migrations.go config/schema_contract.go config/migrations_test.go
git diff --check
```

Both final config runs: **24 top-level tests, 66 leaf cases (56 migration cases), 77 pass events including parent tests, zero failures/skips**. No race diagnostics. Vet/format/whitespace passed. Unchanged GitHub CI compiles Go/builds a container; it does not execute these regressions/race/vet.

| Proof | Result |
| --- | --- |
| Compatible repeat | Zero captured DDL; independent catalog digest and ledger timestamp unchanged; rejecting ledger-write trigger untouched; ordinary reader held |
| Incompatible/ambiguous schema | Column/type/default/nullability, missing/substituted/invalid indexes, FK definitions/enforcement, sequence ownership/increment, write restrictions and marker ambiguity reject without repair |
| Explicit empty initialization | 27 tables, one exact marker, full contract; repeat with initialization permission still emits no DDL |
| Independent callers | Actual pg_blocking_pids owner/waiter barrier; only winner executes DDL; loser rechecks committed winner and emits zero DDL |
| Connection churn / small pools | Independent one-connection pools with idle eviction, shared one/two-connection pools; actual pool wait/PostgreSQL blockers; no stranded advisory lock |
| Late failure | Own backend termination after final sequence DDL; cancellation after actual ledger INSERT: zero public relations/ledger after rollback; clean retry succeeds |
| Bounded waits | Actual advisory blocker; lock timeout 55P03, statement timeout 57014; cancellation and 150ms overall pool-acquisition deadline; no schema/pooled lease/lock residue |
| Managed startup | Same read-only gate accepts compatible history, rejects drift/empty/unknown; call site unchanged |
| Unmanaged development | Automatic fresh creation and missing-column restoration still work, inside transaction exclusion |

Normal independent-pool PID pairs: **760723/760724** (owner/waiter), **760731/760730** (churn). Race: **762561/762562**, **762608/762607**. Owner 760723 server log recorded 102 DDL statements on one backend: first ledger CREATE logged before XID allocation (`0`), all subsequent DDL shared **XID 1565**. Race owner 762561 similarly used **1761**. Actual GORM operations here are transactional CREATE/ALTER/INDEX, with no CONCURRENTLY. Late-failure rollback proves effective connection/DDL ownership. CONCURRENTLY appears only in a negative test seeding an invalid index, never in the runner.

Normal timeout/cancel waiter/blocker PIDs: **760752/760753**, **760757/760758**, **760762/760763**; race **762632/762633**, **762638/762639**, **762643/762644**. Finite polling observes actual blockers/backend state; sleeps do not establish ordering.

### Failed experiments, review and correction

Five drift cases failed as intended before runtime edits. Later negatives exposed nine write restrictions; source review exposed six more false positives and an unversioned collation. Those negative expectations failed before fixes and pass in the final matrix. Reproducing unsafe behavior is not counted as qualification of safety.

An early full matrix had two immediate cancellation-cleanup assertion failures. Go/sql/pgx investigation and PostgreSQL observations showed transient `idle in transaction` before disconnect processing completed. Connection pinning and bounded **observed backend cleanup** replaced the incorrect instantaneous assumption; late-ledger cancellation uses that barrier too. Final normal/race pass; earlier failures remain in local evidence. Independent focused source review found no remaining blocking issue; that reviewer did not execute tests.

### Existing schema consumers

Separate `test_m1s222_auth`, `test_m1s222_billing`, `test_m1s222_studio` databases were seeded with the literal historical schema. Each command below received its explicit verified local URL on port 5562 (placeholders are local-only fixture variables):

```sh
DATABASE_URL="$local_auth_fixture" LOCAL=true APP_ENV=development RESEND_API_KEY='' FROM_EMAIL='' \
 MAILER_OUTBOX_DIR="$task_owned_outbox" go test -p 1 ./internal/auth \
 -run '^(TestEmailVerify_|TestResendVerification_AUTH004_)' -count=1 -timeout=3m -json
GIM6_TEST_DATABASE_URL="$local_billing_fixture" go test -p 1 ./internal/billing \
 -run '^(TestFreeModeAccountDatabaseAllocationDoesNotMutateExhaustedBillingState|TestNormalModeAccountAllocationStillRejectsExhaustedBillingState|TestNormalModeAccountAllocationStillUsesPurchasedCredits|TestAuthenticatedReservationFinalizationUsesRealIDAfterModeChangesToFree)$' \
 -count=1 -timeout=3m -json
DATABASE_URL="$local_studio_fixture" go test -p 1 ./internal/studio/models \
 -run '^Test1_ActualDatabaseConstraintsAndIndexes$' -count=1 -timeout=2m -json
```

Auth **19 top-level / 22 leaf**, billing **4**, Studio **1** passed; no failures/skips. Local outbox prevents external email. Baseline still contains F-09 logging; synthetic-token output is retained locally, not published. After existing fixture AutoMigrate/CRUD, a separately executed `ValidateManagedSchema` accepted all three full databases. This qualifies selected consumers, not all application behavior. No broad OCR/frontend/worker matrix ran.

## Limits, review and production gates

Snapshot canonical rendering is qualified on PostgreSQL 18.6; other majors/GORM upgrades require review. Exact names/defaults may reject semantically equivalent installations, requiring an adoption plan rather than repair. This is structural compatibility, not role/ACL, locale/collation, arbitrary function/operator, replication/event-trigger, data invariant, storage capacity or full readiness qualification. Sequence counters and customer rows are outside scope.

Reviewer checklist: fixture provenance/inventory; strict unknown/adoption behavior; no DDL/ledger writes on applied path; same-connection xact lock; post-lock recheck; actual GORM atomicity/late rollback; timeouts/disconnect limits; opt-in managed initialization; preservation and independent ancestry. Future schema evolution requires a reviewed versioned migration plan, not updating this frozen contract to hide drift.

**Passing synthetic tests does not establish live production schema compatibility, verified production backups, recoverable database state or authorization to release F-09.** Separate owner gates: authorized read-only production catalog comparison; reviewed drift/DDL/locking impact; verified backup point/restore readiness and recovery objectives; migration role/timeouts/failure policy; actual Railway auto-deploy/pre-deploy coordination; final composed immutable candidate and readiness checks. Older-image rollback may execute the old unsafe runner: a reviewed database/deployment recovery plan is required.

Railway `/app/server migrate` is unchanged in configuration. F-09 remains held; historical log handling and F-01/F-02 closure remain separate. The active M1-S2-07 charged-without-artifact defect is unresolved. No production query, credential inspection, migration, configuration change, merge or deployment occurred. No next implementation task is authorized.

## Local closure

Before shutdown the task-owned server had zero advisory locks and zero other client sessions. It was stopped cleanly; port 5562 is no longer listening. Only the independently verified task-owned `pgdata` was removed. Evidence/manifests remain in the durable local task directory. Final free disk approximately 9.2 GiB. Original branch refs, outer staging/unrelated diff and frontend `AGENTS.md` diff matched the preservation hashes; protected historical directories were not modified.
