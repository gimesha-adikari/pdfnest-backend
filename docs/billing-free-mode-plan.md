# Platen Reversible Free Mode Implementation Plan

> **For agentic workers:** implement this plan task-by-task. Read this file and the linked Linear issue before changing code. Use tests first, keep commits scoped, and do not fold unrelated billing repairs into the free-mode feature.

**Goal:** Make supported Platen processing free for guests and registered users through a reversible backend operating mode, while preserving the existing subscription, credit, Paddle, authentication, ownership, resource-safety, and billing-history systems.

**Architecture:** Add one authoritative backend mode, `BILLING_MODE=normal|free`. In free mode, billing allocation returns a valid non-nil allocation with an empty reservation ID and zero plan/credit units before any billing-only PostgreSQL or Redis prerequisite is required. Real reservations created in normal mode always settle according to their saved ID/kind, regardless of the mode at completion time.

**Tech Stack:** Go/Fiber backend, PostgreSQL/GORM, Redis, Next.js/React frontend, Paddle billing, Python processing worker.

**Linear project:** https://linear.app/gimesha/project/platen-reversible-free-mode-4aa3b71e3606

## Investigation baseline

This plan was written against:

- Backend `gimesha-adikari/pdfnest-backend` `master`: `9faae1a42155843e0e5a6e472d6a4109ccaa25a8`
- Frontend `gimesha-adikari/pdfnest` `main`: `70db8e8a5a1466ddb154112ed1ddecee6e6cb57e`
- Worker `gimesha-adikari/pdfnest-worker` `main`: `9d38852e7ca1e7b657f7f644823d400553886ff0`
- SDK `gimesha-adikari/platen-document` `main`: `a5a14413ded0daa93a5839b86554f1fe67d92a93`

If those revisions move materially before implementation, re-check the touched billing, task, identity, and frontend policy paths before applying this plan.

## Global constraints

- The backend is authoritative for billing access.
- Supported backend values are exactly `normal` and `free`.
- Missing `BILLING_MODE` defaults to `normal`.
- Invalid nonempty values fail startup.
- Do not add an authoritative `NEXT_PUBLIC_BILLING_MODE`.
- Do not delete subscription, credit, Paddle, quota, reservation, authentication, or billing-history code.
- Do not rewrite users to Pro, grant huge credits, fabricate subscriptions, zero credits, or flush Redis.
- Do not remove login, ownership checks, object-storage authorization, upload validation, page limits, concurrency controls, timeouts, cancellation, idempotency, cleanup, worker authentication, or abuse/resource protections.
- Free processing does not imply that every currently account-protected workflow becomes anonymous.
- No database migration is expected.
- `pdfnest-worker` and `platen-document` require no operating-mode changes unless tests prove a narrow compatibility fix is unavoidable.
- Existing Paddle webhooks remain verified, idempotent, and active.
- Existing subscribers retain customer-management/cancellation access.
- New subscription and credit checkout creation is disabled in free mode.
- Real reservations created in normal mode must still commit/release after switching to free mode.
- Free-mode work represented by an empty reservation ID must never be retroactively billed after switching back to normal mode.

## Core runtime model

### Normal mode

```text
BILLING_MODE=normal

identity
  -> estimate
  -> existing guest Redis or account PostgreSQL allocation
  -> existing processing path
  -> existing commit/release
```

Normal mode preserves current behavior, including existing route-specific differences.

### Free mode

```text
BILLING_MODE=free

identity
  -> validation/resource admission
  -> billing allocation facade
       -> non-nil allocation
       -> reservation ID = ""
       -> plan units = 0
       -> credit units = 0
       -> no billing DB/Redis write
  -> existing processing path
  -> finalizer sees empty ID and performs no billing settlement
```

Free mode must short-circuit before any dependency that exists only to enforce monetization. In particular, guest processing must not fail because `GuestQuota`/Redis billing quota storage is unavailable if that storage is not otherwise required for the operation.

### Mode transitions

```text
normal reservation -> switch to free -> completion
real reservation ID remains authoritative -> settle normally

free allocation -> switch to normal -> completion
empty reservation ID remains authoritative -> no settlement
```

Never decide whether to settle a reservation by consulting the *current* `BILLING_MODE`.

## File responsibility map

### Backend files expected to change

