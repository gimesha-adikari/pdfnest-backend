# Studio PDF source admission

Studio input PDFs have one authoritative inclusive limit: `studiosource.MaxPDFBytes`
(52,428,800 bytes / 50 MiB). `MAX_STUDIO_UPLOAD_BYTES` no longer overrides it.
The limit applies to new initial/secondary PDF sources and `studio_source`
presign declarations. Exports, watermark/signature images, other storage purposes,
Fiber's 100 MiB ingress and Worker's 128 MiB authentication envelope are unchanged.
Existing sessions are not migrated or revalidated by this change.

## Admission boundaries

- Multipart: inspect the actual staged file; validate PDF header, readability,
  encryption, pages and geometry before source publication and registration.
- Presign: validate declaration and generate a fresh owner/purpose-scoped key.
  A presign is not evidence of upload, provider-enforced size or PDF validity.
- JSON adoption: reject foreign/wrong-purpose keys and foreign VDM assets;
  acquire a private snapshot, validate actual PDF bytes, and derive source ID,
  file size, page count, geometry and metadata on the server. Client document
  metadata cannot substitute for these checks. Source-less blank creation remains.

Stored acquisition uses at most 50 MiB plus one detection byte, a 32 KiB copy
buffer, four concurrent snapshot slots and a two-minute storage deadline. Remote
HEAD rejects early; conditional GET (`If-Match`) and actual bounded reading
establish the snapshot. Missing objects, changed ETags/lengths and storage failures
fail closed. Local acquisition rejects non-regular files and root escapes and
checks the opened object against its path after copying. Snapshots are private
0600 files and are removed when admission finishes or acquisition fails.

The validated snapshot is published to a fresh server-generated
`studio/sources/` key before the document transaction. The user presign path can
only generate `owned/.../<purpose>/` keys; its reusable PUT URL cannot overwrite
the adopted source. Secondary PDFs use fresh `studio/merge-sources/` keys.
This is application-level separation, not an assertion about provider WORM,
privileged administrators, deployed bucket policy or physical deletion.
Remote publication preserves existing encryption with a bounded plaintext read;
it is not a streaming-encryption rewrite or a process-wide memory quota.

Source failures use `STUDIO_SOURCE_*` codes: too large 413, missing 404,
invalid/encrypted/page limit 400, changed 409, storage/registration unavailable
503. Unauthorized input retains the existing 403 contract. Preview-only
`PREVIEW_SOURCE_TOO_LARGE` is unchanged. Public source errors exclude provider,
database and filesystem details.

Temporary snapshots and multipart staging are removed. Registration failures
attempt cleanup only for the newly generated private copy. The original owned
upload is retained. Provider deletion failure/uncertain outcomes remain subject
to existing cleanup limitations; no synchronous physical-erasure guarantee is
introduced. PDF inspection retains the existing in-process pdfcpu validator.

The frontend's 50 MiB admission/copy alignment is a separate Phase 2 task.
F-15 remains partially resolved/open; this change does not activate Worker PR #6
or qualify production sender versions, deployment ordering or rollback.
