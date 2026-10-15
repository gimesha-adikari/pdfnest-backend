# M1-S2-08 — Failure and acceptance matrix

**DESIGN ONLY — NOT IMPLEMENTED.** Every test below is a future implementation acceptance specification, not a passing result. No database, Redis, OCR worker, fault-injection service or broad test suite was started to author this document.

Source base: `13a0e26e4f78e4d25aff244a1a7f1b3ba28192ee`. Read with the [architecture](m1-s2-08-ocr-finalization-design.md) and [release contract](m1-s2-08-finalization-rollout.md). The existing [M1-S2-07 qualification](m1-s2-07-ocr-settlement-qualification.md) is the historical execution record.

## 1. Test vocabulary and infrastructure

All states, barriers and pseudocode here are **DESIGN ONLY — NOT IMPLEMENTED**.

- `J`: authoritative SQL OCR row for `ocrd1_` tasks; `F`: private Redis journal for no-charge `ocrf1_` tasks; `P`: TTL Redis status projection. `R`: the exact linked database reservation. `O`: the predeclared output object.
- For normal billing, run each settlement case twice: plan allocation and custom credits. Add one mixed allocation case, and controlled window resets. Assert exact three-hour/daily/monthly counters, credit balance, reservation status and usage-log count against the initial account; no inference from HTTP alone.
- `PG`: disposable PostgreSQL exercising actual row locks, transactions, rollback and unique constraints. SQLite/mock repositories are insufficient for these concurrency claims.
- `Redis`: isolated real Redis for actual Lua, claim and lost-ack behavior. Miniredis can supplement ordinary unit cases only after script support is verified. It is not evidence for persistence or a real-process restart.
- `Store`: task-owned local storage plus a controlled object-store adapter with typed missing/outage/corruption errors and explicit PUT/GET/DELETE gates. At least one HTTP integration uses the actual storage adapter, with no production credentials.
- `HTTP`: actual controller, identity middleware, status, cancellation, WebSocket and download handlers; synthetic registered users/capabilities. Fake OCR processor generates bounded synthetic PDFs; this is publication integration, not full OCR algorithm evidence.
- `Restart`: child backend/reconciler process killed at a barrier; PostgreSQL/Redis/storage outlive it. Restart with clean process memory. No original goroutine or test closure may perform recovery.

Gate on recorded events/channels, committed SQL rows and controlled command hooks. Time advances use injected clocks or explicit due timestamps. Do not rely on sleeps to make races likely. A timeout bounds the test, not its synchronization proof. Production runtime hooks must not become public fault-injection endpoints.

## 2. Deterministic transition and failure cases

The following table contains **future tests**, with required results. All SQL cancellation/finalization cases assert authorization and linked reservation identity under the same decision lock.