- `config/billing_mode.go` — new mode parser/policy helpers.
- `config/runtime.go` — validate the mode before the current managed-environment early return.
- `main.go` — ensure mode initialization/validation occurs after environment loading.
- `.env.example` — document `BILLING_MODE=normal`.
- `internal/billing/service.go` — account allocation and async allocation free-mode semantics.
- `internal/billing/guest_quota.go` — guest allocation/free empty-ID finalization semantics where still needed.
- `internal/billing/middleware.go` — preserve locals/contracts and avoid billing-only guest quota prerequisites in free mode.
- `internal/auth/session.go` — expose safe effective billing policy.
- `internal/billing/paddle_checkout.go` — block new purchases in free mode before DB preparation/external calls.
- `internal/structure/controller.go` — remove direct paid-tier duplication entitlement in free mode while retaining finite technical bounds.
- `internal/billing/controller.go` — keep stored subscription separate from effective policy; reconcile policy presentation where needed.

### Frontend files expected to change

- `context/AuthContext.tsx` — store effective policy separately from the actual subscription.
- `components/Header.tsx`
- `app/(site)/Home.tsx`
- `app/(site)/subscribe/page.tsx`
- `app/(site)/pricing/page.tsx`
- `components/subscription/PlanButtons.tsx`
- `app/(site)/dashboard/page.tsx`
- `app/(site)/dashboard/settings/page.tsx`
- `components/paddle/PaddleTransactionBridge.tsx`
- `lib/paddle.ts`
- `components/shared/ProcessingModeSelector.tsx`
- `lib/notify.ts`
- focused error-handling/session tests and relevant E2E coverage.

### Repositories not expected to change for this feature

- `gimesha-adikari/pdfnest-worker`
- `gimesha-adikari/platen-document`

Do not change them merely to propagate the billing mode.

## Review focus

These failure modes must be explicitly tested because they are easy to miss:

1. Free guest requests must not require the billing quota Redis store solely for monetization.
2. A real guest Redis reservation created before a normal→free switch must still settle after the switch.
3. A free async task completing after free→normal must remain unbilled.
4. Existing subscribers must still be able to manage/cancel subscriptions while new checkout creation is disabled.
5. Capacity/security `429/401/403` responses must remain truthful and must not be treated as monetization errors just because processing is free.

---

## Task 1 — Add the backend billing operating mode

**Linear:** GIM-5 — https://linear.app/gimesha/issue/GIM-5/add-backend-billing-operating-mode

**Files:**
- Create: `config/billing_mode.go`
- Modify: `config/runtime.go`
- Modify: `main.go`
- Modify: `.env.example`
- Test: `config/billing_mode_test.go`

**Produces:**
- A small immutable/config-derived policy API for `normal` vs `free`.
- Helpers used by later billing/session/checkout tasks.

- [ ] Write tests for missing mode -> normal, explicit normal, explicit free, and invalid values.
- [ ] Run the focused config tests and confirm the invalid-value test fails before implementation.
- [ ] Implement the minimal parser/policy helper.
- [ ] Ensure validation executes in development/test as well as managed environments.
- [ ] Ensure environment loading happens before mode parsing is relied on.
- [ ] Document `BILLING_MODE=normal` in `.env.example`.
- [ ] Run focused config tests and the existing runtime-config tests.
- [ ] Commit only the mode/config documentation changes.

## Task 2 — Implement free billing allocations

**Linear:** GIM-6 — https://linear.app/gimesha/issue/GIM-6/implement-free-billing-allocations

**Files:**
- Modify: `internal/billing/service.go`
- Modify: `internal/billing/guest_quota.go`
- Modify: `internal/billing/middleware.go`
- Test: `internal/billing/free_mode_test.go`
- Test/update: `internal/billing/middleware_test.go`
- Test/update: `internal/billing/guest_async_test.go`

**Consumes:** billing mode/policy from Task 1.

**Produces:**
- Free account and guest allocations with empty IDs and zero allocated units.
- No new billing reservation/quota writes in free mode.

- [ ] Write an authenticated test with exhausted plan windows and zero credits; assert free allocation succeeds and subscription/credits do not change.
- [ ] Write a guest test that proves free allocation succeeds without creating guest billing quota keys.
- [ ] Write a guest middleware test where the billing quota store is unavailable; assert free mode does not fail on the billing-only prerequisite.
- [ ] Run the focused tests and confirm failures before implementation.
- [ ] Add the free short-circuit at the allocation boundary before database/Redis monetization work.
- [ ] Preserve non-nil allocation objects and downstream billing locals.
- [ ] Make guest `Commit`/`Release` explicitly no-op for empty IDs if needed for direct callers.
- [ ] Do not disable real-ID settlement based on current mode.
- [ ] Run billing service, middleware, and guest async tests in both modes.
- [ ] Commit this allocation layer separately.

## Task 3 — Harden async transitions and reservation-kind compatibility

**Linear:** GIM-7 — https://linear.app/gimesha/issue/GIM-7/harden-async-billing-mode-transitions

