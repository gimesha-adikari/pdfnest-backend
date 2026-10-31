# Backend durability integration — 2026-10-29

Review candidate only. No merge, production migration, deployment or image promotion is authorized by this record.

## Revision ancestry

Base: backend master `40dfe66886e7c7c5bc3f522e4ca5fcd55e669a02`.
The integration retains both original commit histories through merge commits:

- F-07 PR #39: `d6144eb96b4cf7aa5c740a18de12fd58f2d9fa20`.
- F-18 PR #40: `506c9ba4d7f2d73c38008be3a4339a78f1db104f`.
- Inherited combined baseline: `febb769794056ff560031ff5c1d4a1a9b3bdd718`.
- Worker compatibility source: PR #11 `8595d73f8c39865f35acd6259b91ed897fc07f7c`, which incorporates the F-11 candidate; standalone PR #10 remains `29a4011253d68c4e0a4e6c89580f03cb4cea1265`.

Original PRs remain separate drafts. This document supplements evidence; it does not replace the original audit or historical records.

## Application guarantees and limits

Studio admission locks the session and persists the job and planned object keys before writes or external dispatch. Expiry uses the same lock. Active/uncertain jobs and successful jobs awaiting publication retain their graph and object references. Duplicate admission returns existing durable state. Unknown dispatch acknowledgements never trigger blind resubmission.

Worker ownership is application-owned PostgreSQL state. Successful terminal snapshots retain exact result and metadata keys beyond Redis expiry; downloads can use retained storage references. Failed/cancelled snapshots are not proof that an actor retry has stopped and remain held. Deletion uses retryable state, shared key locks and immutable key-retirement tombstones; new references cannot reuse retired keys. Shared active references and unresolved settlements prevent deletion.

Backend artifact publication records ownership and artifact fingerprint before its storage write. Artifact availability precedes settlement. Paid reservation commitment and durable completion share one SQL transaction. Guest acknowledgement ambiguity requires a positive Redis outcome receipt; missing state is not charge proof. Redis completion is a recoverable projection. Cancellation and release serialize against publication. Free mode remains supported; subscription, credit, quota and Paddle features are retained.

These are application-level guarantees. External provider deletion, backup/version retention, storage outage behavior, datastore durability settings and full native-runtime enforcement are separate qualification gates.

Known holds requiring explicit reconciliation:

- Historically uncertain Studio admissions without a known Worker identity.
- Lost random Worker job acknowledgements, or terminal results never observed before Worker Redis metadata expires. The backend cannot reconstruct an unknown output key from time alone.
- Failed/cancelled Worker attempts without authoritative producer quiescence.
- Publishing rows with missing artifacts or irretrievably missing guest outcome receipts.
- Legacy uploaded OCR inputs have no demonstrated exclusive session owner; their unsafe unconditional deletion was removed. Full upload reclamation needs durable upload ownership/reference authority.

## Verification and reproducibility

`scripts/test-backend-durability.sh` creates private Unix-socket PostgreSQL and Redis fixtures, then runs migration, Studio lifecycle, billing, tasks, OCR, Worker, conversion and acquisition/storage regressions under the Go race detector. It never uses the caller's databases or development services. CI fetches the exact public Worker commit above, installs its frozen dependency lock and exercises old/strict signed JSON and multipart receivers through the backend transport.

Draft migration tests cover upgrade, idempotent reapplication, atomic failure, refusal of unversioned adoption, read-only startup validation and refusal to roll back retained ownership. No production migration was applied.

The inherited broad baseline is not fully green: Studio rendering benchmarks/stress routes and native rendering/metadata fixture dependencies fail outside their required Worker environment. These are recorded separately from the focused lifecycle gates; assertions were not weakened. Baseline backend master also initially hit the existing one-second Analyzer HTTP-test timeout; its unchanged isolated rerun passed. Exact final revision, local results and GitHub CI/build URL are recorded in the integration PR and execution report.

## Release checklist and rollback