| ID | Initial state and deterministic injection | Required authority / billing | Required artifact / projection / visible result | Infrastructure |
| --- | --- | --- | --- | --- |
| T01 | `J=ready`, `R=reserved`; pause before task lock, cancel and commit, then release finalizer | `J=cancelled`, R released; zero usage/credits | O becomes cleanup-eligible, never downloadable; P eventually cancelled | PG, Redis, Store, HTTP |
| T02a | Finalizer holds task lock; cancel blocks; finalizer commits | J succeeded, R committed exactly once; cancel returns authoritative completion | O retained and matches descriptor; no false cancellation/refund message | PG, real concurrent connections, HTTP |
| T02b | Cancellation holds task lock; finalizer blocks; cancellation commits | J cancelled, R released; finalizer cannot settle | No success download; worker/reconciler cannot restore success | PG, Redis, HTTP |
| T02c | Finalizer holds task lock then transaction rolls back while cancel waits | Rollback wins nothing; cancel may commit and release | No charge, no terminal success from an uncommitted transaction | PG |
| T03 | J succeeded; late authorized cancel | J and R unchanged; one usage log | Existing valid O downloadable; response COMPLETED, not refunded/cancelled | PG, Redis, HTTP |
| T04 | Fail SQL transaction after usage/credit update but before final J update | All billing effects and state roll back; J remains ready, R reserved | O retained; safe retry/no success until a committed decision | PG failure injection |
| T05 | SQL commit applied; connection drops before acknowledgment | Fresh read proves J succeeded and R committed once; never release | Keep O, retry projection; no rerun or cleanup on commit error alone | PG proxy/driver commit barrier, Restart |
| T06 | Redis completion fails before execution after SQL success | J succeeded, R committed once; no stale release | O retained; SQL status/download can work; P replay later, no second charge | PG, Redis command hook, HTTP |
| T07 | Redis Lua applies projection, reply lost | Same as T06; duplicate projection same revision is idempotent | O retained; no reproduced 410 caused by defer; exact PDF downloadable | PG, real Redis post-command barrier, Store |
| T08 | Redis unavailable through backend death/restart | J discoverable without Redis; R never recharged/released from absence | SQL ownership/status/download work when storage available; P rebuilt on Redis return before deadline | PG, Redis outage, Restart, HTTP |
| T09 | Worker dies after PUT, before ready update; expected descriptor already committed | Recovery claims expired processing record, verifies O, marks ready, settles once | Correct O retained and downloadable; no originating goroutine required | PG, Store PUT gate, Restart |
| T10 | Worker dies after SQL success before projection | Recovery finds succeeded due row; no billing operation | P reconstructed, O remains correct; one charge | PG, Redis, Restart |
| T11 | Worker dies before descriptor/PUT; processing lease expires | J failed, R released under lock; no new OCR processor for same ID | No artifact success; input/empty-output cleanup tracked | PG, Restart |
| T12 | Two finalizers start for same ready row | Exactly one transition/usage insertion; other observes same descriptor/decision | One immutable result, no duplicate conflicting key or cleanup | PG two connections + barrier |
| T13 | Concurrent processor claims; second proposes a different descriptor/key | Only one claim/writer; mismatch rejected, no changed binding | First O cannot be overwritten or deleted by loser | PG/Redis free adapter, Store |
| T14 | Prepared O is absent before settlement | After bounded retry/deadline, fail/release; no charge | Safe non-success, typed absence distinguished from outage | PG, Store |
| T15 | O bytes corrupt or wrong size/page count before ready | Never ready/succeeded, R released on authoritative failure | No success; cleanup tombstone and safe error | PG, Store, synthetic PDFs |
| T16 | Storage outage on readback | Do not treat outage as missing; J stays recoverable until deadline, R reserved | Retain intent/O; no false success/deletion | PG, Store outage adapter |
| T17 | O absent/corrupt after succeeded | Immutable successful decision and one charge preserved; integrity-health incident | No bytes served; safe 410 for confirmed removal/corruption, no refund claim; retain audit/alert | PG, Store, HTTP |
| T18 | Redis public key expires while J within download lifetime | SQL retains decision/owner/token; no recreation as a new task | Rebuild only remaining TTL; same PDF, no lifetime extension | PG, Redis clock, HTTP |
| T19 | Public deadline reached; Redis still says completed | Decision remains succeeded, billing unchanged | Download denied by authority, then physical cleanup after grace; no extension by polling | PG, Redis, Store clock |
| T20 | Cleanup selected concurrently with finalization | Eligible cleanup first prevents success; held ready/succeeded row cannot be deleted | No charged row points to application-deleted unexpired O | PG locks, Store DELETE barrier |
| T21 | Cleanup lease expires, stale cleaner later acknowledges | Only current token can update cleanup; immutable key unchanged | Duplicate DELETE safe; metadata remains until confirmed absence; no success revival | PG/Redis, Store |
| T22 | Cancel during bounded PUT; provider later accepts object | Cancelled/released remains terminal; never finalize late O | Tombstone/inventory discovers and deletes late O; no public result | PG/Redis, Store late-PUT barrier |
| T23 | Reservation janitor races with ready finalizer, or Redis is absent | Uses job lock/deadline; cannot independently release a live protocol reservation | Either fail/release with no charge, or succeed/commit with retained O; never released+success | PG, janitor actual path |
| T24 | Invoke legacy commit/release/download callbacks with new reservation | Typed coordinator-required outcome; no independent change | Old callback cannot override authoritative state | PG, billing/tasks integration |
| T25 | Linked reservation missing, released, wrong owner/task/kind/tool | Quarantine or fail safely before charge; never nil-as-success | No downloadable successful artifact for mismatched state | PG isolated invalid records |
| T26 | Commit two times; one retry occurs across a window reset | Usage/credit charge occurs once at original committed decision timestamp | Reconciliation never resets/deducts again | PG controlled billing clock |
| T27 | Creation SQL commit acknowledgment lost | Look up chosen task ID; do not reserve/insert again for same acceptance operation | No processor without confirmed owned initial projection; exact one R/J | PG, Redis, HTTP |
| T28 | Initial Redis registration fails or acknowledgment lost | No processor starts; coordinator fails/releases or resolves uncertain SQL outcome | Eventually P reflects failure, no artifact success; inputs tracked for cleanup | PG, Redis fault hook, HTTP |
| T29 | Database unavailable during status/cancel/finalize | No fall back to Redis authority for `ocrd1_`; no claimed cancel/refund | 503 and retained artifact; recovered fresh decision resolves uncertainty | PG outage, HTTP |
| T30 | Two backend instances claim same due work | SKIP LOCKED/claim token serialize; lease expiry permits takeover | Stale updates fenced; no double charge or missed cleanup | PG concurrent reconcilers, Restart |
| T31 | Due queue exceeds a batch; one permanently failing row | Progress for other rows, capped retry rate; failures remain durable | Oldest-age/backlog alert; no purge to hide failure, capacity pause observable | PG/Redis, fake clock |
| T32 | DELETE denied by provider retention or storage outage | Cleanup remains pending, no false deletion acknowledgment | Backoff/escalation; no new upload beyond approved budget | Store adapter, coordinator |
| T33 | Inventory paginates, sees orphan/owned O; authority unavailable mid-page | Cursor safe for next cycle; never infer orphan on DB/Redis failure | Only confirmed expired unowned prefix objects removed; committed output protected | Store list adapter, PG/Redis |
| T34 | Local persistence crashes around rename/directory sync | Descriptor points only to durably accepted final path or recoverable failure | No success based solely on renamed-but-not-durable file; restart integrity check | Local filesystem process harness |
| T35 | At-rest encryption enabled | Digest/size refer to logical PDF, not ciphertext or ETag | Readback and download validate same logical bytes across encryption; no key leakage | Actual storage adapter, synthetic key |

