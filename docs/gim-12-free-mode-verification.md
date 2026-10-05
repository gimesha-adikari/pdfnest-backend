# GIM-12 local free-mode verification

**Run date:** 2026-10-06 (Asia/Colombo)
**Decision:** **BLOCKED** for staging/rollout.
**Candidate:** backend `72a01ad2b1546c9503d4a9883e2702687e6fe933`; frontend `45ec8017e80bb1fe66f13c6c2aeb86715b2cfe03`.

This is a verification record for the GIM-12 candidate, not a product change. No production source, environment, database, Paddle, or storage service was changed. The backend branch contains only this report beyond its required GIM-11 base. The frontend branch contains no GIM-12 source changes.

## Candidate and local environment

- Backend branch `gim-12-free-mode-verification` was created from GIM-11 commit `72a01ad2b1546c9503d4a9883e2702687e6fe933`. GIM-5 through GIM-11 are in its ancestry. The plan `docs/billing-free-mode-plan.md` was readable and its planning commit `99706196f8cae1c0552ab3da6d4330f19135b577` is an ancestor.
- Frontend branch `gim-12-free-mode-verification` was created from GIM-10 commit `45ec8017e80bb1fe66f13c6c2aeb86715b2cfe03`. GIM-8 frontend commit `ce517701aac204ec7798c01d1e5c50ac2f1451aa` is an ancestor. After fetch, `origin/main` remained `70db8e8a5a1466ddb154112ed1ddecee6e6cb57e`; the candidate was ahead with the GIM-8 and GIM-10 commits and was not rebased onto unrelated work.
- Runtime versions: Go 1.26.4; Node.js 24.11.1; npm 11.6.2; PostgreSQL 17.10; Redis 7.4.9; Python 3.12.13; FastAPI 0.139.2; Uvicorn 0.51.0.
- PostgreSQL ran in a disposable container on `127.0.0.1:55432`, with separate databases `test_gim12`, `test_gim12_full`, `test_gim12_server`, and `test_gim12_acceptance`. `max_connections=300` was set on this test-only server after an initial test attempt hit its default connection cap. The container and its test volume were removed after verification.
- Redis ran in a disposable container on `127.0.0.1:16379`. DB 0 served package tests and the worker; DB 1 served the live backend rehearsal; DB 2 served focused auth/billing tests. Each was isolated from the application Redis at `127.0.0.1:6379` DB 14. DB 14 was observed empty before/after the suite. The disposable Redis container and its test volume were removed after recording final key counts.
- The local backend listened on port 18080. The application binds `0.0.0.0` even though requests were sent via loopback; the process was stopped at the end. The frontend dev server listened on `127.0.0.1:3000` and was stopped. The actual local worker API listened on `127.0.0.1:8000`, used a task-local temporary storage directory and isolated Redis, and was stopped. Its health endpoint returned `{"status":"ok"}`.
- Paddle was never contacted externally. The live backend was pointed at a local fake URL; checkout tests used loopback `httptest` servers. R2/object storage and production credentials were not used for processing. The tests exercised local PostgreSQL, Redis, backend, frontend, and worker code; the worker preservation API was available locally.

## Configuration and session policy

`env -u BILLING_MODE go test ./config -count=1` passed. The configuration suite covers unset, explicit `normal`, explicit `free`, and invalid values. The application was also started in both normal and free mode. A development startup with `BILLING_MODE=invalid-value` exited nonzero before database initialization, reporting: `invalid BILLING_MODE "invalid-value": supported values are "normal" and "free"`.

Live guest `/api/auth/session` responses matched the backend policy:

| Process setting | HTTP | mode | processing unit limits enforced | purchases enabled |
|---|---:|---|---:|---:|
| `normal` | 200 | `normal` | `true` | `true` |
| `free` | 200 | `free` | `false` | `false` |

The authenticated database test `TestSessionAuthenticatedPolicyDoesNotRewriteStoredSubscription` passed against the isolated PostgreSQL database. It checks that a stored paid tier/status/credit balance remains truthful while effective mode is free. A paid account was not signed into the live HTTP/browser rehearsal, so the authenticated processing and paid-subscriber UI matrix is not fully end-to-end verified.

## Live guest synchronous transition rehearsal

A generated one-page PDF was sent to the real local `/api/structure/rotate` handler using one guest identity and the same request across backend restarts:

1. In `free`, the valid request returned HTTP 200 and a valid PDF. Before the normal request, `platen:guestquota:*` had no keys and `billing_reservations` had zero rows.
2. After restarting in `normal`, the same request returned HTTP 200 and a valid PDF. Redis recorded 2 used units with 0 pending units; the guest quota state key appeared. This demonstrates normal quota accounting on the allowed path.
3. The isolated guest hash was then set to the documented saturated test state (`used_3h=4`, `used_day=10`, `used_month=30`, pending values 0). The same request in `normal` returned HTTP 429 with `code=HOURLY_LIMIT_REACHED`, `requestedUnits=2`, and pending values remained 0.
4. After restarting in `free`, the same identity and same valid request returned HTTP 200 with a valid PDF. The saturated usage counters and zero pending values stayed unchanged. No `platen:guestquota:res:*` key appeared, and the database still had zero billing reservation rows.
5. For the final rollback, the backend was restarted in `normal` again. Session policy returned to normal and the same request returned HTTP 429 with the quota state unchanged.

This proves the guest synchronous billing-quota transition and rollback through the real local HTTP path. It does not verify an authenticated processing request, real async worker completion, or a real multi-replica deployment.

A malformed multipart/PDF attempt in free mode returned HTTP 400 (`MISSING_UPLOAD_FILE`). It was rejected, but this request did not establish the exact invalid-PDF or page-limit validator boundary; those remain separate verification gaps.

## Backend test evidence

The complete command `go test -p 1 -count=1 -timeout=300s ./...` passed with fresh isolated PostgreSQL, disposable Redis, and the local metadata worker configured. All packages completed, including `internal/studio`, `internal/studio/models`, `internal/studio/vdm`, `internal/billing`, `internal/auth`, `internal/tasks`, `internal/ocr`, `internal/conversion`, `internal/structure`, and `internal/analyzer/api`.

