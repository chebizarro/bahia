# Verification Report — NOSTR_NATIVE_CONTEXTVM_MIGRATION

## Status
Complete. ContextVM/canonical Nostr migration streams A/B/C are complete and docs/PSTF have been finalized for the final runtime behavior.

## Evidence
- `bahia-dgju` CLOSED: CEP-4/NIP-59 random-key gift-wrap support for `1059`/`21059` around inner ContextVM `25910` was implemented in `internal/controlplane/encrypted_transport*`; focused transport tests passed.
- `bahia-f0uw` CLOSED: production runtime legacy kind reactor/subscriber support was removed; legacy custom kinds are isolated outside production runtime except startup migration/fixtures.
- `bahia-viys` CLOSED: web/CLI client cutover to ContextVM/canonical observables was completed.
- `web/src/lib/nostr/kinds.gen.js` exposes ContextVM (`25910`, `1059`, `21059`, `11316`-`11320`) and canonical observable constants (`30900`, `4903`, `30315`, `30316`, `30002`, `30078`), while labelling legacy ranges migration-only.
- Docs finalized: `docs/user-guide/nostr-integration.md`, `docs/control-planes.md`, `docs/user-guide/mcp-tools.md`, `docs/user-guide/cli-reference.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, `docs/protocol-compatibility.md`, `docs/operator-assistant-protocol.md`, and `AGENTS.md`.

## Tests run
- PASS (`bahia-dgju`): focused transport tests for CEP-4/NIP-59 random-key gift-wrap around inner ContextVM `25910`.
- PASS (`bahia-f0uw`): `go test ./internal/adapters/nostr ./internal/controlplane ./internal/kinds`.
- PASS (`bahia-viys`): `go test ./pkg/client ./cmd/cli` and targeted web unit tests.
- PASS (prior PSTF/client slice): `go test ./internal/nostrmigration ./internal/repository ./internal/app ./internal/controlplane ./internal/kinds ./internal/adapters/nostr ./internal/relaysidecar ./internal/mcp`.
- PASS (prior PSTF/client slice): `npm run test:unit -- --run tests/unit/encrypted-controlplane.test.js` — 13 tests passed.
- PASS (final integrated): `go test ./internal/nostrmigration ./internal/repository ./internal/app ./internal/controlplane ./internal/kinds ./internal/adapters/nostr ./internal/relaysidecar ./internal/mcp ./pkg/client ./cmd/cli`.
- PASS (final integrated): `npm run test:unit -- tests/unit/encrypted-controlplane.test.js tests/unit/public-controlplane.test.js tests/unit/dns-controlplane.test.js tests/unit/workers-actions.test.js tests/unit/nostr-client-parsing.test.js` — 5 files / 70 tests passed.
- PASS: static search for `5997|5998|5999|6000|6001|6002|6003` under `web/src/routes/workers` found no hardcoded worker action literals in touched paths.
- PASS: static search confirmed `web/src/lib/nostr/kinds.gen.js` exposes ContextVM/canonical constants.

## Remaining dependencies and gaps
None identified for `NOSTR_NATIVE_CONTEXTVM_MIGRATION` after the `bahia-prcf` encrypted-result semantics pass.

## Encrypted result terminal semantics pass — bahia-prcf — 2026-06-07

Observed behavior before this pass: `web/src/lib/nostr/encrypted-controlplane.js` still exposed `ENCRYPTED_RESULT_TIMEOUT_MS`, scheduled a `setTimeout` inside `awaitEncryptedResult(...)`, and the focused unit test asserted timeout-based terminal failure when no correlated encrypted result arrived.

Intended behavior: encrypted ContextVM result waiting terminates from correlated result `EVENT` success/error, explicit relay `CLOSED`/AUTH lifecycle failure, publish failure cleanup, or caller-provided operation cancellation. Historical `EOSE` is handled as subscription lifecycle metadata and does not become terminal completion for an open realtime result subscription.

Changes verified:
- `awaitEncryptedResult(...)` no longer uses timeout or `setTimeout` for terminal result completion.
- The subscription handler set includes `onEvent`, `onEose`, and `onClosed`; tests inject those callbacks directly, including relay-less and unknown-relay `CLOSED` paths.
- Assistant encrypted request callers now pass caller-provided `AbortSignal` cancellation instead of long `timeoutMs` values.
- Already-aborted operation signals are rejected before relay connect/publish/subscribe work begins.

Verification:
- PASS: `npm run test:unit -- --run tests/unit/encrypted-controlplane.test.js` — 1 file / 16 tests passed.
- PASS: focused static search for `timeout`, `setTimeout`, `ENCRYPTED_RESULT_TIMEOUT_MS`, and `Timed out waiting for ContextVM` in `web/src/lib/nostr/encrypted-controlplane.js`, `web/tests/unit/encrypted-controlplane.test.js`, and encrypted assistant call sites returned no matches.

## Legacy-kind fixer pass — bahia-6xxd

Closed fixer Beads:
- `bahia-6xxd.1`: backend runtime legacy kind cleanup.
- `bahia-6xxd.2`: web runtime legacy kind cleanup.
- `bahia-6xxd.3`: manifest alias/coverage repair.
- `bahia-6xxd.4`: docs rewrite.
- `bahia-6xxd`: fixer epic.

Final integrated verification after the fixer pass:
- PASS: `go test ./internal/nostrmigration ./internal/repository ./internal/app ./internal/controlplane ./internal/kinds ./internal/adapters/nostr ./internal/relaysidecar ./internal/mcp ./internal/service ./pkg/client ./cmd/cli`.
- PASS: `npm run test:unit -- --run tests/unit/llm-page.test.js tests/unit/controlplane-store.test.js tests/unit/fips-mesh-store.test.js tests/unit/dns-store-subscriptions.test.js tests/unit/dns-store-commands.test.js tests/unit/nostr-client-parsing.test.js tests/unit/encrypted-controlplane.test.js tests/unit/public-controlplane.test.js tests/unit/dns-controlplane.test.js tests/unit/workers-actions.test.js tests/unit/assistant/assistant-store.test.js` — 11 files / 106 tests passed.

## Relay sidecar NIP-23 draft allow-list pass — bahia-h1tz — 2026-06-11

Observed behavior before this pass: the relay sidecar readable/publishable canonical-kind policy allowed NIP-23 long-form content (`30023`) but not the NIP-23 draft companion kind (`30024`). Operators need the sidecar allow-list to include both standard NIP-23 long-form event kinds.

Intended behavior: NIP-23 `LongFormContent=30023` and `LongFormDraft=30024` are standard Nostr kinds consumed directly where Bahia publishes/reads long-form documents or drafts. The sidecar should allow service-signed publishes and readable filters for both, while preserving rejection of unauthorized publishers and legacy runtime kinds.

Changes verified:
- `internal/kinds.IsCanonicalObservableKind` now includes `LongFormDraft` beside `LongFormContent`.
- `internal/relaysidecar` tests prove both NIP-23 kinds are accepted from the service pubkey, rejected from unauthorized pubkeys, readable via kind-scoped filters, and queryable from the sidecar store.

Verification:
- PASS: `GOCACHE=/tmp/bahia-go-build-cache GOMODCACHE=/tmp/bahia-go-mod-cache go test ./internal/kinds ./internal/relaysidecar -count=1`.

## Migration manifest standard-kind omission pass — bahia-8j5h — 2026-06-11

Observed behavior before this pass: `go test ./... -count=1` failed in `internal/nostrmigration` because `internal/kinds.LongFormDraft=30024` was neither mapped in the migration manifest nor explicitly justified. The focused rerun also exposed the companion NIP-23 constant `LongFormContent=30023` as uncovered.

Intended behavior: the migration manifest covers every `internal/kinds` constant either with a legacy-to-canonical disposition or an explicit omission/alias justification. Standard NIP-23 long-form content and draft events (`30023`, `30024`) are consumed directly when applicable and are not Bahia legacy control-plane/read-model inputs to rewrite.

Changes verified:
- `internal/nostrmigration/manifest.go` now documents explicit `standard` omissions for `LongFormContent` and `LongFormDraft`.
- `internal/nostrmigration/manifest_test.go` requires both NIP-23 constants in the requested omission/alias documentation test, while `TestKindConstantsAreMappedOrJustified` continues to parse all `internal/kinds` constants.

Verification:
- PASS: `GOCACHE=/tmp/bahia-go-build-cache GOMODCACHE=/tmp/bahia-go-mod-cache go test ./internal/nostrmigration -count=1`.
- PASS: `GOCACHE=/tmp/bahia-go-build-cache GOMODCACHE=/tmp/bahia-go-mod-cache go test ./... -count=1`.

## Work item D verification — bahia-6xxd.4

Docs/PSTF scope updated in this pass: `docs/control-planes.md`, `docs/user-guide/nostr-integration.md`, `docs/api.md`, `docs/deployment.md`, `docs/architecture.md`, `docs/designs/dns-orchestration-layer.md`, `docs/designs/nostr-native-system-discovery.md`, and `pstf/features/NOSTR_NATIVE_CONTEXTVM_MIGRATION/*`. Production documentation now describes ContextVM `25910` with CEP-4/NIP-59 wrappers (`1059`/`21059`) and canonical observables (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`, plus NIP-09 `5`). Legacy Bahia request/status/result/read-model/encrypted ranges are either removed from production instructions or explicitly marked historical/migration-only.

Sanity checks for this pass:

- PASS: static old-kind grep over target docs/PSTF returned only migration/historical-labelled occurrences.
- PASS: static production-contract grep found no stale instructions to publish old deploy request kinds, subscribe to old service-state kinds, or depend on legacy system-discovery wording in target docs.
- PASS: PSTF JSON artifacts parse with `python3 -m json.tool`.
- PASS: markdown link sanity check for target docs found no broken relative `.md` links.

## Documentation completion pass — bahia-8w7t

User/agent/operator documentation was updated to reflect the final ContextVM/canonical-kind contract and to document startup migration app usage:

- `docs/user-guide/nostr-integration.md`: added operator-facing migration section covering legacy inputs, canonical outputs, idempotency, relay backfill, signing, and failure handling.
- `docs/control-planes.md`: added runtime startup migration app section and clarified that migration failures must be fixed rather than reintroducing legacy subscribers.
- `docs/nostr-commands.md`: rewritten from the old `596x/696x/796x/3196x` production contract to ContextVM `25910`, CEP-4/NIP-59 wrappers, canonical observables, discovery, and migration-only legacy families.
- `docs/event-spec.md`: rewritten around production families (`25910`, `1059`/`21059`, `30900`, `30078`, `30315`, `4903`, `11316`-`11320`, `30002`, `5`) and startup migration behavior.
- `docs/protocol-compatibility.md`: replaced stale old-kind compatibility tables and removed an unresolved merge-conflict marker; now distinguishes ContextVM/canonical observables from external Loom/Hive-CI protocol interop.
- `docs/operator-assistant-protocol.md`: updated the assistant-safe catalog, receipts, signing model, and validation requirements to ContextVM methods and canonical observables.
- `AGENTS.md`: clarified that ad hoc RPC-over-Nostr remains forbidden while ContextVM `25910` is the approved mutation transport, and expanded required doc-maintenance scope for migration/kind changes.

Verification for this pass:

- PASS: static stale production-kind grep over updated user/agent/operator docs returned only migration/historical-labelled occurrences.
- PASS: static search confirmed no conflict markers remain in `docs/protocol-compatibility.md`.
- PASS: markdown link sanity check for updated docs found no broken relative `.md` links.

## Soul Factory ContextVM adapter checkpoint — fp-30 — 2026-07-19

Observed behavior before this checkpoint: Soul Factory provisioning and lifecycle mutations entered Bahia only through domain-specific request kinds even though ContextVM `25910` is the canonical mutation transport for new control-plane clients.

Implemented behavior:

- Registered `soul-factory/provision` and `soul-factory/action` on Bahia's existing verified ContextVM transport.
- Adapted accepted requests into the existing event-driven Soul Factory reactor through its established parsers and handlers; no REST, polling, or fake completion path was introduced.
- Preserved the original signed `25910` event id, author, and timestamp for lifecycle correlation.
- Returned an immediate acceptance acknowledgment only; existing correlated lifecycle events remain available during contraction.
- Projected ContextVM provisioning progress and terminal outcomes onto replaceable `30900` state at `soul-factory:provisioning:<request-event-id>` plus append-only `4903` audit facts, signed by the configured Soul Factory signer.
- Kept malformed params fail-closed before reactor dispatch.

Verification:

- PASS: `GOFLAGS=-buildvcs=false go test ./internal/soulfactory ./internal/controlplane ./internal/app -count=1` in the Go 1.26 build container.
- PASS: focused adapter/projection tests cover provisioning correlation, action tag projection, parser compatibility, malformed-param rejection, signed `30900`/`4903` publication, and direct-interop isolation.

Remaining `fp-30` work is explicitly staged: project lifecycle actions and authoritative Soul read models onto canonical `30900`/`4903`, migrate browser/CLI publishers and subscribers, contract direct request-kind ingress, and complete the max sidecar deployment proof.

## Stranded-remediation salvage — bahia-plf9g — 2026-09-24

Ported the relevant parts of `71da3a42` onto master baseline `56c5e3b8` in
`chore/branch-salvage`, without importing route canaries:

- Discovery advertises only registered methods; DNS retains current override
  retirement and uses `dns/record-set`. LLM approvals use the browser's existing
  approve/reject method names.
- Artifact, policy and tool-approval publishers use signed canonical ContextVM
  envelopes and acceptance-aware correlation, not legacy request kinds.
- Registered the existing config CLI; enforced require_approval for both artifact
  and runtime-release deployment intents; wired saga metrics to the live governed
  provisioner's store instead of the stranded patch's disconnected store option.
- Restored the seeded release workflow and artifact marker while retaining the
  legacy result-file contract. Bootstrap requires explicit service signer trust;
  no unsigned relay-document identity inference was ported.
- Allowlist authorization/config validation was already covered by `fe7deb48`
  (bahia-kppzm). No redundant validator or pre-filter startup warning was added.

Regression evidence: before the port, root CLI tests failed with "root command
missing config command group" and "unknown command config"; canonical publisher
fixtures did not compile against missing APIs. New executable workflow tests
rejected invalid digests, mismatched refs/checkouts and credential leakage.

PASS on the complete salvage tree (`GOMAXPROCS=4`, `GOFLAGS=-p=2`):
`go build ./...`, `go vet ./...`, `go test ./...`, and `make race`.
Coverage includes production app startup to HTTP /metrics with a checkpoint
written after startup; pending/error policy gates on both deployment paths;
actual handler registration against discovery; signed publisher envelopes;
CLI HTTP routing; and executable workflow/bootstrap scripts with isolated
command fixtures. `git diff --check` passed.
`docker compose --env-file /dev/null config --quiet` also passed with explicit
fixture signer/public-key and socket-GID inputs; no containers were started.

These are local portable checks, not live registry, Compose deployment or VM
provider acceptance. No deployment or remote publication was performed. Existing
unimplemented mutation consumers remain unadvertised; publishing a request is
not proof of completion. User owns issue state. RepoPrompt Oracle review could
not run (`targetBindingMismatch`); the worktree diff was reviewed directly.


## Unused implementation adjudication (2026-09-24)

Scope: worktree `bahia-unwired`, branch `lint/unwired-adjudication`, baseline
`31a904872f45e0f544ec2402b43db9b768bce708`. The unrestricted controlplane lint
command reported 23 unused findings. No suppression, lint configuration change,
legacy runtime re-enablement, issue-state operation, push or merge was used.

### Per-finding disposition

Paths below are relative to `internal/controlplane/` unless otherwise noted.
**Wired** means an actual production consumer and signed-transport tests, not
merely a reference inserted to quiet the linter. **Open** means the retained
implementation is not safe to expose by adding a registration alone.

| # | Original unused finding | Disposition and evidence |
|---|---|---|
| 1 | `Reactor.handleFailoverRequest` | **Open.** `continuity_command_handlers.go:11` decodes retired kind 38430. Serialization still emits that kind (`internal/adapters/nostr/continuity_serialization.go:426,504,574`); the runtime rejects it. No ContextVM replacement consumer exists. Migrate the command contract, prerequisite definitions, replay/idempotency and execution/result path together rather than restore legacy ingress. |
| 2 | `Reactor.handleRecoveryRequest` | **Open.** `continuity_command_handlers.go:28` has the same problem for retired kind 38431. The application subscribes to the internal recovery event but no production ingress emits it (`internal/app/app.go:2274`). |
| 3 | `continuityCommandRequestedEvent` | **Open.** `continuity_command_handlers.go:45` supplies source, target and idempotency data exclusively to the two retained command handlers. Deleting it would erase part of that unwired command path, not remove a superseded implementation. |
| 4 | `Reactor.handleFailoverPolicyDefinition` | **Open.** `continuity_definition_handlers.go:31` emits the event consumed by `StoreRecipe` (`internal/app/app.go:2234`). Canonical definition kind 31401 is absent from reactor request/replay kinds (`reactor.go:1657,1666`) and dispatch. Needs an author-scoped definition subscription/backfill, not a new mutation RPC. |
| 5 | `Reactor.handleStandbyNodeDefinition` | **Open.** `continuity_definition_handlers.go:50` has no runtime ingress; its `EventStandbyNodeDefinitionObserved` additionally has no production subscriber. Requires a real standby inventory/storage consumer as well as subscription/backfill. |
| 6 | `Reactor.handleReplicationPolicyDefinition` | **Open.** `continuity_definition_handlers.go:76` emits the input for `StoreReplicationPolicy` (`internal/app/app.go:2244`), but canonical kind 31403 is absent from runtime subscriptions/replay/dispatch. Needs the complete definition hydration path. |
| 7 | `Reactor.handleRecoveryWorkflowDefinition` | **Open.** `continuity_definition_handlers.go:95` emits the recovery recipe consumed at `internal/app/app.go:2254`; kind 31404 has the same missing subscription/backfill/dispatch. |
| 8 | `Reactor.handlePackagePublishIntent` | **Open.** `package_commands.go:151` publishes `package/publish`, with no registered consumer. `package_handlers.go:122` accepts any nonempty caller-supplied `ApprovedBy` as approval. Define/verify approval authority before exposing it; registry failure also currently only logs before terminal success. |
| 9 | `Reactor.handlePackagePromotionRequest` | **Open.** `package_commands.go:169` publishes `package/promote`; the only prior registrations were transport tests. The service's approval check only tests nonempty `ApprovedBy` (`internal/service/package_registry.go:583`), and handler lines 184-190 swallow state publication failures before success. Requires approval provenance and reliable completion, not just a gate wrapper. |
| 10 | `Reactor.handlePackageYankRequest` | **Open.** `package_commands.go:181` publishes `package/yank`. The retained handler uses `YankPackage` even for `Deprecated`, then adds deprecation metadata (`package_handlers.go:199-225`); the service marks the artifact deleted (`internal/service/package_registry.go:433-464`). Resolve destructive yank versus deprecation semantics and state/terminal failure ordering before exposure. |
| 11 | `Reactor.handlePackageDriftDetect` | **Open.** `package_commands.go:193` publishes `package/drift-detect`. Lines 280-281 publish a drift result and then another terminal package result; persistence only occurs in the latter path. A transport wrapper would add yet another response. Refactor to one transport-owned result after intent persistence, with correlated drift data and replay tests. |
| 12 | `Reactor.publishPackageArtifactRegistry` | **Open.** `package_handlers.go:468` is required by publish/promote/yank, not superseded by another working consumer. It publishes then projects, ignores a zero-relay count, and its callers log failures and continue to success. Repair publication/projection/terminal semantics with those operations. |
| 13 | `Reactor.publishPackagePromotionRegistry` | **Open.** `package_handlers.go:482` is the retained publication/promotion projection writer. Same zero-accept/projection/terminal-success gap; no other live command path supplies this capability. |
| 14 | `Reactor.publishPackageDriftEvent` | **Open.** `package_handlers.go:498` emits the first of two direct responses from the unwired drift handler. Retain until the drift command is migrated to one persisted, transport-owned completion; no production replacement exists today. |
| 15 | `Reactor.handleToolApprovalResponse` | **Open.** `tool_approval_command_publisher.go:72` publishes the advertised method; no production registration exists. `reactor.go:1424-1445` overwrites any intent's status without a pending-state/CAS guard, then may execute provisioning again. Needs approval transition/idempotency and audit guarantees, not a new authenticated entrypoint to this implementation. |
| 16 | `Reactor.handlePolicyCreate` | **Wired.** Converted to ContextVM params/results in `policy_contextvm_handlers.go:13`, registered at `reactor_contextvm_handlers.go:21-33`, called by production assembly (`internal/app/app.go:1756`). FleetOperatorGate authorizes the verified inner signer before service access; empty/nil gate denies all. Requires idempotency for creation, validates inputs, persists via PolicyService, publishes canonical registry state and returns correlated `policy_id`. |
| 17 | `Reactor.handlePolicyUpdate` | **Wired.** `policy_contextvm_handlers.go:40`; same production registration/gate. Loads the stored policy, validates a copy before writing, persists and publishes state; errors return through the transport. |
| 18 | `Reactor.handlePolicyDelete` | **Wired.** `policy_contextvm_handlers.go:98`; same registration/gate. Validates ID, deletes via PolicyService and publishes a canonical tombstone before success. |
| 19 | `Reactor.handlePolicyEvaluate` | **Wired.** `policy_contextvm_handlers.go:117`; same registration/gate, UUID validation and actual PolicyService evaluation. Tests exercise a blocking signature policy rather than an unconditional mock success. |
| 20 | `Reactor.publishPolicyRegistry` | **Wired.** `reactor.go:2305`, invoked by the three gated CRUD handlers. Migrated retired output to projector-compatible `30900` / `domain=policy` / `schema=bahia.cp-state.v1` / `d=id`; positive relay acceptance required and per-policy timestamps increase. The existing projector publishes startup snapshots (`internal/adapters/nostr/projector.go:931,2688`), not live CRUD/tombstones, so it did not supersede this responsibility. |
| 21 | `Reactor.handleWorkerUncordonRequest` | **Wired.** `reactor_contextvm_handlers.go:36`, gated registration at line 18. Direct repository scheduling transition to active, shared legacy transition validator, canonical state publication and transport result. |
| 22 | `Reactor.handleWorkerUndrainRequest` | **Wired.** `reactor_contextvm_handlers.go:40`, gated registration at line 19. Same direct path for draining to active. |
| 23 | `Reactor.handleWorkerMaintenanceEnterRequest` | **Wired.** `reactor_contextvm_handlers.go:44`, gated registration at line 20. Same direct path to maintenance. Disabled workers, conflicting worker/idempotency tags and invalid keys are rejected before writes. |

### Why no deletion-only adjudications

The apparent worker replacements called `WorkerCommandPublisher`, whose
`publishLifecycle` / `publishPlacement` publishes the same ContextVM method
(`worker_command_publisher.go:158-210`). Reactor dispatch did not consume it.
Those three forwarding wrappers and their registration-only test rows have been
replaced with real scheduling mutations and stronger signed transport tests;
the unused Reactor implementations were not safely superseded legacy twins.
Other worker forwarders remain outside the 23-finding scope and must not be
mistaken for proof of completed mutations.

### Authorization, completion and test evidence

`FleetOperatorGate.wrap` (`fleet_operator_gate.go:26`) gates all seven methods
against `cfg.Nostr.AuthorizedPubkeys`, not an inferred service/outer-wrapper
identity or a permissive fallback. Production installs the registrar after the
reactor's real dependencies are supplied; policy handlers report unavailable
when the database repository is absent. Legacy numeric command subscriptions
were not reintroduced. The transport owns correlation, encryption and response
replay; handlers do not synthesize Nostr requests or republish incoming commands.

`reactor_contextvm_handlers_test.go` covers each of the seven methods through
signed plaintext and wrapped transport dispatch for allowed, outsider, empty
allowlist and nil-gate cases. The transport admits both test principals, so the
rejections prove the method gate. Assertions cover zero unauthorized storage
access, worker/policy persistence, actual blocking evaluation, canonical signed
state/tombstones, no republished command, response correlation, replay without
repeat mutation, disabled workers, conflicting tags, invalid IDs/rules/enforcement,
unavailable services, repository failure, zero-relay publication rejection and
same-policy state ordering. Existing worker transition tests still pass.

Counterfactual: temporarily removing only the new registrar's registrations made
`TestReactorContextVMMutationsReachableAndAuthorized` fail with
`Code:-32601 Message:method not found`; restoring them passed. Focused
`go test ./internal/controlplane ./internal/app` passed. RepoPrompt Oracle review
was attempted with the worktree diff artifact and failed `targetBindingMismatch`;
the diff was reviewed directly. RepoPrompt's earlier logical-path edit incorrectly
landed in main despite its worktree binding; the exact patch was transferred to
the requested worktree and only that accidental main edit was reversed. Main was
verified clean. All subsequent edits used the physical worktree.

Final repository-wide verification (`GOMAXPROCS=4`, `GOFLAGS=-p=2`):

| Gate | Outcome |
|---|---|
| `golangci-lint run --max-issues-per-linter=0 --max-same-issues=0` | Exit 1: exactly 15 `unused` findings; no other findings |
| `make lint` | Exit 2 from make / exit 1 from lint: the same 15 findings |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./...` | PASS |
| `make race` (`CGO_ENABLED=1 go test -race ./... -count=1`) | PASS |
| `git diff --check` | PASS |

Context triage used Jev advisory filtering (14 requests, 83,971 input tokens,
approximately $0.003528). Its selected ranges were only navigation aids;
load-bearing claims were checked directly against the physical worktree.
The full lint count is **15**, all deliberately retained `unused` findings above;
`make lint` remains nonzero. This is partial migration completion, not a zero-lint
claim or live relay/provider acceptance. The user owns issue state for the open
findings; no `bd` operations were performed.
