# M1-S2-23C — Historical migration-version compatibility

## Scope and source evidence

This correction changes the exact migration-ledger histories accepted by the managed read-only compatibility gate. It does not change the historical schema contract, migration fixture, migration execution, initialization, or any production configuration.

The implementation branch starts at merged backend `master` commit `d661e0aa5b52810424b6f71e696e95a70de4e161`. Source history confirms that commit `25a26520d8980a21719d8cdf028fb8141f79b90d` recorded `20260914_01`. The later ancestor commit `ccc185dd45831598aae258a8277063cc6136c4f8` changed the current marker to `20260918_01_admin_subscription_mutation`. Both historical runner versions insert the current marker with `ON CONFLICT (version) DO NOTHING`; neither deletes a prior marker. The owner-provided production viewer evidence of these two rows is consistent with that source history. Production was not queried for this code-only task.

The previous validator required one row, equal to the current marker. It therefore rejected the historically written two-row set before invoking the frozen structural validator.

## Version policy

The gate accepts only these exact sets:

| Accepted set | Result |
| --- | --- |
| `20260918_01_admin_subscription_mutation` | Continue to structural validation |
| `20260914_01` plus `20260918_01_admin_subscription_mutation` | Continue to structural validation |

An older marker alone, an empty or missing ledger, any unknown marker, any missing current marker, more than two rows, duplicate rows, and malformed marker values remain rejected. The code does not treat “current marker present” as sufficient.

For either accepted set, `checkManagedReadOnly` still calls `validateHistoricalContract` in its PostgreSQL `READ ONLY` repeatable-read transaction. Thus version recognition alone never establishes schema compatibility. Both `ValidateManagedSchema` and the managed migration entry point use this same gate. An accepted already-applied schema returns before migration-lock acquisition or migration execution.

## Regression evidence

The disposable test harness created a fresh PostgreSQL 18.6 cluster bound to loopback and used its guarded `test_` databases. The regression suite adds ten ledger-history cases and four two-version structural-drift cases: 14 database scenarios and 28 calls across managed startup validation and the managed migration entry point. An independent database connection snapshots catalog and ledger metadata before and after. The tests also inspect traced statements and query PostgreSQL advisory-lock state to establish that compatibility checks perform no DDL, DML, ledger rewrite, or migration advisory-lock request.

The historical fixture remains unchanged and starts with the current marker. Tests add or alter only synthetic ledger rows in disposable test databases. The structural drift controls cover a required column, index, foreign key, and sequence with the recognized two-version history still present; all fail closed without repair.

Executed checks and results:

- Focused ledger and structural regression: `go test -p 1 ./config -run '^(TestManagedMigrationHistoricalLedgerCompatibility|TestManagedMigrationRecognizedHistoryStillRejectsStructuralDrift)$' -count=1 -timeout=4m` — passed in 1.451s.
- Complete config suite: `PLATEN_MIGRATION_TEST_DATABASE_URL=<loopback test_admin URL> go test -p 1 ./config -count=1 -timeout=8m` — passed in 9.742s.
- Race-enabled config suite: `PLATEN_MIGRATION_TEST_DATABASE_URL=<loopback test_admin URL> go test -race -p 1 ./config -count=1 -timeout=10m` — passed in 13.245s; no race diagnostics.
- `go vet ./config` — passed.
- `gofmt -d config/migrations.go config/migrations_test.go` — no output.
- `git diff --check` — passed.

The first pre-fix focused run failed on the two-known-version case with the old “missing or unrecognized migration version” error, reproducing the defect. All required PostgreSQL scenarios ran; none were skipped.

## Limits

The production row values and structural catalog were not independently queried here. The production values were supplied as owner evidence; this implementation does not prove that production matches the frozen schema contract. A production read-only catalog comparison and owner-controlled release qualification remain separate gates. No production SQL, migration, configuration change, merge, or deployment was performed. Independent source review is required before any merge or deployment authorization.
