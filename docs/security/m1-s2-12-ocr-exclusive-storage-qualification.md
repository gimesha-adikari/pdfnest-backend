# M1-S2-12 — Dormant OCR exclusive-create storage qualification

## Scope and source baseline

Development foundation only, based directly on `2a2ade3207d9100c84b19a6adced605d4c7de605` (`milestone-1/ocr-artifact-evidence-foundation`, draft PR #17; parent `ef3146d85052c582ef76b2d6926dad0586b119de`). This work belongs on `milestone-1/ocr-exclusive-storage-foundation`, stacked on #17. No protocol activation, managed migration, cleanup service, billing change, merge or deployment is included.

The M1-S2-08 design/verification/rollout documents and M1-S2-11 evidence report remain authoritative historical records. **The live M1-S2-07 charged-without-artifact and lost-completion-acknowledgment defects remain unresolved.** Passing their characterization tests does not fix them.

Source reviewed:

- `internal/ocr/artifact_evidence.go`, its tests, finalization model/repository/authority and lifecycle tests: private intent capture, claim/revision/owner binding, readback evidence and locked readiness.
- `internal/storage/storage.go`: selector, local staging/rename, arbitrary-key save/delete and permissive legacy resolution.
- `internal/storage/r2.go`: MinIO construction, application AES-GCM, upload/presign/delete.
- `internal/storage/artifact_read.go` and tests: strict bounded logical readback and typed absence/outage/integrity errors.
- `internal/storage/ownership.go` and controller: server-generated owned-upload prefixes and allowed purposes.
- `internal/ocr/r2_jobs.go`, `internal/tasks/cleanup.go`, Studio storage cleanup: actual generated keys and cleanup authority.
- Installed `github.com/minio/minio-go/v7 v7.2.1`: `api-put-object.go`, `api-put-object-streaming.go`, `api.go`, `hook-reader.go` and signing implementation. No dependency upgrade.

Graph tools were unavailable in this session; material claims use direct source reads and bounded searches. This is not a graph-completeness claim.

## Original behavior and reachable namespace

Existing local `SaveLocalStream` uses a random staged file followed by ordinary rename, replacing an existing destination. `Store.UploadFile`/`UploadBytes` use unconditional PUT. M1-S2-11's replacement regression still demonstrates this behavior and that earlier verification is point-in-time evidence.

Before this change, only the dormant OCR intent factory and tests minted `ocr-finalization/outputs/` keys. No current HTTP OCR processor creates this namespace. Public upload preparation replaces client key/prefix values with new `owned/<owner-hash>/<purpose>/...` keys; supported purposes are image-to-text PDF, repository analyzer and Studio source. The live OCR job writes `outputs/tasks/<task>/<generated-name>` and cleans its own generated output and owned input references.

Nevertheless, lower-level `SaveObjectFile`, `SaveObjectBytes`, local saves, `Store.UploadFile`, `UploadBytes`, `PresignPutObject` and generic deletion APIs can address arbitrary canonical keys, including the new private prefix. Studio cleanup consumes stored cleanup-row object keys through generic deletion. Ordinary Studio operations do not mint OCR output keys; a privileged caller or inconsistent database record remains a separate trust risk. No global writer/deleter behavior is changed here.

Bounded application-call inventory (in addition to the generic storage primitives):

| Current caller | Destinations / deletion source | Private-prefix reachability |
| --- | --- | --- |
| OCR and conversion task controllers | Generated `outputs/tasks/...`; Markdown `jobs/markdown/source/...` | No ordinary caller-chosen OCR-finalization output key |
| Editor and markup controllers | `NewOwnedKey` for source; generated `edit/layout` / `markup/payload` | No ordinary private-prefix creation; their internal persistence helpers remain broad |
| OCR V2 service | Generated `jobs/ocr_v2/...` input keys; generic artifact-store delegate | No ordinary private-prefix creation; delegate can write/delete supplied keys internally |
| Analyzer upload | Generated upload key passed to local stream save | No new namespace adoption |
| Studio source/finalizer/job coordinator | Studio-generated source/snapshot/job keys; cleanup delegates consume server/repository keys | Privileged storage/cleanup helpers could address this prefix if supplied an inconsistent key |
| Public presign controller | Replaces request prefix/key with owned-purpose keys | Client cannot select the private prefix through this route |
| Generic local temporary sweeper | Aged filesystem entries, not task ownership | Can delete local output root if located in swept areas |

The temporary sweeper can recursively remove aged matching directories. In particular, default local `/tmp/pdfnest-storage` matches its shared-temp `pdfnest-` prefix; storage inside the dedicated temporary workspace is also unsuitable for retained artifacts. **Activation requires a durable, pre-provisioned local root outside those swept paths, with protected deletion authority.** This task documents the hazard without changing cleanup or retention.

## Private key and upload authority

The existing server factory remains the only dormant OCR constructor:

`ocr-finalization/outputs/<SHA256(domain, owner, task, processing-claim)>/<UUIDv4>.pdf`

The domain-separated tuple is JSON encoded. Fresh random artifact IDs and server-generated claims avoid accidental reuse. Storage accepts exactly this canonical shape: lowercase 64-character hexadecimal binding, canonical UUIDv4, `.pdf`, no extra components, source prefix, absolute path, traversal, encoding trick or normalization. Randomness does not imply physical immutability.

The storage API is an internal mechanism, not an authentication proof. The private OCR adapter accepts a captured SQL intent binding task, owner, current processor claim, revision, artifact ID, key, digest and size. `captureArtifactInTx` validates the locked current row; `persist` checks the captured snapshot and lease before starting. A new claim cannot claim an old attempt's key. No public caller can choose a destination through this interface.

Captured authority can become stale while I/O is underway. A completed upload is not proof that authority remained current until commit. Cancellation may win after upload begins; the late object is retained as an orphan and existing `readyInTx` rejects the cancelled row. Claim expiry prevents evidence/readiness. Readiness still checks the current locked row, revision and claim. No SQL lifecycle logic changes.

## Additive storage interface

`CreateArtifactIfAbsent(ctx, key, sourcePath, logicalSHA256, logicalByteSize)` reuses the existing storage selector. The dormant `selectedArtifactStore.persist` calls it instead of unconditional save. Existing writers, readers and their callers retain their behavior.

Both implementations require positive bounded size (maximum 250 MiB), a regular source file and exact logical digest/size. Local copying streams and hashes the bytes actually staged. Remote encryption uses the existing bounded whole-message AES-GCM representation; its memory cost remains proportional to the file, with both logical and encrypted buffers. This stage does not introduce a streaming encryption format.

The writer establishes creation and byte association, not PDF validity. Existing independent readback, pdfcpu strict structural validation, bounded Ghostscript null-page inspection, digest, size and page-count checks remain necessary before private evidence can authorize readiness. No header-only validation, ETag-as-PDF-hash or password-preservation claim is introduced.

## Linux local no-clobber publication

1. Open the configured root as a directory with `O_NOFOLLOW`.
2. Walk/create each canonical component using held directory descriptors, `mkdirat(0700)` and `openat(O_DIRECTORY|O_NOFOLLOW)`. Even an in-root symlink alias is rejected.
3. Exclusively create a deterministic private `.ocr-stage-<artifact-id>.pdf` slot with `O_CREAT|O_EXCL|O_NOFOLLOW`, mode `0600`.
4. Stream bounded bytes, checking actual size and SHA-256. Synchronize and close the completed staged file.
5. Atomically publish with `linkat(stage, final)`. An existing final name yields conflict; no rename, check-then-create or overwrite fallback is used.
6. Remove only this call's staging alias, then synchronize the leaf and all held ancestor directories through the configured root, bottom-up.

Only a complete, file-synchronized inode is published under the final key. Separate processes use the same kernel exclusion rules; no application mutex establishes exclusivity. Another writer cannot replace the final inode through this API. Existing names, including symlinks at the final name, conflict safely.

A live or crashed staging slot returns uncertainty, not absence. Crash debris is bounded to one slot per intent; retries do not create new staging names. A future authorized recovery policy must decide how to inspect/remove abandoned staging slots or fail the intent. This task adds no such service. Normal calls remove only staging files they created; a successful unlink relinquishes ownership so deferred cleanup cannot remove a later writer's staging file.

File-sync failure before publication yields no new final object. Unknown link outcomes, staging-unlink failure or directory-sync failure after publication remain uncertain and retain the final object. Filesystems without the required hard-link operation fail closed. Non-Linux local adapters return unsupported.

**Durability limit:** file and directory synchronization are exercised on the current Linux filesystem, including injected failures. The configured root and its external ancestors must already be durably provisioned. Tests kill a process during staging, but do not simulate power loss, dishonest hardware caches, filesystem failure, hostile directory mutation, or a new filesystem's semantics. No unconditional crash-durability or retention guarantee is claimed.

## Conditional remote creation: SDK and provider evidence

Cloudflare documents `If-None-Match: *` on single-object PUT as an atomic create-if-absent condition, including concurrent writers and the distinction between a negative read and an in-flight write. This is provider documentation, not an actual R2 test. [Cloudflare R2 conditional operations](https://developers.cloudflare.com/r2/api/s3/extensions/)

AWS documents HTTP 412 for a failed precondition and HTTP 409 for a conflicting conditional operation. Those outcomes remain distinct from transport uncertainty. Multipart completion is a separate contract and is deliberately unused. [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)

Installed MinIO v7.2.1 supplies `PutObjectOptions.SetMatchETagExcept("*")`. `Header()` carries it into request metadata before SigV4 signing. Explicit known ciphertext size plus `DisableMultipart=true` selects single PUT; no initiation/part/completion sequence is used. `DisableContentSha256=true` avoids streaming signed-chunk transformations; the HTTPS request retains signed conditional headers and uses the SDK's supported unsigned-payload mode. Existing application GCM encryption occurs before PUT. Provider ETags describe physical bytes and are ignored for logical verification.

A subtle SDK detail matters: `newHook` wraps even a nonseekable source in a seek-capable hook, and `executeMethod` retries seekable bodies. The private reader permits the initial rewind but rejects subsequent rewinds. Thus a lost acknowledgment cannot be hidden by a second conditional PUT returning 412. Local protocol tests use an SDK client configured for four retries yet observe exactly one PUT on 503, interrupted transport and lost acknowledgment. The SDK may enter retry/backoff before the replay guard rejects its next rewind; this is not a claim that no retry bookkeeping occurs. Caller cancellation/deadline is preserved.

The new remote API accepts only standard HTTPS `<32-lowercase-hex-account>.r2.cloudflarestorage.com` endpoints. Other S3 endpoints, insecure R2 endpoints and unqualified variants fail closed before network I/O. A private struct field, set only in package tests, permits loopback protocol simulation; environment configuration cannot enable it. A 501/unsupported response never falls back to unconditional PUT. Other R2 endpoint variants require later explicit qualification.

The protocol server checks the exact PUT path/header and absence of multipart query parameters, verifies the transmitted SigV4 authorization with synthetic keys, and atomically commits at most one object under a mutex modeling the documented provider operation. Tests use the actual MinIO HTTP path and actual GCM encrypt/decrypt. This is **simulated provider behavior**, not R2, MinIO-server or production-bucket qualification. Actual R2, TLS/provider network behavior, bucket policy and regional endpoint compatibility remain activation gates.

## Error and ambiguity contract

All errors contain bounded categories, not source paths, keys, credentials or provider response bodies.

| Category | Meaning and required behavior |
| --- | --- |
| `invalid_key_or_source` | Reject malformed namespace, invalid metadata or source digest/size mismatch; no new final object |
| `already_exists` | Local final-name conflict or remote 412; never overwrite; does not prove matching bytes |
| `conditional_conflict` | Remote 409; retain intent and reconcile; do not infer object presence/absence |
| `denied` | Local permission denial or remote 401/403; not object absence |
| `exclusive_create_unsupported` | Unknown endpoint/platform, unsupported atomic publication or remote 501; no unconditional fallback |
| `not_applied` | This invocation stopped before publication/PUT; not proof that an earlier attempt or another writer did not commit |
| `uncertain_retain_intent` | Existing staging slot, unknown publication response, failed post-publication sync, network/timeout/interruption/503/lost acknowledgment; no output deletion or replacement |

Existing strict read errors remain distinct: confirmed missing at that read instant, denied, unavailable/uncertain, integrity, too large and temporary-file failure. A `NoSuchKey` observation while an earlier PUT is in flight cannot resolve that PUT as failed. Tests hold PUT before commit, obtain a missing GET, then permit the same PUT to commit.

Future coordination must reconcile a retained intent through exact-key independent logical readback. Matching digest, size and valid PDF/page count can establish the intended object; a conflict alone cannot. An explicit retry is still conditional and cannot replace an established object. A mismatching object requires safe integrity failure/quarantine policy, not overwrite. An unavailable read leaves uncertainty. No settlement, failure transition or destructive cleanup is automated here.

## Executable qualification

Task-owned PostgreSQL 18: `127.0.0.1:5549/test_m1_s212`, synthetic role `m1_s212`, bounded test pools. Task-owned Redis from the already installed image: `127.0.0.1:6389`, database 15. Temporary local storage and all protocol HTTP listeners are task-owned and loopback-only. No backend HTTP/OCR worker or external object store was started. Storage selectors were explicitly `APP_ENV=development`, `STORAGE_MODE=local`; ambient R2 and Paddle credential variables were unset for test commands. No dotenv/startup path was invoked. The preserved S2-07 PostgreSQL directory was neither inspected nor reused.

Evidence types:

- **Actual local storage:** first creation, conflicts, original bytes retained, 0600 files, root/symlink confinement, staged-file invisibility, partial-copy failure, file/directory sync failures, unsupported publication, cancellation and bounded crash debris.
- **Kernel/process concurrency:** twelve goroutines compete for one key; two separate Go test processes submit different PDF bytes to the same key; exactly one wins. A separately killed process leaves no final file and one bounded staging slot.
- **Actual SDK, simulated provider:** same-content and different-content conditional writers, signed precondition, 412/409/403/501/503, interrupted/lost replies, GCM nonce changes, explicit retry after uncertainty and readback, cancellation, and negative GET during in-flight PUT.
- **Actual PDF inspection:** synthetic gofpdf files; readback verifies logical SHA and bytes after remote GCM. `qpdf --check`, `pdfinfo` and `pdftotext` inspect the exact decrypted persisted snapshot (one page and synthetic marker). Existing two-page local evidence checks inspect the exact final object. pdfcpu/Ghostscript evidence tests remain intact.
- **Actual PostgreSQL:** captured intent/claim binding, current result reaches ready, cancellation after upload begins rejects later readiness with reservation released and no charge, lease expiry rejects evidence, unknown write plus matching readback reaches ready without charging, unavailable reconciliation read stays processing/reserved. Independent observer assertions include descriptor, revision, reservation, all three usage counters, credits and log count. Existing joint settlement/concurrency tests remain unchanged.

Commands run under the isolated environment:

```sh
go test -json -p 1 ./internal/storage ./internal/ocr -run 'TestArtifactExclusive' -count=1 -timeout=3m
go test -json -p 1 ./internal/billing/... ./internal/tasks/... ./internal/ocr/... ./internal/storage/... -count=1 -timeout=5m
go test -race -json -p 1 ./internal/billing/... ./internal/tasks/... ./internal/ocr/... ./internal/storage/... -count=1 -timeout=6m
go vet ./internal/billing/... ./internal/tasks/... ./internal/ocr/... ./internal/storage/...
git diff --check
```

Final focused exclusive-create run: **15 top-level passes, 44 passes including subtests, one subprocess-helper skip**. Final normal and race regression runs each:

| Package | Top-level passes | Passes including subtests | Skips |
| --- | ---: | ---: | ---: |
| Billing | 55 | 77 | 0 |
| Tasks | 29 | 45 | 0 |
| OCR | 40 | 247 | 0 |
| Storage | 29 | 54 | 1 helper |
| **Total** | **153** | **423** | **1** |

The helper deliberately skips when encountered in the parent suite; it is invoked successfully by both process tests. No PostgreSQL qualification cases skipped. The held-upload lease-expiry case was also separately executed under the race detector against disposable PostgreSQL (five passes including its parent/subtests), advancing the clock before the upload resumed. Vet exits zero; targeted gofmt emits no paths and diff checks are clean. Independent tools: qpdf 12.3.2, pdfinfo 26.05.0 and Ghostscript 10.06.0. GitHub CI is separate: the existing workflow is restricted to `master`/`main` push/PR targets, compiles/builds containers, and does not run these PostgreSQL/storage tests. No such CI coverage is implied for this stacked PR.

 Earlier development attempts included an unused-variable build failure, a deliberately interrupted stuck protocol fixture after a failed signature assertion, and fixture read-limit/observer-assertion failures. These were test-harness defects corrected before the final runs; they are not counted as passing qualification.

## Runtime inactivity and remaining gates

Production-source search finds the new storage call only inside the private dormant `selectedArtifactStore`; `newArtifactVerifier` and `captureArtifactInTx` have no production invocation. No HTTP route, startup registration or feature flag activates the new path. The actual lifecycle repository/model/authority, billing, generic tasks, Lua, DTOs, live `r2_jobs.go`, migration registry, source processing and legacy output cleanup are unchanged. Frontend and worker repositories are untouched. The earlier metadata-only SQL fixture helper remains test-only; new storage/readiness cases use actual PDFs.

Exclusive creation prevents replacement only through this conforming writer. It does not protect against legacy privileged writers, a credential holder, administrator, deletion, storage loss or retention expiry. Activation requires a dedicated output storage permission boundary or equivalent reviewed service isolation: only the qualified writer may create outputs, legacy/presigned writers must be unable to modify this prefix, and deletion must be separately authorized against committed ownership and retention. A prefix alone cannot constrain a broad credential. If the provider cannot restrict overwrite separately from create, a dedicated write service/credential boundary and audited application restriction are required. Physical versioning/object lock is not assumed.

Still required: real provider qualification, protected cleanup/retention, authoritative download/availability checks, coordinator and authenticated identity adapter, lease renewal/recovery, Redis projection, free-mode journal, reconciler/orphan discovery, reviewed managed migration, guest/deadline/account-deletion policies, native-parser resource qualification and mixed-version rollout. Local multi-instance recovery requires shared durable access. No automatic task settlement or deletion is added.

Independent preview-owner PR #11 and worker PR #1 retain their coordinated release dependency; Studio snapshot PR #12 is separate. This dormant OCR stack does not merge or deploy any of them. Existing F-01/F-02 qualification remains bounded.

## Resource and publication safety

Disk availability was approximately 10 GiB before work and 10,598,772,736 bytes (about 9.9 GiB) after qualification. Task-owned disposable PostgreSQL/Redis processes were stopped and their newly created data removed after checking exact PID/data path, port, container ID and ownership label. Ports 5549 and 6389 are free. Sanitized local evidence remains outside Git; no generated PDF, credential, request capture or HAR is staged. The earlier preserved PostgreSQL directory was not touched.

The frontend's existing `AGENTS.md` diff checksum remains `4394cdfddf55a605d9bfbd7d0d1c84aeb0c5dd434cea18c7bd6496c8bfdc1f03`. Outer dirty gitlinks, documentation, staging material and scratch files are preserved. No frontend/worker commits, additional clone/worktree, live backend service, production call, merge or deployment occurred. A final fixture-only signature check uses the transmitted timestamp and SDK HMAC helper, avoiding dependence on the wall-clock second of a second signing call; the updated protocol cases pass normally and under the race detector.