Focused acceptance runs also passed:

- `TestSessionAuthenticatedPolicyDoesNotRewriteStoredSubscription`.
- GIM-9 tests: free subscription and credit rejection, authentication/validation ordering, invalid-policy fail-closed behavior, no subscription-row creation, existing subscription/history preservation, normal subscription and credit checkout reaching fake Paddle, per-request policy refresh, portal availability, and webhook processing in free mode.
- The normal checkout fake received one transaction request for subscription and one for a credit pack. Free checkout tests assert rejection without a Paddle transaction request or subscription-row preparation.
- `TestCreatePortalSessionRemainsAvailableInFreeMode` passed with a fake portal response. `TestWebhookStillProcessesPreexistingPurchaseInFreeMode` passed with a signed test event. These tests verify the supported portal-session and webhook boundaries; they do not exercise a real Paddle account.
- `go test -v -count=1 -run '^TestPDFToMarkdown' ./internal/conversion` passed. It included both route aliases for guest/account single allocation, worker-submission failure, task-persistence failure, cancellation, stale release, free guest/account allocations remaining unbilled after switch to normal, idempotent replay, and validation/idempotency cleanup.
- The full suite includes guest/account async allocation, exact-once guest settlement, reservation-kind persistence, task stale/cancel/download authorization, finite duplicate entitlement, and free-mode empty-ID tests. Those are repository tests, not a live worker transition rehearsal.
- `internal/studio` and `internal/studio/models` passed with the metadata worker active. Worker logs showed successful `POST /api/v1/metadata/preserve` requests (HTTP 200); the endpoint’s health check returned 200. The full suite therefore closes the prior worker-unavailable package gap.
- `TestController_EndToEndRoutes` was run three times in isolation and passed all three runs in a combined 0.317 seconds. The test’s session-creation path validates the public GitHub hostname through `net.LookupIP`, so it remains external-DNS-sensitive. A prior one-second timeout was not reproduced; no timeout or product code was changed.

Two environment/test-data issues were resolved without product changes. An initial Studio attempt exceeded PostgreSQL’s default connection cap; it passed after using the isolated test server configured for 300 connections. A broad-suite attempt against a reused database observed five pending Studio rows where the test expected one; the complete suite passed against a fresh database. The first fresh full-suite run is the authoritative result.

## Frontend verification

- `npm run test:unit` passed: 100/100 test files. The suite includes free/normal/unknown billing policy, paid Pro remaining distinct from free mode, purchase-disabled refresh, unknown policy fail-closed behavior, quota-vs-resource error handling, and Paddle transaction bridge checks.
- `npx tsc --noEmit` passed.
- `npm run build` passed with local/dummy API and Paddle environment overrides. No production services were called.
- `tests/e2e/free-mode-ui.local.spec.ts` passed 8/8 mocked browser tests. These cover guest free UI, unknown policy, normal purchase affordances, `PURCHASES_DISABLED` refresh without retry, credits, and a paid Pro member retaining management controls in free mode. This is mocked browser evidence, not a full-stack paid-user test.
- A local browser smoke used system Chrome against the real local frontend/backend. In free mode, `/subscribe` showed the free-processing message, disabled a monthly purchase action, and did not show a fake Pro tier. In normal mode, `/subscribe` rendered pricing content and the live backend session reported `mode=normal` and purchases enabled. The normal purchase action was not clicked. No authenticated paid-user browser session was available.
- The Playwright project config uses the installed Chrome channel. A separate direct Playwright launch without an executable override failed because the expected bundled headless shell revision was absent; rerunning with `/usr/bin/google-chrome` worked. The 8-test configured suite passed.
- Targeted ESLint over files changed since `origin/main` exited nonzero with 20 errors and 14 warnings. The same changed-file set checked at the fetched `origin/main` baseline had 20 errors and 15 warnings. GIM-12 made no frontend source edits, so this is existing candidate lint debt rather than a GIM-12 source regression; lint is still not a passing gate and should be reconciled with the project’s release policy.

## Unverified release matrix and decision

The following GIM-12 items were not completed end-to-end and prevent a release-ready claim:

1. Authenticated synchronous processing with exhausted plan quota and zero credits in normal mode, the same request in free mode, and before/after stored account data snapshot.
2. Real guest and account async jobs completing, failing, cancelling, settling, and crossing both mode transitions through the running task/worker system. Unit/integration package tests pass, but no real async job was driven across process restarts.
3. Actual authenticated paid-Pro frontend settings/dashboard in free mode, management/cancel actions against a live account, and full normal UI restoration after rollback.
4. Live free-mode upload/page-limit, per-identity/global capacity, technical 429, task ownership/cancellation/download authorization, and storage authorization checks. Related automated package tests passed; the full real API/browser matrix was not executed.
5. A real pre-switch external checkout completed by webhook after switching modes. Fake-Paddle checkout and signed webhook tests passed separately, but the exact external transaction/webhook sequence was not run as one live rehearsal.
6. Two simultaneously running replicas with different modes. Each local process was not tested concurrently; each process derives policy from its own environment, so deployment must coordinate all replicas.
7. Full frontend Playwright coverage for authenticated paid users and normal rollback. The selected eight-test suite is mocked; the real local browser checks covered a guest only.

There were no new source defects reproduced during these checks. These are evidence gaps, not proof that the unrun paths are defective. The candidate remains **BLOCKED** until the critical account and async transition flows and safety/authorization checks are executed in a controlled integration environment, the frontend lint result is dispositioned, and the remaining acceptance evidence is reviewed.

## Rollout and rollback checklist

Before eventual rollout, verify in the intended environment that PostgreSQL, Redis, worker/API, object storage, webhook secrets, and Paddle configuration are healthy; set the same intended `BILLING_MODE` on every backend replica; and ensure the frontend reads policy only from `/api/auth/session`.

After rollout, check in order: guest and authenticated `GET /api/auth/session`; one supported guest and account processing operation; one async task through success and failure/cancel; pricing and subscribe pages; dashboard and paid-subscriber settings; free-mode checkout rejection or normal-mode fake/safe checkout; portal access; webhook signature/idempotency observability; both PDF-to-Markdown aliases; ownership/download/cancel rejection; resource and upload/page limits; and the billing state snapshot.