**Files:**
- Modify only where tests prove needed: `internal/billing/service.go`, `internal/billing/init.go`, `internal/tasks/tasks.go`, task/download paths
- Test: `internal/billing/mode_transition_test.go`
- Test/update: `internal/tasks/reservation_kind_test.go`
- Test/update: `internal/tasks/download_controller_test.go`
- Test/update: `internal/ocr/guest_async_integration_test.go`

**Consumes:** empty-ID free allocations from Task 2.

**Produces:**
- Deterministic normal→free and free→normal behavior for in-flight jobs.

- [ ] Test a real account reservation created in normal mode and committed after switching to free.
- [ ] Test a real guest reservation created in normal mode and committed/released after switching to free.
- [ ] Test an empty-ID free reservation completing after switching back to normal.
- [ ] Exercise cancel, failure, stale recovery, replay, and download finalization.
- [ ] Preserve reservation kind for all new task flows that defer settlement.
- [ ] If legacy missing-kind real IDs need recovery, add a narrow backend resolver that distinguishes "not found" from storage failure.
- [ ] Do not invent a persistent `free` reservation kind unless the empty-ID model proves insufficient.
- [ ] Run task/billing/async integration tests.
- [ ] Commit transition compatibility separately.

## Task 4 — Expose effective policy through the session API

**Linear:** GIM-8 — https://linear.app/gimesha/issue/GIM-8/expose-effective-billing-policy-in-session

**Backend files:**
- Modify: `internal/auth/session.go`
- Test: `internal/auth/session_test.go` or the existing session test location

**Frontend files:**
- Modify: `context/AuthContext.tsx`
- Test: focused AuthContext/billing policy unit test

**Produces:**
- A backend-derived effective billing policy available to guests and users.

- [ ] Define a safe policy projection containing at minimum the effective mode, whether processing-unit limits are enforced, and whether new purchases are enabled.
- [ ] Keep the stored `subscription` object unchanged and truthful.
- [ ] Add guest and user session tests.
- [ ] Update `AuthContext` to hold effective policy separately from `subscription.tier`.
- [ ] Do not introduce an independent public env authority.
- [ ] Verify unknown/unavailable session policy fails safely for purchase UI.
- [ ] Commit backend session contract and frontend context together only if required by the contract change; otherwise keep reviewable commits.

## Task 5 — Disable new Paddle purchases in free mode

**Linear:** GIM-9 — https://linear.app/gimesha/issue/GIM-9/disable-new-paddle-purchases-in-free-mode

**Files:**
- Modify: `internal/billing/paddle_checkout.go`
- Test: `internal/billing/paddle_checkout_test.go`
- Update webhook tests only to prove existing synchronization remains active.

**Consumes:** billing mode/policy from Task 1.

**Produces:**
- No new subscription or credit checkout while processing is free.

- [ ] Write tests asserting free mode rejects subscription checkout before subscription-row preparation.
- [ ] Write tests asserting free mode rejects credit checkout before external Paddle transaction creation.
- [ ] Verify normal mode preserves existing checkout behavior.
- [ ] Verify webhook processing remains active in both modes.
- [ ] Verify existing portal/customer-management paths are not disabled.
- [ ] Commit checkout guards separately.

## Task 6 — Apply effective free-mode capability and frontend presentation

**Linear:** GIM-10 — https://linear.app/gimesha/issue/GIM-10/update-frontend-for-free-operating-mode

**Backend file:**
- Modify: `internal/structure/controller.go` for the direct paid-tier duplication entitlement.

**Frontend files:**
- Modify the billing presentation files listed in the file responsibility map.
- Add/update focused unit tests and `tests/e2e/free-mode.spec.ts`.

**Consumes:** effective policy from Task 4.

**Produces:**
- Free-mode UI without deleting normal paid-mode UI.

- [ ] Add a backend test proving duplication entitlement no longer depends on paid tier in free mode while finite technical bounds remain.
- [ ] Hide/replace upgrade and Buy Credits actions when purchases are disabled.
- [ ] Keep `/pricing` and `/subscribe` routes but render the free operating state.
- [ ] Remove quota exhaustion meters from effective capacity presentation without fabricating huge limits.
- [ ] Preserve actual billing history/tier details where useful for account management.
- [ ] Keep existing subscriber management/cancellation controls.
- [ ] Prevent ordinary free navigation and `_ptxn` handling from opening new checkout when purchases are disabled.
- [ ] Update login/register and processing-mode copy that currently implies paid capacity.
- [ ] Ensure capacity, authentication, authorization, validation, and transport errors remain visible and correctly classified.
- [ ] Verify normal-mode UI restores the paid presentation.

## Task 7 — Fix the existing PDF-to-Markdown double billing defect separately

**Linear:** GIM-11 — https://linear.app/gimesha/issue/GIM-11/fix-pdf-to-markdown-double-billing-defect

