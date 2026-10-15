# M1-S2-13 — Dormant OCR local-root safety

Base: `d8dd5d5e67ee85d38c4560f3c3ade3da6f1baa92` (draft PR #18). This is a dormant local-writer guard, not a retention system or live OCR correction.

## Hazard and correction

`GetLocalStorageDir()` defaults to `os.TempDir()/pdfnest-storage`. `internal/tasks/cleanup.go` scans the system temporary directory and can recursively delete aged entries with `pdfnest-` and other listed prefixes. It also scans children of `temp.GetDir()` without that prefix restriction. A focused regression against the unchanged parent reproduced successful creation under the default sweepable root; the test failed as expected before implementation.

Only the dormant Linux exclusive-create writer now opens its configured root through a safety guard. Rejection returns typed `ArtifactCreateUnsafeRoot` / `unsafe_local_root` before creating any OCR namespace, staging file or final artifact. Existing `GetLocalStorageDir()` may still create the empty configured directory; its behavior is unchanged.

The guard rejects:

- The system temporary directory itself.
- The active dedicated temporary workspace itself.
- Descendants of top-level system-temp entries matching the sweeper's prefixes.
- Dedicated-workspace descendants whose first child is removable by its child scan.
- Missing, noncanonical, non-directory, unresolved or symlink-aliased roots/anchors.

Classification uses `filepath.Rel` component boundaries, not an arbitrary string prefix. It preserves the current sweeper's `pdfnest-temp*` exemptions, and does not invent a dedicated scan when the dedicated workspace falls back to the system temp directory. A test compares the mirrored prefix list with the actual cleanup source to catch list drift; other future cleanup-rule changes still require reviewing this guard.

After classification, every root component is opened with descriptor-relative `O_NOFOLLOW`, preventing ancestor symlink substitution during opening. Existing staging, hard-link no-replace publication, hashing, synchronization and uncertain-outcome handling are unchanged. Permitted roots must remain under trusted, stable filesystem provisioning.

## Focused evidence

No cleanup worker was executed. Fixtures were task-owned temporary directories and synthetic PDFs. Tests cover default/nested/dedicated/system roots, all current sweep prefixes, exemptions and fallback behavior, sibling boundaries, missing/noncanonical/file roots, symlink aliases/ancestors/loops, absence of OCR output on rejection, and permitted creation plus exact readback.

Commands (explicit development/local selection; ambient R2/Paddle credential variables unset):

```sh
go test -json ./internal/storage -count=1 -timeout=2m
go test -race -json ./internal/storage -count=1 -timeout=2m
go test -json ./internal/ocr -run '^TestArtifact(Evidence(LocalPDF|InvalidBytes|KeyPolicy|UncertainWritesAndReplacement|ReadBoundaryFailures|LargeAllowedPDF)|ExclusiveIntentBinding)$' -count=1 -timeout=2m
go vet ./internal/storage ./internal/ocr
git diff --check
```

Results: storage **33 top-level passes / 58 including subtests**, in both normal and race runs; one intentional subprocess-helper skip in each parent runner. The helper is actually invoked by the existing separate-process tests. Existing competing-goroutine and different-content cross-process no-clobber tests pass under permitted roots. Selected OCR evidence tests: **7 top-level passes / 49 including subtests**, zero skips. Vet, targeted gofmt and diff checks pass. No final failures or race findings. The initial pre-fix default-root regression failed as expected. SQL tests and the full backend matrix were deliberately not run; no database or service provisioning was needed.

## Preservation and limits

No changes to cleanup behavior, general storage defaults, ordinary uploads, remote conditional PUT, billing, PostgreSQL lifecycle, Redis, public routes, frontend, worker, managed migrations or startup. The live OCR protocol and documented M1-S2-07 inconsistency remain unchanged. PR #18 is the unchanged parent.

This guard excludes paths reachable by the current application sweeper. It does not establish protection from OS cleanup, administrator actions, privileged directory relocation, mount aliases, object deletion, storage loss or future cleanup-policy changes. Existing M1-S2-12 durability limitations remain. No production storage or credentials were accessed. No database, Redis, cleanup daemon or live OCR service was started; test listeners and fixtures are cleaned by the test harness. Unrelated frontend `AGENTS.md` and outer repository work are preserved.
