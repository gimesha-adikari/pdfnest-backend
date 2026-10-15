# M1-S2-14 — Private OCR artifact namespace guard

Base: `bf471963ac83bc4da302b2f0de8ab37117e84e6a` (draft PR #19). Dormant namespace ownership only; no protocol activation, deletion coordinator or retention system.

## Evidence and policy

The pre-fix regression created a synthetic PDF through exclusive creation, then confirmed that generic `SaveObjectBytes` could replace it and `DeleteLocalObject` could remove it. Both calls returned nil; the new regression failed as expected against the parent.

The reserved namespace is the exact case-sensitive component pair `ocr-finalization/outputs`, including that namespace key itself and every descendant. A shared helper follows the existing sanitizer's trim, slash/backslash, dot-segment and root-clamping rules. It rejects entry into the private namespace even when later `..` segments leave it. Component comparisons preserve siblings such as `outputs-other`, `outputs2`, `Outputs` and unrelated parent prefixes. Keys are object keys, not URLs; no new URL-decoding behavior is introduced.

Rejection uses bounded sentinel `ErrPrivateOCRNamespace`, with no key, source path or provider body. Coverage:

- `SaveLocalStream` guards writes before temporary output or input consumption; `SaveLocalFile` delegates to it.
- `SaveObjectFile` / `SaveObjectBytes` inspect the original key before their existing normalization and selector, preventing aliases from being erased before a leaf guard sees them.
- `DeleteLocalObject` and `DeleteObject` reject before local deletion, selector initialization or remote SDK work.
- `Store.UploadFile` / `UploadBytes` reject before source reading, encryption and PUT.
- `Store.DeleteObject` rejects before SDK deletion.
- `Store.PresignPutObject` rejects before signing. Bulk `PresignUploads` preflights its prefix and every explicit key before generating any capability, including mixed batches.

No check is added to authorized `CreateArtifactIfAbsent` or strict `ReadArtifact`. Their direct exclusive SDK/filesystem operations remain unchanged; they do not delegate to generic writers.

## Runtime and bypass boundary

The bounded prefix/SDK-operation search and the M1-S2-12 caller inventory found no legitimate current runtime generic consumer of this namespace. Its current producer is the dormant private intent factory; existing live OCR output uses a different namespace. No application route is activated.

The MinIO mutation calls in the inspected backend are the now-guarded generic methods and the authorized exclusive writer. The client field is package-private, but trusted storage code can still bypass method guards. Independent workers, administrators, broad credentials, raw MinIO/S3 access, provider deletion and already-issued presigned URLs are not controlled by this application check. The public presign flow currently generates owner-scoped upload keys; no claim is made that historical capabilities have been revoked.

Direct filesystem access/native code can replace or delete files independently. Existing legacy local APIs can also follow pre-existing filesystem aliases; an actor able to provision symlink/hard-link aliases or relocate storage must remain outside the untrusted boundary. `ResolveObject` can give trusted callers a persistent local path; strict artifact readback instead returns a temporary copy. Namespace checks do not constitute filesystem sandboxing, IAM isolation, permanent immutability or authenticated-download authority.

Old corruption/replacement tests now inject physical changes explicitly through test-only filesystem code rather than expecting a generic private write to succeed. The lost-write-ack fixture uses exclusive creation. The generic overwrite/read-snapshot compatibility fixture now uses an ordinary legacy key. No verification assertions were removed.

## Focused verification

Explicit development/local selection; ambient R2/Paddle credential variables unset. Synthetic PDFs and task-owned temporary directories only; remote requests use the existing loopback MinIO protocol fixture, not R2.

```sh
go test -json ./internal/storage -count=1 -timeout=3m
go test -race -json ./internal/storage -count=1 -timeout=3m
go test -json ./internal/ocr -run '^TestArtifact(Evidence(LocalPDF|InvalidBytes|KeyPolicy|UncertainWritesAndReplacement|ReadBoundaryFailures|LargeAllowedPDF)|ExclusiveIntentBinding)$' -count=1 -timeout=3m
go vet ./internal/storage ./internal/ocr
git diff --check
```

Storage normal and race runs each: **36 top-level passes / 62 including subtests**, one intentional subprocess-helper skip. That helper is invoked by the existing process tests. Selected OCR evidence: **7 top-level passes / 49 including subtests**, zero skips. No final failures or race findings; targeted gofmt, vet and diff checks pass. The initial pre-fix failure is characterization, not a passing safety result.

The new matrix tests 13 namespace/path variants across ten generic interfaces, mixed-batch presigning, selector wrappers in remote mode and zero remote requests on rejection. Five ordinary/sibling keys retain local write/overwrite/delete, remote write/delete and presign behavior; permitted operations issue the expected requests. Original artifact bytes remain intact after rejected operations, strict readback succeeds, and a second exclusive creation conflicts. Existing local-root, concurrent/process writer, conditional PUT and PDF-validation regressions remain passing. No PostgreSQL, Redis, browser, worker or backend-wide matrix was needed or run.

## Preservation and residual risk

No live OCR, billing, SQL lifecycle, Redis, temporary cleanup, managed migration, identity, guest/free mode, frontend, worker or encryption logic changes. **The live M1-S2-07 charged-without-artifact problem remains unresolved.** Provider qualification, protected cleanup/retention, recovery, authoritative downloads and rollout remain separate gates.

No production infrastructure or data was accessed. Test fixtures/listeners are cleaned by the harness; no persistent services were started. Disk remains approximately 9.9 GiB free. Frontend `AGENTS.md`, outer dirty gitlinks/documentation/staging/scratch work and the protected PostgreSQL directory are preserved. This task ends at publication and independent review; it does not update the permanent Antigravity handoff.