## 3. Authorization, free mode and legacy cases

| ID | Scenario | Required result | Infrastructure |
| --- | --- | --- | --- |
| C01 | User A owns task; B requests status, WS, download or cancellation without capability | Denied; no binding/counter/decision mutation; no token/key/reservation/path disclosure | PG, Redis, real identity HTTP |
| C02 | B possesses exact issued capability; cancel uses documented header | Preserve permitted capability semantics; check against authoritative record. Wrong/blank/expired token denied; ownerless legacy status still requires exact token | HTTP, current authorization tests |
| C03 | Unknown/malformed new-protocol ID, missing authority row, or forged owner field in Redis | Safe unknown/unavailable outcome; no fallback to legacy registry, no owner adoption | PG/Redis, HTTP |
| C04 | Free mode, `config.DB=nil`, empty reservation ID | No SQL call/settlement callback. Atomic F decision, validated O and current result; duplicate success/cancel correctly ordered | Real Redis, local Store, unit/HTTP harness |
| C05 | Free completion applied but reply lost | Journal retains descriptor, output retained, discovery/replay succeeds | Redis post-command barrier, Restart |
| C06 | Free Redis outage across backend restart | No SQL fallback or guessed authority; resume due journal after Redis returns, no charge | Redis, Restart |
| C07 | Free public status TTL expires but journal/discovery remain | Reconstruct only inside deadline; cleanup survives status expiry | Redis clock, Store |
| C08 | Catastrophic loss of free journal/index | No fabricated ownership/success; safe unavailable/unknown; no charge; inventory quarantines then expires orphan output | Redis disposable flush confined to test process, Store |
| C09 | Real guest identity middleware reaches `/ocr/jobs` | Under approved user-only policy: 401 before reservation/processor. No quota use. Synthetic registered-user free request remains supported | Redis identity store, HTTP |
| C10 | Legacy guest quota producer and legacy database task | Existing reservation-kind dispatch, terminal immutability, token/download rules unchanged | Existing billing/tasks tests |
| C11 | Current mode changes between acceptance and replay | Stored protocol/kind controls; free never charged, SQL task never treated as free/guest | PG, Redis, configuration test |
| C12 | Legacy completed task / token, non-PDF TXT/MD/JSON download | Existing behavior unchanged; new OCR digest/parse policy not imposed on other consumers | Existing tasks/consumer tests |
| C13 | New task running during compatibility-only release; old/new request routing | New IDs not enabled before all writers/readers understand protocol; old binary cannot cancel/clean new output | Two-version staging harness, release routing |
| C14 | Attempt rollback to original Redis-only binary after durable success | Gate rejects unsafe rollback; compatibility reader/reconciler remains until lifecycle drained | Release automation test, no production |
| C15 | User/account deletion and expired capability | Privacy deletion preserves cleanup pointer; no orphan caused by cascade; no unauthorized result afterward | PG, identity/storage integration |

