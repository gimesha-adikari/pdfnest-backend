# M1-S2-11 — Dormant OCR artifact storage evidence

## Scope and outcome

Development-only foundation based directly on lifecycle commit `ef3146d85052c582ef76b2d6926dad0586b119de` / draft PR #16. This stage independently qualifies **point-in-time logical PDF readback**, not the complete OCR finalization protocol, immutable storage or production durability.

The existing live `/api/ocr/jobs` path, billing implementation, task registry/Lua, public authorization/download routes, worker processing, storage selector semantics and managed migrations remain unchanged. No coordinator, endpoint, protocol prefix, startup registration, Redis projection, free-mode journal, retention default or cleanup service is activated. The known M1-S2-07 charged-without-artifact defect remains unresolved.

## Source contracts reviewed

Reviewed `internal/ocr/finalization_model.go`, repository, authority and foundation/lifecycle tests; live `r2_jobs.go` and generated-output checks; `internal/storage/storage.go`, `r2.go`, ownership, selector and encryption tests; `internal/uploads/validation.go`; pdfcpu's local dependency implementation; the S2-10 report; and all three accepted S2-08 architecture/matrix/rollout documents. Graph tools were unavailable; call-site evidence uses bounded source searches.

- `RemoteStorageEnabled` is mandatory in managed environments and opt-in in development. Local storage must be explicitly selected for isolated tests, independent of stale R2 environment values.
- `SaveLocalStream` stages, hashes, syncs the file and renames it. It can replace an existing target and does not fsync its parent directory. Random keys do not prove physical immutability or power-loss durability.
- `SaveObjectFile` uses that selector. Remote `UploadFile` encrypts using the existing optional AES-GCM configuration and uploads with no conditional write. It buffers the source/encrypted message and uses a background upload context. This stage does not change those legacy behaviors.
- Generic `ResolveObject` permits development raw-path fallbacks and can collapse remote download/config/decryption failures into `ErrObjectNotFound`. It is **not used** for this evidence boundary. Existing consumers keep their behavior.
- Existing `DownloadToTemp` decrypts logical bytes, but has no caller byte limit and does not preserve the required error distinctions. The new strict read is additive, uses the same selector/client/GCM functions, and does not redirect existing callers.
- Live OCR uses `uploads.CheckPDFPageLimit`, whose pdfcpu page-count parse is stronger than a header but is not independent persisted-output verification. The new adapter does not alter it.

## Private output-key and descriptor contract

`newArtifactIntent` accepts trusted logical facts, never a caller-chosen artifact ID/key. It generates a fresh UUIDv4 artifact ID and the canonical key:

```text
ocr-finalization/outputs/<SHA256(JSON(domain, owner, task, processor-claim))>/<UUIDv4>.pdf
```

The domain is `platen/ocr/output/v1`; JSON array framing avoids ambiguous concatenation. No raw owner/task/claim is exposed in the key. The claim is already a private random server-generated capability in the dormant lifecycle. Each new destination uses a new unpredictable UUID, even for identical content and the same claim.

`artifactKeyMatches` requires the exact derived prefix, canonical lowercase UUIDv4 and suffix. `recordIntentInTx`, capture and verification enforce it. Another task, owner, processing attempt, uppercase/alternate representation, source-upload prefix, absolute path, traversal, whitespace or client-provided destination cannot substitute. Source-upload routes do not issue this output namespace.

The immutable intent records artifact ID/key, **logical PDF** SHA-256 and size, page count, PDF MIME and validation version. Verification time is initially zero. Ownership, task, reservation, claim and revision are captured from a locked current processing row; they are not reconstructed from filenames or client metadata.

## Persistence and strict storage readback

The private `selectedArtifactStore` reuses `storage.SaveObjectFile`. `artifactVerifier.persist` requires a valid captured intent, a live claim according to its trusted clock and a regular bounded source file. A failed write returns a safe **uncertain write** error; it does not infer whether PUT committed, delete anything or retry the write. Even a known-before-write test fault is conservatively classified as uncertain at this boundary.

New `storage.ReadArtifact` / `Store.ReadArtifact` provide the separate strict read contract:

1. Require a canonical key and explicit positive byte bound (at most 250 MiB, matching the existing upload ceiling; no active output-size policy/default is installed).
2. Local reads use Go's rooted filesystem API, confined to the configured local storage root, without raw-path fallbacks. Symlink escape is rejected.
3. Copy from the exact object into a private mode-0600 temporary snapshot with bounded streaming. The persistent local object is never handed to the verifier as a mutable parse target.
4. Remote reads use the actual MinIO client and caller context. Wire reads are bounded by the logical budget plus the current 12-byte nonce/16-byte GCM tag. The existing production decryption function is used before logical verification.
5. Return a cleanup function that removes only that snapshot and reports unlink failure. Read/copy/decryption failures also attempt removal of their own temporary snapshot.

