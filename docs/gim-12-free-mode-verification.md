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