## 4. Convert characterization into safety regressions

Do not merely rename the current tests or change their meaning without an active implementation under test. Preserve the original source/commit as evidence. Future S2-07-derived assertions must become:

| Existing characterization | Replacement safety assertion |
| --- | --- |
| `cancel_before_commit` | Cancellation commits first: cancelled + released + zero usage; cleanup only from authoritative decision |
| `cancel_after_commit` | Pause **after the joint SQL success commit**, then cancel: completed + one committed charge + retained downloadable PDF. Also pause before that transaction and let cancel win: released + no charge |
| `redis_completion_failure` | SQL success retained; projection failure cannot release R or delete O; status/download succeed through authority or return a genuine dependency error, then replay repairs projection |
| `redis_completion_ack_lost` | SQL or free journal terminal decision retained, O exists and returns exact valid bytes; repeated projection does not charge or clean |
| `success_duplicate_and_late_cancel` / competing settlement | All side effects exactly once; finalization descriptor immutable; late cancellation returns completion without refund assertion |
| Free empty reservation | Still passes with DB nil; also exercise journal recovery and no billing call |
| Related guest quota / synthetic direct guest | Legacy quota behavior preserved; `/ocr/jobs` rejects guests only after policy approval and actual middleware coverage |

Faults must assert all three systems, not only callbacks: committed database rows and counters, raw authoritative Redis/journal/projection, and exact object contents/absence. Use two independent SQL connections and fresh reads after ambiguous transaction errors. Check task-owned input/output/temp cleanup separately.

## 5. Artifact and HTTP evidence

Generate small PDFs with known page counts and synthetic text markers. Independently run qpdf check, pdfinfo and text extraction on fixture and downloaded file. Require byte SHA-256 equality with the committed logical descriptor; this establishes association, not OCR quality. Password-protected or malformed PDF output must reject under the ordinary readable output contract. No plaintext/encrypted-output preservation policy is introduced.

Capture only task correlation, method, local route, status, safe state, digest equality and counts. Do not save request bodies, cookies, capabilities, storage keys, billing IDs or unredacted SQL logs. A successful status response alone is not artifact availability evidence. A mock OCR processor is explicitly labeled; do not claim actual OCR accuracy or production billing integration.

## 6. Future execution gates

**Command plan — DESIGN ONLY — NOT EXECUTED for this task:**

1. Provision explicitly disposable loopback PostgreSQL/Redis and task-owned local/object-store substitute. Verify all effective endpoints before using the opt-in harness. Never reuse the preserved S2-07 directory without authorization.
2. Reuse `PLATEN_M1_BILLING_TEST_DATABASE_URL` and the existing optional real-Redis qualification configuration after extending their guards. Migrations apply only to the disposable future harness.
3. Run targeted OCR finalization matrix cases for plan, credits, mixed allocation and free mode; run concurrency under the race detector, and the separate restart/HTTP harness. Record actual counts from execution, not the number of rows in this specification.
4. Run `go test ./internal/tasks/... ./internal/ocr/... ./internal/billing/...` and directly affected storage tests; run `go vet` on modified packages. Keep skipped external-dependency cases explicit. Known unrelated broad Studio test-environment failures are not fixed by these tests.
5. Run frontend task polling/cancel/unit and focused browser compatibility tests because protocol dispatch changes, despite unchanged public status names. Preserve F-01/F-02 guards and ordinary non-PDF results. Full-suite scope is determined by the eventual implementation diff.
6. Run mixed-version/rollback rehearsals before enabling acceptance. Tests must prove gates prevent original binaries seeing new tasks, not merely that both versions compile.

Acceptance requires every applicable row to pass or receive an independently approved contract change. Missing guest-policy approval, unsupported storage discovery/durability, unsafe cleanup, a new mandatory free-mode SQL call or a process-memory-only recovery path blocks implementation acceptance. The design itself does not close the runtime defect.