1. Independently review the combined PR and confirm its base/head and preserved original ancestry. Merge requires separate owner authorization. Original #39/#40 are not merged by this work.
2. Approve and review a database backup and explicit draft `config/migrations/20261029_backend_durability.up.sql`; apply only under separate migration authorization. Managed startup validates the separate durability marker and required authority constraints read-only. The existing migrate command does not apply this draft. For an empty managed database, initialize the historical schema using its approved baseline before the extension; do not adopt unversioned tables created by a newer local AutoMigrate run.
3. Stop/drain old backend admissions, in-flight producers and old reservation janitors before enabling the new lifecycle. Reconcile existing ambiguous jobs. Do not permit old replicas to delete or release new durable ownership.
4. Start with `STUDIO_EXPIRED_SESSION_RECLAMATION_ENABLED=false`. Enable it only after the complete old-replica/pre-admission drain is independently verified. Preserve object tombstones permanently and keep PostgreSQL ownership tables and guest outcome receipts during rollback.
5. Verify shared backend/Worker object storage and key mappings, active sender/image versions, installed SDK, retention settings, capacity, and storage/Redis persistence configuration in an authorized canary. F-15 strict production activation remains NO-GO until sender, capacity and rollback gates pass.
6. On rollback, stop admissions and reconcilers. Retain the schema, ownership rows, immutable tombstones and guest receipts. An old binary is unsafe while unresolved publishing rows exist because its janitor lacks the durable release guard; drain/reconcile first or retain the corrected guard. The down migration is only an empty-state draft and refuses retained ownership; never erase records to make downgrade succeed.
7. Obtain distinct approval for canary/deployment, any live migration/financial reconciliation, and provider-account inspection. No such operation was performed here.

F-07 hard allocated-byte/inode quota enforcement remains BLOCKED pending an independently provisioned and qualified kernel mechanism; monitored scanning is not that proof. F-11 native aggregate enforcement, detached-descendant cleanup and independent controller authority remain BLOCKED pending real runtime qualification. F-18 provider physical erasure remains UNVERIFIED. This candidate does not declare those findings fully resolved.

## Final hardening addendum — M1-S2-07-01

The existence-only settlement check was reproduced on the earlier integration head: incorrect, truncated, empty and same-length altered objects all committed a paid reservation. Settlement now reads canonical stored application bytes and verifies the immutable SHA-256 and length. It never treats an ETag or existence as content proof. Source staging captures one private generated-output snapshot before fingerprinting; retries cannot substitute a different source or overwrite a settled object. Mismatches preserve publishing ownership and reservation state.

Plaintext verification uses streaming buffers and reads at most the recorded length plus one byte. Source staging is bounded by the opened source's initial length plus one byte. Storage operations have a 30-second deadline. The existing AES-GCM envelope requires contiguous buffers: encrypted verification/upload serializes its live buffers and has a 250 MiB plaintext budget (up to two such buffers). This budget is not a plaintext output ceiling. Larger encrypted artifacts remain explicitly pending and need a separately qualified bounded encryption/verification path; do not treat their existence as permission to charge.

If a chosen task object is missing, reconciliation can recover it from an already retained successful Markdown Worker result only when task ID, owner, terminal status, submit path, canonical namespace and the recorded content fingerprint all match. It verifies the copied task object again before settlement. This introduces no raw-input retention or content stored in PostgreSQL. Native OCR, HTML and Markdown-to-PDF outputs whose only process-local source disappeared still require exact-byte restoration or regeneration from independently retained inputs. Do not release their publishing hold or overwrite an unverified object to force completion.

A comparable nine-package race run with identical native dependencies, private PostgreSQL/Redis, benchmark fixtures and frozen Worker source produced 516 passing test events on original master and 640 on the corrected candidate, with the same 21 Studio failure events and zero skips on both. The 21 are ten rendering benchmark events (including seven concurrency subtests), seven rendering stress events, one missing encrypted-PDF fixture and three metadata-Worker-dependent overlay/materialization tests. There were no candidate-only failures. Supplying the existing encrypted-PDF fixture made that test pass unchanged on both trees. The remaining rendering fixtures use obsolete caller-supplied IDs or invalid placeholder PDFs; the three metadata cases still require their isolated native Worker. The earlier billing/receiver skips were exercised with their explicit disposable fixtures. These results do not make the broad Studio suite green or qualify production runtime behavior.