Local readback streams to disk without another full-file byte buffer. Existing whole-message AES-GCM requires bounded ciphertext and plaintext buffers in remote readback; this limitation is explicit. Local file sync/rename behavior, object generation/version support, remote conditional writes and legacy upload cancellation are not strengthened by this stage.

## Independent verification and private readiness evidence

`captureArtifactInTx` locks/checks the current processing task, exact owner/reservation, expected revision, live claim and immutable intent, then returns a value snapshot. Storage and PDF work occur **outside SQL locks**.

`artifactVerifier.verify`:

- Rechecks snapshot chronology/lease and canonical task-owned key.
- Independently resolves the intended stored object through the strict adapter.
- Streams logical bytes into SHA-256; requires exact positive byte count and digest equality.
- Uses the existing pdfcpu dependency in strict mode to validate PDF structure, rejects encrypted output under the ordinary generated-PDF contract, and compares actual positive page count with intent and the existing `MAX_PAGES_OCR` limit (default 150).
- Runs bounded Ghostscript null-page inspection with `-dSAFER`, `-dPDFSTOPONERROR`, `-dPDFSTOPONWARNING` and `-sDEVICE=nullpage` to detect stream-decoding failures not rejected by pdfcpu alone. Ghostscript already exists in the backend Dockerfile; no image/dependency configuration changes were needed. Output is discarded, no rendered artifact is produced, and no command diagnostics/path/PDF content becomes an error message.
- Checks context and lease again at completion. The inspection budget is an explicit constructor argument, not a production timeout setting; tests use five seconds.
- Issues private `artifactEvidence` only after successful verification and temporary-file cleanup. Cleanup/close failure returns zero evidence.

The private evidence binds task, owner, processor claim, task revision, every descriptor field and verification time. `readyInTx` now requires it as a separate argument and checks it against the locked row/request before its existing state, revision, claim, chronology and descriptor checks. Metadata alone or zero evidence cannot mark ready. Cancellation or expiry winning before readiness prevents readiness and charging. No HTTP client can construct these unexported fields or interfaces; trusted code in package `ocr` remains part of the trust boundary.

The old foundation billing/lifecycle suites retain their metadata-only characterizations through an explicitly named **test-only** `metadataOnlyReadiness` helper in `_test.go`. It is not compiled into runtime source and does not count as physical artifact qualification. New artifact tests use actual persisted PDFs and verifier-issued evidence. No production constructor converts arbitrary descriptor metadata directly into evidence.

Structural parsing/null-page inspection establishes readable output and association with intended bytes; it does not prove OCR text accuracy, rendering equivalence, source authenticity, signatures, encryption strength or all malicious-PDF safety. Byte/page bounds and the Ghostscript deadline do not establish a hard memory/CPU sandbox for in-process pdfcpu parsing. Native-tool version/resource qualification remains an activation gate.

## Error classification and ambiguous outcomes

Errors expose bounded categories, not keys, paths, response bodies or credentials.

| Condition | New boundary result | Consequence |
| --- | --- | --- |
| Local ENOENT; remote explicit `NoSuchKey`/`NoSuchObject` with HTTP 404 | `missing` | Confirmed absence of the selected key at read time; no automatic deletion/terminal transition |
| Malformed/noncanonical key | `invalid_key` | No read |
| Permission/authentication denial (401/403) | `denied` | No evidence; do not infer absence |
| Network failure, timeout, generic 404/absent bucket, 503, partial transport read, unknown failure | `uncertain_or_unavailable` | Retain intent/output; recoverable observation, never cleanup authority |
| GCM authentication/decryption failure | `integrity` | No logical evidence; not a missing object |
| Read exceeds budget | `too_large` | Bounded read stops; no evidence |
| Temporary create/read/write/close/unlink failure | `temporary_file` or private temporary error | No evidence; only own temporary cleanup attempted |
| Digest/size/page/format mismatch, malformed/encrypted PDF, corrupt stream | Private evidence rejection | No readiness or successful artifact publication |
| Missing/timed-out native inspector | Private inspection-unavailable error | No evidence; output retained |
| PUT fails or its acknowledgment is lost | Private write-uncertain error | No inference about persistent presence; readback may resolve the outcome |

Provider retention locking concerns deletion. This adapter exposes no DELETE; a simulated retention-related read error is uncertain/denied rather than confirmed missing. Existing `RetentionLockedError` and legacy consumers are unchanged.