Rollback is to set `BILLING_MODE=normal` on every backend replica and restart/redeploy them using the environment’s established procedure. Then re-fetch session policy, verify normal guest/account quota enforcement and purchase checkout availability, confirm existing subscription/credit state is unchanged, and check that old real-ID reservations still settle according to their stored reservation metadata. No production rollback was run for GIM-12.

## Git and cleanup

- Backend diff base: `72a01ad2b1546c9503d4a9883e2702687e6fe933`.
- Frontend diff base: `45ec8017e80bb1fe66f13c6c2aeb86715b2cfe03`.
- `git diff --check` passed in both repositories. No product-source changes were made.
- Next dev generated a tracked `AGENTS.md` edit in the frontend; after stopping Next, that exact generated diff was restored. The frontend checkout is clean at its candidate base.
- Disposable backend/worker/frontend processes, PostgreSQL/Redis containers and their anonymous volumes were stopped/removed. Redis DB 14 on the separate shared local Redis remained at zero keys. All task-local `/tmp/gim12-*` artifacts were removed after recording the results here.
- No push, PR, merge, production environment change, or deployment occurred.

## Continuation verification — 2026-10-06

This addendum preserves the earlier **BLOCKED** record above and records the remaining local integration evidence gathered after it. It does not rewrite the earlier evidence or claim production acceptance. No product source was changed.

### Updated blocker disposition

| Gate | Previous state | New evidence | State after this run |
|---|---|---|---|
| Authenticated synchronous normal/free/rollback | Missing | Same isolated Pro account with exhausted processing quota and zero credits: normal returned `429 HOURLY_LIMIT_REACHED`; free returned `200`; rollback to normal returned the same `429`. Billing state was unchanged by free processing. | Closed for the local API matrix |
| Account async normal→free and free→normal | Repository tests only | Real OCR/PDF-to-Markdown tasks ran through local backend, task persistence, worker, and download/cancel finalization. Stored DB reservations settled after completion through a free-mode replica; empty IDs stayed unbilled after finalization through a normal-mode replica. | Partial: account success and normal→free cancellation covered; account free→normal cancellation and worker-returned async failure not covered live |
| Guest async normal→free and free→normal | Repository tests only | Real guest OCR and PDF-to-Markdown tasks exercised Redis pending/used transitions, mode-opposite finalization, cancellation, and duplicate download/cancel. | Partial: success/cancel covered in both directions; worker-returned async failure not separately induced live |
| Ownership/authentication and billing non-mutation | Partial | Protected checkout without identity returned `401`; wrong-owner task status/cancel/download returned `403`; reservations remained pending until an authorized finalizer. | Closed for exercised routes |
| Free-mode validation/resource capacity | Partial | Missing upload and invalid file remained `400`; 1001-page PDF remained `400 PAGE_LIMIT_EXCEEDED`; saturated global technical capacity remained `429 SERVER_BUSY`, distinct from billing quota. | Closed for listed checks; upload-size boundary not run |
| Paid Pro real-browser free/rollback | Missing | Real local session and local Pro account showed stored Pro/120 credits separately from free policy and kept management controls; normal rollback restored purchase affordances. | Closed for local browser presentation |
| Lint disposition | Unresolved | Candidate changed-file lint had 20 errors/14 warnings; fetched `origin/main` baseline had 20 errors/15 warnings. No candidate-only diagnostic signatures. Repository CI runs unit tests and TypeScript but not lint. | Existing debt is not a CI gate; no new lint regression |
| Replica consistency | Missing | Concurrent local normal and free replicas on separate ports reported their own policy while sharing isolated state. | Local behavior demonstrated; uniform production configuration remains an operational prerequisite |
| Historical OCR `failed to finalize billing` | Unresolved | Source emitter identified; representative sync/async success, failure, cancel, and settlement paths ran without reproducing the message. No original incident logs exist in this workspace. | **Unresolved: historical cause not proven** |
| External storage isolation | Not previously recorded | A first PDF-to-Markdown request in this continuation used a nonlocal R2 endpoint inherited from repository environment loading and may have created the source object listed below. No remote read or delete was attempted. | **Unresolved external side effect; blocks release closure** |

### Isolated services and account state

The continuation used the local disposable PostgreSQL server on `127.0.0.1:55432`, disposable Redis on `127.0.0.1:16379`, and local worker/storage services. The final full Go suite used a fresh `test_gim12_fullrun` database, Redis DB 3, and a separate worker listener on `127.0.0.1:18005`; it did not use the live-rehearsal database or Redis DB 1. A local S3Mock bucket was used for the later PDF-to-Markdown worker tests. The shared application Redis on port 6379 was not used for billing tests.

The authenticated fixture was a local Pro account (`gim12-live-pro@platen.invalid`) with fake local Paddle identifiers and management URLs. For the synchronous quota comparison, the fixture had no custom credits and processing windows at their normal-mode limits. The same account/request produced normal-mode `429 HOURLY_LIMIT_REACHED`, free-mode `200`, then `429 HOURLY_LIMIT_REACHED` after rollback. Free success created no billing reservation and changed neither usage nor credits or stored tier/status/Paddle identifiers. Later async and browser cases used a paid-state fixture with 120 credits; those were separate test phases and their normal-mode accounting changes are recorded below. Test-only account state was confined to the disposable database.

### Authenticated synchronous API evidence

`POST /api/structure/rotate` was exercised through the actual local backend using a valid supported PDF and the same authenticated account:

| Mode | Result | Billing state |
|---|---|---|
| normal | HTTP `429`, `HOURLY_LIMIT_REACHED` | No successful operation; quota remained exhausted; credits stayed 0; no new reservation row |
| free | HTTP `200`, valid rotated PDF | No new reservation; usage stayed at its saturated before-state; credits stayed 0; subscription fields stayed unchanged |
| rollback to normal | HTTP `429`, `HOURLY_LIMIT_REACHED` | Same billing state as before the free request |