This is an existing normal-mode defect, not the free-mode mechanism. Keep it as a separate change so reviewers can reason about baseline repair independently.

**Files:**
- Modify: `internal/conversion/routes.go` and/or the authoritative async billing path
- Test/update: `internal/conversion/pdfToMarkdown_billing_test.go`
- Add routed integration coverage

- [ ] Reproduce the double-allocation risk through the routed `/conversion/pdf-to-markdown` path.
- [ ] Choose exactly one authoritative reservation/allocation boundary.
- [ ] Preserve idempotency and guest/account ownership.
- [ ] Test both aliases and success/failure/download settlement.
- [ ] Commit this defect repair independently from Tasks 1-6.

## Task 8 — End-to-end verification and rollout rehearsal

**Linear:** GIM-12 — https://linear.app/gimesha/issue/GIM-12/end-to-end-verify-free-and-normal-modes

**Produces:** release evidence, not new architecture.

- [ ] Run guest sync and authenticated sync in free mode with exhausted billing quotas.
- [ ] Run guest async and authenticated async in free mode with exhausted billing quotas and zero credits.
- [ ] Prove no new billing reservations/quota allocations are created for free operations.
- [ ] Prove subscription IDs, tiers, Paddle IDs, and purchased credits remain unchanged by free processing.
- [ ] Prove resource concurrency, page/upload/format limits, ownership, authentication, and storage authorization still reject invalid/unsafe requests.
- [ ] Prove failure, cancellation, stale recovery, replay, and download cleanup paths.
- [ ] Rehearse normal→free with real work in flight.
- [ ] Rehearse free→normal with free work in flight.
- [ ] Verify new checkout is disabled while webhook synchronization and existing subscriber management remain active.
- [ ] Verify frontend pricing/dashboard/Paddle behavior in both modes.
- [ ] Diagnose the previously observed live `failed to finalize billing` OCR failure before production rollout.
- [ ] Verify every backend replica reports/uses the intended mode during rollout.
- [ ] Attach final test evidence to GIM-12.

## Explicitly out of scope

Do not use this feature to redesign these concerns:

- Completely anonymous Studio.
- Anonymous access to routes whose login requirement is for ownership/security.
- Removing the protected R2 presign requirement.
- Reworking OCR V2/editor billing from admission-time charging to completion-time charging unless approved as a separate billing-lifecycle project.
- Replacing Paddle.
- Replacing Redis/PostgreSQL.
- Changing worker concurrency/resource limits.
- Refactoring unrelated billing architecture for style alone.

## Existing risks to keep visible

- `/conversion/pdf-to-markdown` currently has a double-reservation risk and is tracked separately as GIM-11.
- Some legacy async task flows can lose reservation kind.
- Some async routes currently settle at successful `202 Accepted`, not eventual worker completion.
- Guest support is not uniform across every workflow.
- The billing status projection currently duplicates tier-limit numbers that differ from enforcement; do not use that duplicate table as the free-mode authority.
- Rolling deployments can temporarily run mixed modes across replicas; rollout must be coordinated.
- Free mode removes monetization quota friction, so resource/abuse controls must remain independently enforced.

## Final acceptance criteria

The feature is ready only when all of the following are true:

- `BILLING_MODE=normal` preserves intended existing paid enforcement.
- `BILLING_MODE=free` allows supported guest/account processing without plan quota or purchased credits.
- Free operations create no new billing reservation or guest billing quota allocation.
- Existing real reservations settle across normal→free.
- Free empty-ID work remains unbilled across free→normal.
- Purchased credits and stored subscription data are not mutated by free processing.
- New checkout creation is disabled in free mode.
- Existing subscriber management and Paddle webhooks remain functional.
- Frontend policy comes from the backend and does not fake user tiers.
- Authentication, ownership, storage authorization, validation, capacity, timeouts, cancellation, idempotency, cleanup, and worker security remain active.
- No database migration, user-tier rewrite, artificial credit grant, or Redis flush is required.
- Worker and SDK remain unchanged for the operating mode unless a separately justified compatibility fix is required.
- GIM-11 is either fixed before rollout or explicitly blocked with a documented reason.
- GIM-12 verification evidence is complete before production rollout.

## Recommended execution order

1. GIM-5 — backend mode/config.
2. GIM-6 — free allocation semantics.
3. GIM-7 — transition/task compatibility.
4. GIM-8 — backend policy projection + frontend context.
5. GIM-9 — Paddle checkout guard.
6. GIM-10 — frontend and direct capability presentation.
7. GIM-11 — existing PDF-to-Markdown defect as a separate reviewed change.
8. GIM-12 — end-to-end verification and rollout rehearsal.

Do not start the next phase merely because the previous code compiles; each phase should have its focused tests passing and be reviewable on its own.
