# Original audit F-09 — Verification bearer log hardening

Qualified 2026-10-16 against backend `master`
`296838852195f96ca5590b3466a33ec5ae3e39d9`. Both local and advertised remote
master matched before edits. The clean checkout initially held OCR branch
`milestone-1/ocr-recovery-lease-clock-hardening` at
`5b01d3a04f53d45397a55f4e7bb3577263fd03a0`; that branch and draft PR #24 remain
separate and unchanged. This correction uses independent branch
`security/f09-verification-bearer-log-hardening`, directly from the verified
master, with a draft PR targeting `master`. No OCR or independent PR #11/#12
changes are included.

## Finding and narrow correction

Read the [original audit F-09](https://github.com/gimesha-adikari/platen-docs/blob/734f800998d93860f92cb457b81e6fa5d4b5602e/platen_findings/PLATEN_PDF_PROJECT_UNDERSTANDING_AND_UPGRADE_READINESS_REPORT.md)
and the [permanent engineering checkpoint](https://github.com/gimesha-adikari/platen-docs/blob/734f800998d93860f92cb457b81e6fa5d4b5602e/ai-handoffs/platen-antigravity-transition-2026-10-15/15-M1-S2-17-19-AND-AUDIT-PRIORITY.md).
The audit assigns medium security severity/high source confidence and P0
remediation priority. A log reader could exercise the email-verification
capability during its valid lifetime. This does not independently establish
account takeover or prove production compromise.

At the baseline, `internal/auth/controller.go:145` unconditionally executed
`log.Printf("[AUTH EMAIL] verify URL: %s", verifyURL)`. Registration and resend
both call `sendVerificationEmail`, so this shared sink disclosed the raw bearer
for both paths. The runtime correction deletes only that statement. Existing
preparation, template, success and failure reporting remains.

The actual link remains unchanged in email text, HTML and embedded template
data. Generation still uses 32 cryptographically random bytes encoded as hex;
only its SHA-256 hash is stored with the existing 30-minute expiry. No token,
verification/resend, API response, session, provider configuration, model,
migration, billing, Redis, OCR, storage or frontend behavior is changed.

## Reproduction and regression

Before deleting the statement, a direct-helper characterization used a synthetic
64-character hexadecimal token, captured/restored the standard logger, and
confirmed the raw token, complete URL and token query were logged. Sanitized
example only:

```text
[AUTH EMAIL] verify URL: [REDACTED VERIFICATION LINK]
```

The three cases cover custom frontend URL/trailing-slash normalization, default
frontend URL, and deterministic local outbox failure. Both successful cases
already proved exactly one correct-recipient/subject email with its link intact
in text and rendered HTML. The error case uses a regular file blocking the
outbox directory, confirms the original file survives, and checks error/success
reporting. All three historical cases passed. Negative no-disclosure assertions
then failed in all three cases against the unchanged vulnerable source, and
passed after deletion. Boolean assertions keep bearer values out of failure
diagnostics. No actual token or complete bearer URL is published here.

Tests set `LOCAL=true`, `APP_ENV=development`, a test-owned `MAILER_OUTBOX_DIR`
and empty provider credentials. The mailer returns through its local outbox
before provider configuration/client creation, including on filesystem error;
there is no provider fallback. Logger capture is nonparallel and restores its
previous writer after every case. Other logger configuration is untouched.

## Verification

The focused regression runs without a database. Existing verification/resend
tests use a fresh PostgreSQL **18.6** UTF-8 database `test_f09_utf8`, synthetic
role `f09_test`, explicitly verified at `127.0.0.1:5560`. The existing SQL helper
uses `DATABASE_URL` and AutoMigrate without its own loopback guard, so the test
command supplied only this independently validated disposable target; no
application environment file was sourced.

```sh
env -u DATABASE_URL RESEND_API_KEY='' FROM_EMAIL='' go test -p 1 ./internal/auth -run '^TestVerificationEmailDoesNotLogBearerURL$' -count=1 -timeout=2m -json
env -u DATABASE_URL RESEND_API_KEY='' FROM_EMAIL='' go test -race -p 1 ./internal/auth -run '^TestVerificationEmailDoesNotLogBearerURL$' -count=1 -timeout=3m -json
env DATABASE_URL='postgres://f09_test@127.0.0.1:5560/test_f09_utf8?sslmode=disable' RESEND_API_KEY='' FROM_EMAIL='' go test -p 1 ./internal/auth -run '^(TestEmailVerify_|TestResendVerification_AUTH004_)' -count=1 -timeout=3m -json
go vet ./internal/auth ./internal/mailer
gofmt -l internal/auth/controller.go internal/auth/verification_logging_test.go
git diff --check
```

Execution additionally set `TMPDIR` to prepared disposable security-test storage.

| Final check | Top-level passed | Leaf cases passed | Pass events including parents | Failed / skipped |
| --- | ---: | ---: | ---: | --- |
| Logging/delivery regression | 1 | 3 | 4 | 0 / 0 |
| Same regression, race detector | 1 | 3 | 4 | 0 / 0 |
| Existing verification/resend | 19 | 22 | 23 | 0 / 0 |

No race diagnostics. Vet, formatting and whitespace checks passed. Independent
read-only boundary investigation and one candidate review found no concrete
bypass/regression in the bounded change. Graph tools were unavailable; evidence
uses targeted source reads/searches without claiming graph coverage.

Existing tests preserve first verification, concurrent/idempotent same-token
success within expiry, expiry rejection including a previously successful token,
wrong-token isolation, resend replacement/old-token rejection, and generic
unknown/pending/verified-account responses. An already-used token is deliberately
accepted within its valid window; this task does not change that contract.

## Limits, publication and preservation

Shared mailer/provider errors are still logged verbatim and returned to the
verification helper; registration also logs delivery errors. A provider error
that reflected request content could potentially disclose sensitive data. The
reviewed source does not establish such reflection, and no external provider
request was made. Local filesystem-error no-disclosure is tested; provider-
specific errors are not qualified. The shared mailer is unchanged.

Only `controller.go`, `verification_logging_test.go` and this report are
committed. Publication is an independent draft PR from the stated branch to
master; its first new commit has direct parent
`296838852195f96ca5590b3466a33ec5ae3e39d9`. Final SHA, PR URL and returned CI
state are supplied with the publication report; CI is distinct from local proof.

User changes, independent branch refs, OCR PRs #13–#24, frontend `AGENTS.md` and
outer staging/docs/scratch are preserved. Task-owned PostgreSQL is stopped and
only its verified disposable data directory is removed before publication;
the protected historical M1-S2-07 directory was never opened or reused. Local
outboxes are removed by test cleanup. No production email, secrets, customer
data, R2, billing, merge or deployment was accessed/performed.

This source correction neither deletes historical logs nor revokes previously
disclosed tokens. Historical retention/access assessment requires separate
owner authorization. It does not qualify production historical-log cleanup,
global F-01/F-02 closure, or resolution of the live OCR billing/artifact issue.