This closes the prior authenticated synchronous API evidence gap for the tested operation. It does not generalize to every synchronous endpoint.

Additional sync OCR checks used the paid-state fixture: valid normal OCR returned `200` and committed its real 7-unit database reservation; valid free OCR returned `200` without another reservation or usage/credit mutation. An invalid-language request returned the existing `500 OCR_PROCESSING_FAILED` in both modes. In normal mode its real reservation was released and no usage was charged; free mode made no billing write. This keeps validation/processing behavior independent from monetization policy.

### Account and guest async lifecycle evidence

The live async exercise used local OCR and PDF-to-Markdown worker paths, not direct calls to `CommitAsync`/`ReleaseAsync`.

**Account normal→free:**

- Authenticated OCR task `accd1be0-dd2c-464c-b2b9-d12081348df0` stored database reservation `b2f7ad90-14de-4191-9206-20e30035a04e`, completed through the local worker, and committed 7 plan units exactly once. The account's 120 test credits were unchanged.
- A real PDF-to-Markdown task `d1b10ece-ab94-4dbe-891f-07ee7e8664bc` stored DB reservation `212cd130-0b5c-4d83-b7ca-eb5e782f4bba` for 4 plan units. The worker completed the task; the reservation remained pending until its authorized download. A wrong-owner download through the free replica returned `403` and left it pending. The correct owner downloaded through the free replica (`200`), repeated the download (`200`), and the DB reservation committed once. Each usage window rose by 4; credits remained 120 and the stored Pro/active subscription stayed unchanged.
- A normal-mode 141-page OCR task `ad97afa4-1790-4310-84b8-2e601a0d177d` stored reservation `046d39b3-cb2f-447c-b814-60401aaaa9fa` for 76 units. Cancellation through the free replica returned `200`; repeated cancellation also returned `200`. The task reached `CANCELLED`, the real DB reservation became released, and no usage/credit charge occurred.

**Account free→normal:**

- A free-mode PDF-to-Markdown task `d137a8be-64f7-424c-8c79-b01d433d6a07` completed with empty reservation ID. Authorized download through the normal replica and duplicate download both returned `200`. No reservation row was created retroactively; usage and 120 credits were unchanged.
- A free-mode OCR task `2548acc9-4781-4ac4-86cf-5d08509f4267` also completed with an empty ID and database kind. Download through the normal replica returned `200`; there was no billing settlement.

**Guest normal→free:**

- Guest OCR task `227d2c98-c01b-4582-b5d0-21ced37276c9` persisted real guest reservation `797344ae-07b3-4600-a046-161eb6b6ec32`; worker completion settled 7 units: guest pending returned to zero, used increased once, and the reservation key disappeared. Download succeeded.
- Guest PDF-to-Markdown task `0541c887-920f-4adb-af96-e55a242f444d` persisted reservation `b64be392-4c70-400b-87dc-058222a6f5bb`, with 4 units pending. An account identity's download returned `403` and left pending unchanged. Guest-owner download through the free replica returned `200`; duplicate download returned `200`; pending went to zero, used increased by 4 once, and the reservation key was removed.
- A normal guest OCR task `b4fc8cd7-d0ae-4d82-884e-8902d970e97f` held a real 8-unit Redis reservation while processing. Wrong-owner status and cancel calls returned `403`; guest-owner cancellation through the free replica returned `200` twice. Pending returned to zero, used remained zero for that reservation, and no guest reservation key remained.

**Guest free→normal:**

- Free guest OCR task `d304150f-f8b3-4813-bb0b-3ce3318dd5e7` and free guest PDF-to-Markdown task `0446cfb1-3616-4f85-ae53-153952c6a0da` had empty IDs. Worker completion and authorized downloads through the normal replica succeeded. Guest used/pending values were unchanged and no guest billing reservation key was created.
- A larger free guest OCR task `e61defca-695d-4d2a-b8b7-fce79bfca486` was cancelled through the normal replica; duplicate cancellation was safe. It had an empty ID and caused no Redis billing mutation.

These exercises prove the tested allocation-time reservation identity survives cross-mode completion. They do not prove every possible simultaneous commit/cancel race schedule; no state-machine redesign was attempted.

The live matrix did not separately inject a worker-returned asynchronous processing error in either identity class. Account free→normal success was driven, but account free→normal cancellation was not. Account normal→free cancellation, guest cancellation in both mode directions, and stale-account release were driven. These missing cases are still part of the requested full transition matrix and should be closed before GIM-12 can be marked complete.

### Stale recovery, authorization, validation, and resource controls

- With the Dramatiq actor paused, a real queued account PDF-to-Markdown task had a pending 4-unit DB reservation. The isolated Redis task record was aged using the test Redis DB's supported data path, then an actual task-status GET on the free replica invoked stale recovery. The task became `FAILED`, the real reservation became `released`, and a repeat GET did not release it twice. This was an API/integration stale-path test, not a wall-clock production-timeout wait.
- Unauthenticated protected checkout returned `401`. While a guest task had pending quota, wrong-account status and cancellation returned `403`. Wrong-owner downloads for account and guest PDF-to-Markdown tasks returned `403` and preserved pending settlement state until the correct owner finalized.
- In free mode, missing upload returned HTTP `400 MISSING_UPLOAD_FILE`; an invalid text payload named as PDF returned the same existing code/status. A generated 1001-page valid PDF returned HTTP `400 PAGE_LIMIT_EXCEEDED` with the 1000-page maximum in the message.
- Global technical capacity was filled only in isolated Redis DB 1 with four temporary active-job members. A valid free-mode `/api/structure/rotate` request returned HTTP `429 SERVER_BUSY` and `Retry-After: 5`, proving the 429 was technical capacity, not billing quota. Only those four test members were removed afterward.
- Upload-size boundary was not exercised in this continuation. Per-identity heavy-job saturation was not separately run; global technical-capacity evidence and existing automated tests remain the available proof for that area.

### Paid Pro real-browser free/rollback and checkout race

The local frontend ran in a real browser against the local backend and actual authenticated session cookie; the session/account was not mocked. In free mode:

- `/subscribe` continued to show stored `Pro` and a distinct free-processing policy; five purchase controls were disabled.
- `/dashboard` showed the actual 120-credit balance and no credit-pack purchase buttons.
- `/dashboard/settings#billing` showed the active Pro subscription and retained “Update Payment Method,” “View Billing Portal,” and “Manage Subscription” controls.
- The browser recorded no page errors or failed requests in that free-mode pass.

After refreshing the same account against the normal replica, Pro and 120 credits remained visible, eligible plan and credit purchase controls returned, and management controls remained present. No actual portal link was followed and no Paddle request was made. Test management URLs were local fake URLs.

The `PURCHASES_DISABLED` stale-session race was not repeated against a live browser. The repository's mocked Playwright test covers the response/refresh/no-retry behavior; a real race would require deliberately routing session and checkout to differently configured replicas or coordinating a process restart at a narrow UI moment. That was not treated as equivalent to production rollout and no Paddle overlay was opened.

### Historical OCR finalization incident

Source search found one emitter of the exact historical phrase `failed to finalize billing`: authenticated synchronous billing middleware in `internal/billing/middleware.go` when controller work succeeds but reservation commit fails. That handler attempts release and returns HTTP 500; it discards the underlying commit/release error details, so the phrase alone does not reveal whether the cause was a missing reservation, database connectivity, or another settlement failure.

Representative local normal/free OCR success, invalid-language failure, async completion, cancellation, and cross-mode task/download settlement were exercised. Normal success committed; normal processing failure released; free paths with empty IDs performed no billing settlement; async account/guest stored reservations settled in their authoritative stores. The exact historical error did not recur. No original production logs or failed request identifiers are available here, so the historical root cause is **not proven** and cannot be declared fixed solely from non-reproduction. This remains a release follow-up: obtain the original error context/observability or accept a documented incident disposition before claiming resolution.

### Worker metadata finding

Backend PDF-to-Markdown creation passes reservation ID and kind into task persistence. The worker mirror code (`pdfnest-worker/app/jobs/store.py::sync_task_mirror`) mirrors `reservationId` but omits `reservationKind`. Real mirrored task state exhibited that omission for account and guest tasks. The backend compatibility resolver selected the correct authoritative DB/Redis reservation store and the live download flows settled correctly. No worker code was changed; this remains a compatibility risk to retain in future auditing.

### Nonlocal R2 side effect — release blocker

The first PDF-to-Markdown integration attempt in this continuation was launched before the backend's nonlocal `.env` R2 values had been overridden. The endpoint returned `202`, and the backend uploaded the test source under:

```text
jobs/markdown/source/a1e179c5-4d19-4ed8-9e8a-3e71a4ddd169.pdf
```

The endpoint host was checked only as nonlocal; the exact account/bucket ownership was not established in this report. The later worker was configured to the local S3Mock and returned `404` for that local copy of the key; it did not read the nonlocal object. No subsequent remote read, list, delete, or cleanup was attempted. This addendum supersedes the earlier broad statement that object storage was not used for processing **for this continuation only**. The test likely left an orphaned test PDF in a nonlocal object store. An authorized operator must identify the owning environment and reconcile/delete the exact object through the normal storage process before the rollout evidence can be considered clean. No product source or persistent environment setting was changed.

### Replica, lint, backend, and frontend verification

Two local backend processes were active concurrently: normal on port `18081` and free on `18080` (later S3Mock-backed instances used `18083` normal and `18082` free). Their guest `/api/auth/session` responses reflected their own process-local environment while returning the same stored Pro subscription. This demonstrates the mixed-fleet hazard: during production rollout and rollback every backend replica must receive the same `BILLING_MODE`; the session endpoint reports the policy of the replica that served that request. No production deployment tooling was changed.

The required final backend command was rerun with a fresh isolated database, disposable Redis DB 3, and the separate local worker:

```text
go test -p 1 -count=1 -timeout=300s ./...
```

Result: **PASS**, exit code 0. The output included `internal/billing`, `internal/auth`, `internal/conversion`, `internal/ocr`, `internal/studio`, `internal/studio/models`, and `internal/tasks`, plus the remaining packages. Studio metadata preservation passed with the worker listener healthy.

Frontend verification after live browser work:

- `npm run test:unit`: **PASS**, 100/100 test files.
- `npx tsc --noEmit`: **PASS**.
- `npm run build` with local/dummy API and Paddle configuration: **PASS**.
- `NEXT_PUBLIC_API_URL=http://127.0.0.1:18080 npx playwright test tests/e2e/free-mode-ui.local.spec.ts --project=chromium`: **PASS**, 8/8 mocked tests.
- Real local browser tests against authenticated local backend: free Pro policy and retained management controls passed; normal rollback restored purchase affordances; no page errors/failed requests recorded.
- Targeted lint: candidate changed-file diagnostics were 20 errors/14 warnings; fetched `origin/main` baseline on the same 23-file set was 20 errors/15 warnings. Diagnostic signatures showed no candidate-only errors or warnings; five new GIM-8/GIM-10 files had zero diagnostics. `.github/workflows/container-build.yml` runs unit tests and TypeScript but not lint; README documents lint as a developer command, and no zero-error release gate was found in repository CI. Accordingly this is pre-existing lint debt and not a CI-blocking regression, while a separate release policy outside the repository remains unknown.

### Final decision and remaining blockers

**Decision: BLOCKED.** The authenticated live synchronous matrix, tested account/guest async transition paths, paid Pro browser free/rollback, task ownership, validation, technical global 429, full backend suite, and frontend unit/type/build/browser checks now have local evidence. The earlier lack of local PostgreSQL/worker/full-suite evidence is closed.

Do not close GIM-12 yet. Remaining actions:

1. **High — nonlocal storage side effect:** have an authorized operator identify and reconcile the exact source object above. Do not use this report as proof it was removed.
2. **High — historical OCR error:** original `failed to finalize billing` cause is not proven; obtain incident logs/context or record an explicit accepted disposition. Current-path non-reproduction is not a root-cause fix.
3. **High — async transition completeness:** induce worker-returned failures for account and guest async tasks, and drive account free→normal cancellation through the live task lifecycle.
4. **Operational — replica consistency:** assign rollout ownership to ensure every replica has the same mode before and during rollout/rollback; no production multi-replica control plane was rehearsed.
5. **Evidence gap — live purchase race:** only the mocked browser race plus GIM-9 backend tests are available; no real local stale-session purchase click was run.
6. **Optional validation gap:** upload-size and per-identity capacity live saturation were not separately driven; existing automated tests remain the evidence for those controls.

