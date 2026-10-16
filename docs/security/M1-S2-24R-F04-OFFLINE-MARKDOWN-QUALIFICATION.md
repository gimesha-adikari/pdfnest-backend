# M1-S2-24R - F-04 offline Markdown renderer qualification

## Disposition and source

Bounded source correction and local qualification for a DRAFT PR. This is not
production acceptance, merge authorization, or a claim of OS-sandboxed Chromium.
Independent source and security review is required before merge or deployment.

- Repository: `gimesha-adikari/pdfnest-backend`.
- Verified starting remote master: `406ddaa2d0bcfb3fd8b701524bd8165ec322191e`.
- Implementation branch: `milestone-1/f04-offline-markdown-renderer`.
- Original checkout retained on `milestone-1/managed-migration-historical-ledger-compatibility`,
  HEAD `aed47301b58b6fd4bc240588481fbd907171674f`, with no source edits there.
- Both starting commits have tree `bababf13a94cf290af7f2790cdd9ca01df72e53e`.
- Accepted investigation: [M1-S2-24 checkpoint](https://github.com/gimesha-adikari/platen-docs/blob/186bdebf04af007032373d65c7795672f4b6a9f5/ai-handoffs/platen-antigravity-transition-2026-10-15/30-M1-S2-24-F04-CHROMIUM-NETWORK-AND-FILE-ISOLATION-INVESTIGATION.md).
- Owner selected strict offline rendering, deliberately breaking remote/local-path image compatibility.
- Go 1.26.4, Google Chrome 155.0.8059.39, Goldmark 1.8.2, chromedp 0.15.1.
- Graph tools were unavailable; verification used direct source and installed dependency reads.

## Root cause and enforced contract

Previously, default Goldmark generated image elements from untrusted Markdown,
then Chromium opened a temporary `file://` document without destination controls.
Both network URLs and filesystem-relative image paths could become server-side
resource loads. Controlled prior tests demonstrated loopback requests and image
embedding; no production reachability or credential disclosure was established.

The shared `MarkdownToPdf` method now accepts only validated embedded PNG/JPEG
images. It rejects other Markdown image destinations with `ErrMarkdownPolicy`,
including HTTP(S), scheme-relative, relative/parent/absolute filesystem paths,
file URLs, unsupported schemes, SVG and malformed or over-budget data images.

Goldmark's own URL normalization is applied to resolved AST image destinations;
inline, full-reference, collapsed-reference and shortcut-reference forms share
that boundary. Allowed images are canonicalized. The complete generated HTML
is independently parsed and checked against a small element/attribute allowlist.
No unsafe Goldmark HTML or attribute extension is enabled. Raw HTML constructs
are rejected explicitly; harmless comments retain the existing omission behavior.
Unresolved or malformed syntax that Goldmark renders as literal text is not a
resource request. Clickable links remain annotations and are not navigated.

## Browser enforcement and lifecycle

- No intermediate HTML file and no file-origin document.
- Fresh `about:blank` target, followed by `Page.setDocumentContent`.
- Browser controls are installed before document insertion: request interception,
  request/navigation observation, script execution disabled, service workers and
  cache bypassed, downloads denied, and CSP violation observation through Audits.
- CSP denies scripts, connections, fonts, objects, frames, workers, media, base
  URLs and forms. Images are data-only; only the exact trusted stylesheet hash
  is permitted. Actual image URLs must also match the validated image map.
- A separate nonforwarding proxy rejects every HTTP/CONNECT request without
  DNS resolution or an upstream dial. It does not reuse or weaken HTML-to-PDF's
  public-destination proxy.
- Chromium has a single proxy with no DIRECT fallback, `<-loopback>` bypass
  removal, resolver denial except the proxy address, QUIC disabled, and the
  existing nonproxied-WebRTC restriction.
- Forbidden browser requests, navigation, WebSocket events, CSP violations,
  failed image decoding, or observed workspace violations cancel the render;
  they cannot become a successful PDF with missing resources.
- The hardened process allocator and process-group termination remain in use.
- Chrome's profile and auxiliary temporary files are placed in one private
  workspace and removed after process termination. Partial output is removed
  on every error after output creation. Successful output retains the existing
  caller-owned removal/storage flow and uses mode 0600.

## Engineering resource budgets

These are explicit implementation thresholds qualified with synthetic fixtures;
production capacity and product acceptance of the thresholds remain review gates.
They must not be described as previously approved production defaults.

| Resource | Threshold / enforcement |
|---|---|
| Markdown | 2 MiB; stat plus a bounded read before parsing |
| Async source copy | Size preflight before copy/task reservation; renderer repeats validation |
| Images | 32 occurrences, including repeated images |
| Base64 payload | 1 MiB per image; strict alphabet/size checks before allocation |
| Decoded image file | 768 KiB per image, including canonical PNG output |
| Image dimensions | 4096 per axis and 4,000,000 pixels per image; checked before full decode |
| Aggregate pixels | 16,000,000, counting repeated occurrences |
| Generated HTML | 16 MiB; bounded writer and final document check |
| Published PDF | 32 MiB; 64 KiB CDP stream reads, checked before each output write |
| Operation context | 35 seconds from renderer entry, inherited by browser/process |
| Browser workspace | Observed 64 MiB logical named regular-file threshold and 4096 entries; phase checks plus 100 ms monitor |

PNG is fully decoded and re-encoded to a static image, removing metadata,
animation chunks and trailing bytes. JPEG is fully decoded for validation but
retains its validated original bytes and EXIF/color metadata; this avoids lossy
recompression and the tested EXIF orientation regression. MIME labels alone
never authorize an image. Pixel counts bound codec input, not total process heap.

The general 100 MiB HTTP multipart/upload-staging limit remains unchanged. The
Markdown source cap applies before renderer reads and before the async extra
copy. No application HTML temporary file is created; application PDF output is
bounded. Chrome's internal temporary/spool allocations are subject to observation
and cancellation, not an OS disk quota. Transient peaks and open/unlinked files
are not certified by the directory monitor. There is no absolute OS disk or
memory cap. Context deadlines cannot preempt synchronous Goldmark/codec calls;
inputs are bounded and cancellation is checked around these operations. These
limits must remain explicit in any acceptance claim.

## Rendering compatibility

Preserved and checked: headings, paragraphs, emphasis, lists, blockquotes,
clickable PDF links, escaped code, valid PNG/JPEG images, EXIF-oriented JPEG,
A4/letter/legal paper sizes, margins and existing portrait semantics. Both routes
still call the same renderer. Async continues its existing top/bottom-only option
parsing; no generic task or print-option expansion was made.

The existing dark palette, typography and rounded code panels are retained.
Prism CSS/JS and the autoloader are removed. Code is static escaped monospace text;
automatic syntax token coloring is intentionally absent, using the task's
explicitly permitted static-code option. No highlighting package, grammar set,
asset download or dependency was introduced. Default Goldmark did not enable GFM
tables; pipe-table syntax continues to render as ordinary text, rather than adding
a table extension under this security change.

Remote images and filesystem image paths now fail explicitly. SVG/GIF/WebP and
noncanonical data image forms are unsupported. Raw HTML that previously vanished
silently now fails, except harmless comments. PNG animation/metadata is removed.
Budgets can reject documents that previously consumed unbounded resources.

Synthetic A4/letter/legal PDFs were checked using `pdftotext`, `pdfimages -list`,
`pdfinfo` and PDF annotation bytes. Image dimensions are present as actual PDF
objects. Heading coordinates move by the expected 72 points for a one-inch margin
change. The A4 first page was rendered with Poppler and visually inspected for
legibility, code styling, image presence and clipping. This is a representative
fixture check, not a complete historical visual baseline.

## Verification actually executed

Commands used cached dependencies only: `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local`.
The final recorded runner inherited only PATH/HOME/locale values; production DB,
storage, API credentials/endpoints and live-test switches were not inherited.
Redis fixtures used miniredis; HTTP/HTTPS/IPv6 listeners were task-owned.

| Command | Recorded result |
|---|---|
| `go test -json -count=1 -timeout=180s ./internal/conversion ./internal/netguard ./internal/process` | PASS: 64 top-level tests, 109 subtests, 4 skips |
| `go test -race -json -count=1 -timeout=180s ./internal/conversion ./internal/netguard ./internal/process` | PASS: same 64 top-level tests and 109 subtests, 4 skips; no race report |
| `go vet ./internal/conversion ./internal/netguard ./internal/process` | PASS |
| `go build ./...` | PASS |
| `gofmt` and `git diff --check` | PASS |
| `go test -json -count=1 -timeout=180s ./...` during implementation | FAIL / environment-limited: unchanged Studio packages require PostgreSQL at localhost:5432 |

The final relevant/race runs cover later JPEG, workspace and comment corrections;
the broader suite was not rerun to repeat the unavailable PostgreSQL operations.
The full attempt recorded 420 passing top-level tests and 300 passing subtests,
94 failing top-level tests in `internal/studio` and `internal/studio/models`, and
181 skipped tests. Those two source scopes have no diff from the verified base.
A clean baseline of conversion/netguard/process passed before implementation.
No PostgreSQL instance was provisioned and no production DB was accessed.

The four relevant-suite skips are:

- `TestHTMLPDFPublicNetworkIntegration`: explicit public HTTPS opt-in not enabled.
- `TestPDFToMarkdownRouteAliasesCreateOneAccountReservation`: isolated PostgreSQL DSN unavailable.
- `TestPDFToMarkdownAccountWorkerFailureReleasesReservation`: same unavailable DSN.
- `TestPDFToMarkdownAccountCancellationReleasesReservation`: same unavailable DSN.

New focused coverage includes destination/reference/encoding rejection, actual
PNG/JPEG PDF objects, EXIF rotation, image bytes/count/dimension/aggregate limits,
raw HTML and final HTML resource rejection, direct HTTP(S)/IPv4/IPv6/file/data/FTP
navigation, WS/WSS/fetch rejection, redirect targets with zero requests, independent
proxy/bypass testing with interception intentionally allowing the loopback image,
HTTP/CONNECT deny behavior, PDF byte/short-write limits, cancellation/process/proxy
cleanup, workspace breach/missing-workspace rejection and failed-output removal.
Private/link-local/metadata-style strings are parser/classifier inputs only; no
real metadata or private service is contacted. In strict offline mode even the
first redirect-origin request is forbidden; no real public-origin probe is needed.

The original negative test failed before the fix against a nonnetworking fake
browser. Additional red/green checks caught route error mapping, bounded-string
writer bypass, EXIF orientation, workspace overflow and missing-workspace handling.
Early browser development failures were corrected for chromedp string flag values,
blank-frame initialization, auxiliary socket cleanup and asynchronous CSP outcomes.
One timed-out development harness left a task-owned Chrome group; it was identified
by its unique profile and terminated. Final lifecycle tests pass without leftover
files/processes in their task-owned workspaces. No personal Chrome was used.

## Synchronous / asynchronous failure proof

Real registered routes, the real renderer and task registry with task-owned
miniredis are exercised. Sync policy rejection returns HTTP 400 and
`MARKDOWN_RESOURCE_FORBIDDEN`, with no PDF. Async image rejection returns the
existing queued-task response, then reaches FAILED with the same policy reason,
empty result key/URL, removed copied input and no successful artifact. Guest
pending and used units both remain zero after failure. Oversized async input
returns HTTP 400 before copying or task reservation. No storage service is needed
or reached for these policy failures. PostgreSQL account settlement is not claimed
as dynamically tested; its policy and the generic async contracts are unchanged.

## Review boundaries and residual risks

A fresh read-only pre-patch investigator traced the source/callers and a separate
fresh read-only reviewer examined the candidate for concrete bypass/regression
routes. No surviving F-04 bypass was identified in that cycle. The review noted
cooperative CPU deadlines and internal spool limits; subsequent parent changes
added monitored workspace enforcement and harmless-comment compatibility, followed
by renewed relevant/race/vet checks. This does not replace independent engineering
source/security review of the final PR before merge.

`NoSandbox` remains. Browser policy is not a network namespace, filesystem sandbox,
seccomp boundary, hard disk quota or protection against a Chromium exploit. No
packet capture, production Chromium test, production egress probe or production
reachability assertion is made. Deployed runtime/version remains unverified.

HTML-to-PDF, netguard, process helper, Code-to-PDF/F-05, module dependencies,
billing/subscriptions/credit policy, authentication, OCR and R2 semantics have no
source changes. Existing async cancellation/upload/publication concerns remain
separate. No commit in this task is authorized for merge or deployment.

## Broader-suite failing top-level tests

These names are preserved to avoid presenting the broader suite as passing.
Nested failures and all skips are also retained in the JSONL evidence.

### `pdfnest-backend/internal/studio`

```text
TestBenchmark_ModeB_WarmCacheVsColdCache
TestBenchmark_ModeC_SingleflightScaling
TestBenchmark_ModeE_MultiDocumentConcurrency
TestController_ExecuteCommandRejectsCallerSuppliedVDM
TestController_CreateSessionFromUpload_DerivesRealPDFState
TestController_CreateSecondaryAsset_IsOwnedWithoutCreatingVersion
TestController_CompleteLifecycle
TestController_VersionDAGBranchingAndLineage
TestController_ErrorContracts
TestController_IdempotencyReplay
TestController_TilePreview_Comprehensive
TestStudioFinalizerMaterializesSourcePersistedInLocalModeWithR2Environment
TestStudioFinalizerMaterializeVersionByIDReusesVerifiedSnapshot
TestStudioFinalizerMaterializeVersionByIDFallsBackForInvalidSnapshots
TestStudioFinalizer_MaterializesAuthoritativeVDMAndReusesSnapshot
TestStudioFinalizer_UsesActiveUndoRedoStateAndEnforcesOwnership
TestStudioFinalizer_FailureLeavesActiveVersionAndNoExportObject
TestStudioEditorDurability_PostgreSQL
TestStudioCompileReconcilesWithoutClientPolling_PostgreSQL
TestStudioCompileGetAndBackgroundReconciliationCreateOneVersion_PostgreSQL
TestStudioCompilePreparationDoesNotHoldJobLock_PostgreSQL
TestStudioCompileGetAndBackgroundPrepareConcurrentlyWithoutDuplicateFinalization_PostgreSQL
TestStudioCompileDownloadFailureLeavesJobRetryable_PostgreSQL
TestStudioCompileClientCancellationDuringPreparationCanRecoverInBackground_PostgreSQL
TestStudioCompileFinalizationFailureCleansPreparedObjectAndCanRetry_PostgreSQL
TestStudioCompileStaleActiveVersionRegistersDetachedResult_PostgreSQL
TestStudioJobReconcilerPersistsWorkerFailureAndCancellation_PostgreSQL
TestMarkupMaterializedVDMPreservesPageIDs
TestStudioMaterializationCoordinator_CompressesCurrentVDMAndReusesSnapshot
TestStudioMaterializationCoordinator_DerivesSplitVDMAndRejectsStaleResult
TestStudioMaterializationCoordinator_CoversRepresentativeProcessorDispatchAndOwnership
TestStudioMaterializationCoordinator_RejectsMergeAssetOwnershipAndUnknownIDs
TestStudioMaterializationCoordinator_RejectsInvalidProcessorOutputAndCleansObject
TestStudioMaterializationCoordinator_RejectsActiveVersionChangeAfterProcessing
TestOperationCoordinator_RotateDerivesVersionAndPreservesHistory
TestOperationCoordinator_DeleteValidationAndDerivation
TestOperationCoordinator_ReorderRequiresCompleteStablePageIDs
TestOperationCoordinator_DuplicateCreatesUniqueLineageAndPreservesDescriptor
TestOperationCoordinator_InsertBlankDerivesAdjacentDimensions
TestOperationCoordinator_CropDerivesAuthoritativeBoxAndSupportsUndoRedo
TestOperationCoordinator_CropRejectsInvalidGeometry
TestOperationCoordinator_SignatureOverlayOwnsAssetAndPreservesIDOnUpdate
TestOperationCoordinator_UpdateMetadataDerivesStateAndSupportsReloadUndoRedo
TestOperationCoordinator_AddTextOverlayDerivesServerIdentityAndSupportsDAG
TestOperationCoordinator_UpdateTextOverlayPreservesIDAndRejectsWrongTarget
TestOperationCoordinator_AddTextOverlayRejectsInvalidAndUnauthorizedInput
TestOperationCoordinator_AddWatermarkDerivesAllPageOverlaysAndDeletesOnlyTargets
TestOperationCoordinator_UpdateMetadataRejectsUnknownInvalidStaleAndUnauthorized
TestOperationCoordinator_PageNumberingUsesDocumentRuleAndSupportsDisableUndoRedo
TestOperationCoordinator_PageNumberingFollowsDeleteDuplicateAndBlankSequence
TestOperationCoordinator_PageNumberingRejectsUnsupportedSettings
TestOperationCoordinator_RejectsUnknownMalformedStaleAndUnauthorized
TestOperationCoordinator_ConcurrentCommandsSerializeOnBaseVersion
TestStudioFinalizer_FlattensFoundationTextOverlayIntoPDF
TestStudioMaterializationCoordinator_CompressPreservesFlattenedOverlayAndRedo
TestStudioFinalizer_FlattensPageNumbersAndMaterializationClearsDeferredRule
TestStudioTilePreview_RendersDeferredOverlayAcrossCropRotationUndoRedo
TestStress_IdenticalConcurrentTilesSingleflight
TestStress_DifferentSubTilesConcurrentLoad
TestStress_MultiPageAndMultiDocumentConcurrentLoad
TestStress_WorkerSaturationAndQueueFull429Propagation
TestStress_CorruptPDFHandlingAndErrorMetrics
TestStress_ScaleProgression_1To8
TestStress_MetricsEndpointObservability
TestStress_HugePageClipSafetyAndMaxTileDimension
TestRealRendering_EndToEndWorkerRenderer
TestRealRendering_EndToEndWorkerRenderer_SubTilesAndIntegratedRotation
TestRealRendering_NoSilentSyntheticFallbackOnWorkerFailure
TestRealRendering_SingleflightStrictProof
TestRealRendering_CoordinateAndScaleValidations
TestService_CreateDocumentAndGetSession
TestService_ApplyOperation_SuccessAndIdempotency
TestService_ConcurrentIdempotencyRace
TestService_UndoRedoAndBranchCheckout
TestService_RegisterSnapshotAndExport
TestService_DeleteSessionRequiresOwnerAndRemovesWorkspace
TestRepository_DeleteSessionWorkspaceRemovesAssociatedRows
TestStorageCleanupFailurePersistsAndRetrySurvivesWorkerRecreation
TestStorageCleanupAlreadyMissingObjectResolves
TestStorageCleanupRetentionLockIsDeferredWithoutHotRetry
TestStorageCleanupPermissionAndNetworkFailuresKeepNormalBackoff
TestCreateSessionRejectsForeignStoredSource
TestApplyOperationRejectsForeignAssetWithoutAdvancingVersion
TestStudioPersistenceUsesLocalStorageWhenR2EnvironmentIsPresent
TestStudio001_Qualification_SameSessionReuseAndCompiledVersionState
TestStudio001_Qualification_HistoricalExplicitEnglishPath
TestStudio001_Regression_ExtractionReusedOnReopenAndAfterCompile
```

### `pdfnest-backend/internal/studio/models`

```text
Test1_ActualDatabaseConstraintsAndIndexes
Test2_RealSessionLockBlockingBehavior
Test3_RealIdempotencyRaceExecution
Test4_TransactionRollbackAtomicity
Test5_PostgreSQLUniqueConstraints
Test6_PostgreSQLForeignKeyRejection
Test7_CyclicVersionSnapshotRelationshipAndDeletionSafety
```