The existing decryption code's recognized plaintext compatibility remains. Unknown ciphertext/authentication failures are rejected; logical hash and PDF checks are still required even if bytes have a recognized plaintext prefix. Key rotation and real provider behavior are not newly qualified.

## Replacement, cancellation and cleanup limitations

Tests demonstrate that actual local storage overwrites an existing object. A later verification after replacement rejects different bytes; an earlier evidence value still describes the earlier snapshot. Neither random naming nor repeated reads makes a mutable object immutable.

**Activation blocker:** before live use, enforce exclusive write ownership/fencing for the entire artifact lifetime or a reviewed conditional-write/immutable-version contract, plus owned cleanup protection. No S3 version ID, ETag-based immutability or conditional PUT guarantee is assumed here. External replacement/removal after verification cannot be prevented by this foundation, and evidence must not be presented as that guarantee. A future publication/download boundary must enforce approved freshness and availability checks.

A captured intent can outlive cancellation while a PUT/readback is in flight. Late objects are retained; the task lock rejects readiness after cancellation. The adapter never releases billing, changes task state, marks failure automatically or deletes persistent output on these observations. Durable orphan/retention cleanup is a separate stage.

Successful and failed verification normally remove their temporary snapshots. An injected real unlink-permission failure returns no evidence; the test restores permissions and removes only its own leftover. No temp janitor or automatic retry service is added. A future coordinator must bound retries/disk consumption and retain temporary cleanup obligations on repeated filesystem failures.

## Executable evidence and environment

Synthetic fixtures contain known markers/pages and per-fixture SHA-256. No customer document or password is used. Tests explicitly select development/local storage under `t.TempDir`; remote tests instantiate a MinIO client pointed only at a loopback `httptest` S3 simulation with synthetic credentials and fixed region. No production R2 endpoint, bucket, credentials, auth or billing service is used.

- Actual local persistence/readback: selector + `SaveObjectFile`/`SaveLocalStream` + rooted strict read; persistent bytes checked independently with qpdf **12.3.2**, pdfinfo/Poppler **26.05.0** and pdftotext. The two-page marker fixture passed syntax/stream checks, page count and text checks.
- Go/native validation: pdfcpu **0.12.1**, Ghostscript **10.06.0**. Actual malformed, truncated, non-PDF, empty, encrypted (including empty user password), wrong digest/size/page count, and compressed-stream corruption cases reject. A valid PDF larger than 1 MiB passes within the explicit eight-MiB test budget.
- Remote simulation: actual MinIO HTTP GET + production GCM encrypt/decrypt functions, real synthetic PDF parsing, nonce/ciphertext changes with unchanged logical hash; missing key versus missing bucket, denied/outage/timeout, partial body, disconnected transport and corrupt ciphertext. This is **not real R2/S3 service or remote upload integration**.
- Controlled write faults: a test adapter fails before applying a write or delegates to actual local persistence and then loses acknowledgment. Subsequent actual readback distinguishes absent versus valid persisted output. No deletion/rewrite is triggered.
- PostgreSQL **18.6**, loopback `127.0.0.1:5549`, fresh UTF-8 `test_m1_s211`, synthetic role/accounts/reservations and bounded observer pools. SQL tests cover real evidence to ready/success, zero evidence, wrong task/descriptor, cancellation before/after readback, lease expiry during readback and readiness-update rollback. Durable billing/task assertions use independent observer connections.
- Task-owned Redis 7 Alpine on `127.0.0.1:6389` for unchanged billing/task/known-bad qualification. Existing default Redis was untouched. No frontend, Go HTTP server or OCR worker was started.

### Intermediate failures, accurately retained

1. Initial remote-positive simulation failed because its HTTP response omitted the S3-required Last-Modified header. The fixture was corrected; production classification properly failed closed.
2. The initial lease-expiry fault changed an outer verifier value rather than the value receiver's captured clock. Its expected failure did not occur. A shared explicit test clock fixed the injection; the final expiry case rejects evidence.
3. A new damaged-Flate-stream test **failed against pdfcpu-only validation**: it obtained evidence despite broken compressed content. Adding bounded native stream inspection made the regression pass. The live OCR validator was not changed.

These failed runs are not counted as passing evidence.

### Commands and final results

Test-only configuration:

```sh
export DATABASE_URL='postgres://m1_s211@127.0.0.1:5549/test_m1_s211?sslmode=disable'
export GIM6_TEST_DATABASE_URL="$DATABASE_URL"
export PLATEN_M1_BILLING_TEST_DATABASE_URL="$DATABASE_URL"
export REDIS_URL='redis://127.0.0.1:6389/0'
export PLATEN_M1_BILLING_TEST_REDIS_URL='redis://127.0.0.1:6389/15'
export BILLING_MODE=normal APP_ENV=development STORAGE_MODE=local

go test -p 1 ./internal/ocr ./internal/storage -run '^Test(Artifact|OCR(Lifecycle|Foundation))' -count=1 -json
go test -p 1 ./internal/billing/... ./internal/tasks/... ./internal/ocr/... ./internal/storage/... -count=1 -json
go test -race -p 1 ./internal/billing/... ./internal/tasks/... ./internal/ocr/... ./internal/storage/... -count=1 -json
go vet ./internal/billing/... ./internal/tasks/... ./internal/ocr/... ./internal/storage/...
gofmt -l <changed Go files>
git diff --check
```

| Final run | Top-level passed | Passed including subtests | Failed / skipped |
| --- | ---: | ---: | --- |
| Focused artifact + lifecycle + transaction foundation | 28 | 214 | 0 / 0 |
| Full billing (normal and race, each) | 55 | 77 | 0 / 0 |
| Full tasks (normal and race, each) | 29 | 45 | 0 / 0 |
| Full OCR (normal and race, each) | 38 | 233 | 0 / 0 |
| Full storage (normal and race, each) | 16 | 24 | 0 / 0 |
| Each full run total | 138 | 379 | 0 / 0 |

No race diagnostics. Vet, formatting and diff checks have zero diagnostics. PostgreSQL tests actually executed; none were counted through an environment skip. The unchanged S2-07 known-bad characterization still passes as characterization. No frontend/browser rerun was needed because no public contract changed.

## Changed files and runtime-inert proof

| File | Purpose |
| --- | --- |
| `internal/ocr/artifact_evidence.go` | Private key factory, locked capture, selector adapter, bounded independent verifier and private evidence |
| `internal/ocr/artifact_evidence_test.go` | Actual local PDF/readback, independent tools, faults, SQL integration and concurrent verification |
| `internal/storage/artifact_read.go` | Additive canonical rooted/remote logical-byte snapshot and typed error contract |
| `internal/storage/artifact_read_test.go` | Actual local replacement/root confinement; loopback MinIO/GCM/error simulations |
| `internal/ocr/finalization_repository.go` | Enforce private key policy at intent/readiness; require bound verifier evidence |
| `internal/ocr/finalization_repository_test.go` | Adapt existing SQL-only fixtures to the private evidence signature and key policy without weakening billing assertions |
| `internal/ocr/finalization_lifecycle_test.go` | Same signature/fixture adaptation; existing claim, cancellation and chronology assertions preserved |
| `internal/ocr/finalization_model.go` | Comment clarifying logical evidence only; no schema-field/tag changes |
| This report | Source contracts, qualified evidence, errors, limitations and activation gates |

All-Go call-site searches show no handler/startup caller for the factory/verifier/capture; `ReadArtifact` is called only by the dormant OCR adapter and the new storage wrapper. Exact parent diffs are empty for `internal/billing`, `internal/tasks`, live OCR/controller/characterization files, legacy storage implementations, `config` and `main.go`. Schema models/version, migration runner, feature flags and callbacks are unchanged. No database schema change was needed beyond test-only creation of the already dormant model in the new disposable database.

Frontend remains `0f6fa13361778917afbeeef19e977f7bd8002944` with its preserved unrelated unstaged AGENTS change; worker remains `09067d0a18985f791de27c142d8cc1a750bde598`. Outer gitlinks/docs/staging/scratch work and the preserved S2-07 PostgreSQL directory are not modified. Only verified task-owned disposable test resources are stopped/removed. Publishing the new stacked draft does not authorize merging or rollout; PR #16 and the earlier independent branches remain unchanged.

## Remaining gates

Before activation: enforce object write ownership/immutability and conditional/version identity where selected; qualify real remote PUT/GET, replacement, provider errors, at-rest key rotation, upload deadlines and local directory-fsync durability; approve proof freshness, resource/concurrency limits and strict-validator compatibility; build trusted identity/clock/processor adapters, coordinator, lease/recovery handling, Redis projection/download authority, free-mode journal, durable reconciliation/cleanup obligations, migrations, guest/retention policies, creation idempotency and safe mixed-version rollout/rollback. Preview-owner backend/worker PRs remain interdependent and unreleased; Studio snapshot work remains separate. This does not close broader F-01/F-02 coverage.

**One next recommendation:** review and specify enforceable private-output write ownership and version/conditional-write semantics before authorizing a coordinator that consumes this evidence. No such mechanism is implemented here.