Until items 1–3 are handled and reviewed, retain `GIM-12` as **In Progress** and do not proceed to staging/rollout. The later GIM-8+ product behavior is not expanded in this continuation.

### Continuation Git record

The backend and frontend remained on `gim-12-free-mode-verification`. The only intended continuation change is this documentation file. The full backend source tree and frontend source tree were not modified. No push, PR, merge, deployment, external Paddle call, or production configuration change occurred. The nonlocal object write described above is the sole known external-service side effect and remains unresolved.

## Final blocker-closure continuation — 2026-10-06

This continuation preserves the preceding blocked evidence and updates the three live async gaps, investigates the R2 side effect using local evidence only, and distinguishes the current OCR finalization emitter from the unresolved historical incident. No product source was changed.

### Updated blocker state

| Gate | State before this continuation | New evidence | State now |
|---|---|---|---|
| Account free→normal cancellation | Missing | Live authenticated async OCR task created in free mode, then cancelled through the normal-mode task API twice | Closed for the exercised task path |
| Account normal worker-returned failure | Missing | Live worker accepted work, failed during OCR processing, and backend released its real database reservation | Closed for the exercised task path |
| Guest normal worker-returned failure | Missing | Live worker accepted guest work, failed during OCR processing, and guest pending quota/reservation were released | Closed for the exercised task path |
| Free-mode worker failure | Optional | Account and guest async free-mode requests reached worker failure with empty reservation IDs and no billing allocations | Closed for the exercised paths |
| Nonlocal R2 object | Open | Local provenance/root-cause evidence assembled; no remote object operation was authorized or performed | **Open — operator reconciliation required** |
| Historical synchronous OCR `failed to finalize billing` | Open | Current emitter and error conditions documented; generic emitter reproduced with an isolated forced database write error, but historical cause is not established | **Open — incident evidence or owner risk acceptance required** |
| Same billing mode on every replica | Operational prerequisite | Prior local mixed-mode replica rehearsal remains evidence; no production deployment owner was assigned | **Open operational prerequisite** |
| Full backend suite | Previously reported pass; this continuation first hit harness/DB limits | Fresh isolated run passed all packages with local storage and a 300-connection disposable PostgreSQL server | Closed |

The release decision remains **BLOCKED**. The two operationally unresolved high-severity items (R2 reconciliation and historical OCR incident disposition) prevent closure even though the newly requested local async gates and final backend/frontend regressions passed.

### Account free→normal cancellation — live API

Using the isolated local Pro account, `POST /api/ocr/extract-text-async` on the free-mode backend returned HTTP `202`. Task `58ccda8e-7ee0-499f-b168-0bf50beda275` reached `PROCESSING` with an empty `reservationId` and stored `reservationKind=database`. Before submission the test account was Pro/active with 120 custom credits, zero in each usage window, and no processing reservation rows. The same account/task was then sent to the normal-mode backend's authenticated `DELETE /api/v1/tasks/58ccda8e-7ee0-499f-b168-0bf50beda275`; the first and repeated cancellation both returned HTTP `200`. The task remained `CANCELLED` with empty reservation ID. The before/after subscription state remained Pro/active, credits 120, usage 0/0/0, and task-linked processing reservation count zero. No reservation was retroactively created, no usage or credit changed, and repeated cancellation did not create a billing mutation.

This closes the specified account free→normal cancellation gate for the actual OCR task lifecycle; it is not a direct service-call test. The invalid first barrier attempt from this continuation, where a short failure shim caused the worker to fail before cancellation, is not counted as cancellation evidence.

### Account worker-returned failure — live API and worker

Normal-mode authenticated `POST /api/ocr/extract-text-async` returned HTTP `202`, persisted task `a77a69b4-6ba5-4ef2-ba4e-30defffc1333`, and created exactly one database reservation `2b6764a6-fa54-4b98-94ce-25fc9eb08654` for 6 plan units and 0 credit units. Its initial state was `reserved`; the account's baseline was Pro/active, 120 credits, and usage 0/0/0. The request was accepted and the worker reached the real local `/api/v1/ocr/extract-text` processing endpoint. A test-only local Tesseract executable then returned a processing error. This failure occurred after dispatch and worker processing began; it was not request validation, task persistence, dispatch transport failure, or cancellation.

The worker returned HTTP `500`; the task became `FAILED` with the worker error. A repeated authorized task-status GET left it failed. The reservation became `released`; no usage log or plan usage increment was recorded, and credits remained 120. The release was effective once. The local Tesseract shim was a controlled test fixture; no product or worker source was changed.

### Guest worker-returned failure — live API and worker

Normal-mode guest `POST /api/ocr/extract-text-async` returned HTTP `202` and created task `940cc420-e492-44c4-8911-f4d7068d0891` with guest reservation `e266bffe-a710-40c2-9810-52b202637ec2` and kind `guest`. Before failure, the guest quota state had `pending_3h=6`, `used_3h=0`; the same reservation key existed. The request reached the local worker OCR processing endpoint, where the same controlled Tesseract fixture returned a processing error and worker HTTP `500`.

The task became `FAILED`; a repeated authorized status GET did not alter it. After finalization, guest pending returned to 0, used remained 0, all checked windows showed no usage charge, and the guest reservation key was absent (`EXISTS=0`). The guest reservation was released effectively once. This proves a worker-returned failure path, not a Redis/PostgreSQL outage simulation.

### Optional free-mode worker-failure regression

The same controlled processing failure was exercised after requests had been accepted in free mode:

- Account task `08ed462d-886c-487d-98f0-99979baf6e48` became `FAILED`, stored an empty reservation ID and `database` kind, and had no billing reservation row for its task ID. The account remained Pro/active with 120 credits and the usage state measured at that phase was unchanged.
- Guest task `b9370276-cc7f-480e-9cf1-ee438e97efde` became `FAILED`, stored an empty reservation ID and `guest` kind, and left all guest used/pending quota values at zero with zero guest billing reservation keys.

