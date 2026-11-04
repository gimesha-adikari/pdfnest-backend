# Platen PDF — Independent Evidence Correction Addendum

**Date:** 2026-11-04. **Scope:** Backend Draft PR #47 migration-operator correction only. This addendum supersedes blanket audit-closure language in the separate local operational report; it does not change original finding statuses without path-specific validation.

## Reviewed patch evidence

- `EXPECTED_DATABASE`, `EXPECTED_USER` and `EXPECTED_CLUSTER_SYSTEM_IDENTIFIER` are explicit mandatory operator pins. Never infer an approved pin from the database being connected to.
- `test-migration-operator.sh` includes additional negative checks for missing/malformed expected database identifiers, alongside the independently isolated dual-cluster mismatch test.
- The Python check compiles the actual embedded parser under the CI interpreter. URI and keyword/value authentication are exercised on disposable PostgreSQL by other tests. **This is not proof of execution on Python 3.8–3.11 or 3.13+.** Confirm operator Python version during the authorized release preflight.
- CI success demonstrates disposable qualification, not validation of the live production database, an accessible backup or a restored production snapshot. No production migration is authorized by this addendum.

## Original security audit — evidence-safe classifications

| Finding | Evidence currently established | Closure / release consequence |
| --- | --- | --- |
| **F-11** | Worker raster preallocation guard and tests exist (e.g. `tests/test_raster_admission.py`), while Worker `docs/F11_RASTER_SECURITY.md` separately identifies unresolved release qualification. | Candidate-code mitigation visible; **do not globally close** until full reachable paths and installed SDK/production qualification are accepted. Render-session tests alone do not prove F-11. |
| **F-15** | Worker `app/core/security.py` compares the SHA-256 of received raw-body bytes to the signed body digest; Backend signing and matching deployed secrets require coordination. | Scoped source mitigation supported; live integrated verification is still needed before asserting production closure. |
| **M1-S2-07** | Backend legacy OCR controllers now call `AdmitDurable` and `PublishArtifact`, with proof and settlement in `internal/tasks/durable.go`. | Strong candidate-code progress, **not blanket proof** that every legacy charged-without-artifact/crash-recovery scenario or existing production job is resolved. Retain path-specific acceptance evidence. |
| **F-07** | Git ingestion has application-level bounds. | OS/volume resource-exhaustion limitations remain; retain original acquisition-ceiling scope and separately verify all reachable paths. |
| **F-18** | Durable object-retirement/tombstone records exist. | A tombstone is not evidence of physical Cloudflare R2 deletion, retention compliance, or corrected editable-PowerPoint claims across the product. |
| **F-01** | Device/cloud privacy path is outside this bounded migration-operator correction. | Still tracked; no automatic closure or release waiver. |
| **F-02** | Password-restoration / protected-publication path is outside this correction. | Still tracked; no automatic closure or release waiver. |

**Do not use the statement “no unresolved audit findings remain.”** Release-specific exclusions require explicit, documented owner risk decisions and must not be equated with resolution.

## Unverified operational prerequisites

- **G-10:** Read-only catalog verification of the *actual* authorized production database (including cluster/system identifier and durability marker) remains pending.
- **G-11:** Encrypted backup and successfully restored, isolated **production-data** recovery rehearsal remain pending; synthetic CI restore tests are not equivalent.
- **G-12:** Migration, Worker deployment and Backend deployment remain blocked pending separate written authorization and successful earlier gates.
- **G-13:** Independent production recovery-key provisioning, off-cluster custody and recoverability remain unverified.

**Release decision: NO-GO.** Keep Backend PR #47 and Worker PR #13 draft and unmerged. No production data, migration, deployment, Railway setting, or default branch was changed by this evidence correction.