The task failure remains visible normally; free mode did not disable worker failure reporting or alter identity/task behavior.

### R2 incident facts and local provenance

The exact known object key remains:

```text
jobs/markdown/source/a1e179c5-4d19-4ed8-9e8a-3e71a4ddd169.pdf
```

No bucket listing, object read/download, delete, or remote configuration request was made during this continuation. Based on the locally available Codex command history, the request was an authenticated normal-mode test-phase `POST http://127.0.0.1:18081/api/conversion/pdf-to-markdown-async` on 2026-10-05 at 19:02:33 UTC (2026-10-06 00:32:33 Asia/Colombo). The backend had been launched from the `pdfnest-backend` working directory with `BILLING_MODE=normal` and `PORT=18081`; the launch command had no explicit R2 or `STORAGE_MODE` override. The request returned `202`; its task identifier aligns with the object key. The exact backend child PID was not recorded.

The current route registration in `internal/conversion/routes.go` maps both `/conversion/pdf-to-markdown-async` and `/conversion/pdf-to-markdown` to `Controller.HandleAsyncPDFToMarkdown`. That handler in `internal/conversion/pdfToMarkdown.go` calls `storage.Default()` and `UploadFile` for `jobs/markdown/source/<task-id>.pdf` before worker processing. The path directly asks for the R2-backed store; it does not use the development `RemoteStorageEnabled()` selector before the upload.

The local `.env` was inspected with values redacted. It contains `R2_ENDPOINT`, `R2_BUCKET`, `R2_ACCESS_KEY`, and `R2_SECRET_KEY`; the endpoint is HTTPS and non-loopback. `APP_ENV=development`; `STORAGE_MODE` is unset. `main.go` calls `godotenv.Load()` before initializing services. Since the historical process environment was not snapshotted, it is not possible to prove whether each value came from the ambient shell or was supplied by dotenv after being absent from that shell. The launch record did not show explicit R2 exports, so dotenv is the likely source. No endpoint host, account identifier, bucket value, or credentials are reproduced here.

Ownership confidence for the destination is **unknown**. Local evidence proves that a configured non-loopback Cloudflare R2 endpoint was selected; it does not establish whether the account/bucket was authorized test storage or production storage.

### R2 operator reconciliation packet and prevention

Required operator action: an authorized storage operator must use approved infrastructure configuration (not this report or Codex access) to identify the Cloudflare account and bucket corresponding to the local R2 configuration, verify whether the exact key above exists, reconcile/delete it according to the normal storage incident and retention procedure if appropriate, and record the account/bucket identity, key, action, and verification result. Do not disclose credentials in that record. **Remote object reconciliation was not completed by an authorized operator.** This remains a release blocker.

The supported cause classification is **test-harness/environment setup error**, combined with an unsafe handler configuration boundary: the integration launch omitted an explicit local storage configuration, dotenv could load nonlocal R2 settings, and this handler directly instantiated/used the R2 store. Setting only `STORAGE_MODE=local` would not have prevented this particular handler from calling `storage.Default()` when all R2 variables are present. The provenance caveat above means the exact shell-versus-dotenv source cannot be proven.

Future GIM-12 launch procedure: before starting any integration backend, require a preflight that fails unless every storage path used by the selected flow is explicitly local/loopback or filesystem-backed. For PDF-to-Markdown tests, do not provide real R2 credentials; use a dedicated local adapter/test fixture or fail closed before starting the test backend. Inspect the effective process environment without printing values, verify the endpoint is loopback when an S3 mock is used, and audit direct `storage.Default()` call sites because `STORAGE_MODE=local` alone is insufficient there. This is a verification-harness recommendation; no product behavior was changed.

### OCR `failed to finalize billing` emitter and source-supported causes

The only Go source emitter of the exact lowercase response text is `internal/billing/middleware.go:67-71`. Current path:

```text
POST /api/ocr/extract-text or /api/ocr/to-text-pdf
  -> billing.Use(tool)
  -> identity check and request estimate
  -> Default.Reserve(...)
  -> controller runs
  -> successful controller response (status < 400)
  -> Default.Commit(reservation.ID)
  -> on commit error, best-effort Default.Release(reservation.ID)
  -> HTTP 500 {"error":"failed to finalize billing"}
```

`internal/ocr/routes.go:20-21` registers those two synchronous routes behind `billing.Use`. `POST /api/ocr/extract-text` reaches `Controller.ProcessOCR`, which validates the upload/page limit and calls `ocrService.ExtractTextFromPDF`; `internal/ocr/extractText.go:22-38` posts to the worker's `/api/v1/ocr/extract-text`, rejects a non-2xx worker response, and only returns success after it has a successful worker response/body. `ProcessImageToTextPDF` is the second synchronous billing-wrapped OCR route and returns success only after its image-processing/service operation succeeds. The exact historical phrase therefore points to the synchronous authenticated middleware finalizer, rather than the async task path. The available historical note does not preserve a request path/request ID, so the specific synchronous OCR route cannot be distinguished.

For a non-empty reservation ID, `Service.Commit` in `internal/billing/service.go:407-477` can return an error if the transaction cannot start/commit, the reservation `SELECT ... FOR UPDATE` fails (including a missing row or DB/query failure), the conditional reservation update fails, the subscription lookup fails (including missing subscription), saving subscription accounting fails, or inserting the usage log fails. A non-reserved status and a conditional update with zero affected rows are deliberate no-op success paths, not errors. `Release` is attempted after commit error, but its error is discarded by the middleware; if the DB update itself fails, release may remain unapplied. This list describes errors supported by the code; it does not identify which condition occurred historically.

### OCR local reproduction and historical disposition

All cases used isolated local infrastructure and the live HTTP routes:

| Case | Route/result | Billing evidence |
|---|---|---|
| Normal authenticated OCR success | `POST /api/ocr/extract-text`; HTTP `200`, text/plain, 90-byte worker result | Real 6-plan-unit reservation committed; account usage became 6/6/6 and one usage log was added; no finalization error |
| Normal authenticated OCR worker/controller failure | Same route with valid PDF; controlled local worker/Tesseract failure; HTTP `500 OCR_PROCESSING_FAILED` | Real reservation released; no usage log or usage increment; credits unchanged |
| Free authenticated OCR success | Same route on free replica; HTTP `200` with worker result | Empty-ID commit was a no-op; reservation count, credits, and usage remained as measured immediately before the free request |
| Normal allocation followed by free-mode settlement | Synchronous same-request process switch was not practical because each local backend process retained its configured mode. Async cross-mode success/failure/cancel paths are separately live-verified above and in prior report history. | Stored real ID/kind finalized against its store despite the opposite current replica mode |

The exact current HTTP emitter was also forced in the isolated disposable database: a temporary, test-only `BEFORE INSERT` trigger for the isolated user's `UsageLog` caused an otherwise successful normal synchronous OCR request to fail during commit. The actual worker OCR processing succeeded; GORM logged the injected SQL error; the transaction rolled back; the API returned HTTP `500` with the exact `{"error":"failed to finalize billing"}` body; the middleware's release changed the reservation to `released`, and usage/counter state remained unchanged. The trigger/function were dropped immediately after the test. This demonstrates one source-supported way the current emitter can occur; it does not reproduce or prove the historical incident mechanism.

GIM-6 empty-ID semantics explain why free-mode synchronous finalization is a no-op. GIM-7's reservation-kind resolver concerns async settlement and cannot fix this synchronous `Service.Commit` path. No GIM-5 through GIM-11 source change has been shown to remove the historical failure mechanism. Original production logs, request ID, exact route, and underlying commit error remain unavailable. The required incident classification is **NOT REPRODUCED — INSUFFICIENT EVIDENCE**. The historical incident remains a release blocker unless the project owner supplies incident evidence or explicitly accepts the residual risk.

### Replica consistency and remaining optional checks

The earlier two-replica rehearsal remains valid and was not repeated. Each backend reports its own process-local policy. Before rollout and rollback, the operator must apply the same `BILLING_MODE` to every backend replica and verify each instance's session policy through approved deployment tooling. A rollout owner still needs to be assigned; no production infrastructure command or deployment was used here.

The real stale-session `PURCHASES_DISABLED` browser race was not repeated. Existing evidence remains the backend GIM-9 guard plus mocked Playwright race from the prior report; no real Paddle call or overlay was triggered. The optional upload-size and per-identity capacity checks were also not repeated; the prior live missing-upload, invalid-PDF, page-limit, and global technical `429 SERVER_BUSY` checks remain documented. These do not replace the unresolved high-severity R2/OCR gates.

### Final backend and frontend regression results

The first full-suite attempt in this continuation used a local S3Mock R2 endpoint and PostgreSQL `max_connections=100`. It exposed a test-harness mismatch: `TestGuestAsyncRequestPostFixSuccess` received an `r2://` artifact URL then tried to read it with `os.ReadFile`, which expects the local filesystem artifact. With all `R2_*` variables omitted and local storage selected, the focused command `go test ./internal/ocr -run '^TestGuestAsyncRequestPostFixSuccess$' -count=1` passed. A second suite attempt with the corrected storage environment still hit PostgreSQL `too many clients already` in Studio tests. These were not treated as product passes or code failures.

The disposable PostgreSQL container's `max_connections` was raised from 100 to 300, a new empty test database (`test_gim12_final_20261006_b`) and Redis DB 6 were used, the healthy worker was restricted to loopback, and all `R2_*` variables were absent. The final command was:

```text
GOMAXPROCS=2 go test -p 1 -count=1 -timeout=300s ./...
```

Result: **PASS**, exit code 0. All packages passed, including `internal/billing`, `internal/auth`, `internal/conversion`, `internal/ocr`, `internal/studio`, `internal/studio/models`, `internal/tasks`, and `internal/storage`. The local worker health endpoint passed before the run. No nonlocal storage endpoint was used in this final suite run.

Frontend regressions after the live work:

- `npm run test:unit`: **PASS**, `UNIT SUITE: 100/100 files passed`.
- `npx tsc --noEmit`: **PASS**, exit code 0.
- `APP_ENV=development MANAGED_BUILD=false` with loopback API/app URLs and dummy local Google/Paddle build values, then `npm run build`: **PASS**. Next compiled, type-checked, generated all 38 static pages, and exited 0. During static generation it attempted the configured loopback backend at `127.0.0.1:18021`, found it unavailable, and used the bundled static tools catalog as designed; this was a local fallback warning, not a build failure or nonlocal request.
- Lint was not rerun because there are no frontend source changes. The prior same-file-set comparison remains candidate 20 errors/14 warnings versus `origin/main` 20 errors/15 warnings, with no candidate-only diagnostics; the inspected repository CI does not run lint.
- Browser suites were not rerun: prior report evidence remains mocked Playwright 8/8 and the real local paid-Pro free/normal rollback browser run. The optional real stale-session purchase race remains unverified.

### Final release decision and required ownership

**Final decision: BLOCKED.** The newly requested account free→normal cancellation, normal account worker failure, normal guest worker failure, optional free-mode worker failures, and final complete backend/frontend regression gates now pass. GIM-12 must remain In Progress because:

1. **Storage operator action (high severity):** reconcile the exact R2 source object under approved account/bucket procedures and record the outcome. Codex did not and must not perform a remote object operation without authorization.
2. **OCR incident disposition (high severity):** provide original incident logs/request context and identify the underlying commit error, or have the project owner explicitly accept the remaining risk. No source fix has been tied to the historical incident.
3. **Rollout ownership (operational prerequisite):** assign responsibility for consistent `BILLING_MODE` across all replicas during rollout and rollback and instance-by-instance policy verification.

Until items 1 and 2 are resolved and item 3 has an owner/checklist, keep GIM-12 **In Progress** and **do not proceed to staging/rollout**. No product-source changes were made in this continuation; no remote storage access, production-data mutation, external Paddle action, push, PR, merge, or deployment occurred.
