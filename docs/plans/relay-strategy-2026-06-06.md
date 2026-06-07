# Bahia Relay Strategy: Plan

## Goal
Define a Nostr-native relay strategy for Bahia that separates relay lists by purpose, keeps ContextVM discovery and NIP-51/NIP-65 semantics canonical, incorporates NIP-34 repository relay routing, and preserves security boundaries between user, service, public, and sensitive transports.

## Background
- Bahia's canonical bootstrap is ContextVM discovery `11316`-`11320` plus NIP-51 relay sets `30002`; production clients must not depend on legacy discovery `31974` (`docs/control-planes.md:1-18`).
- Relay topology must use existing Nostr mechanisms: NIP-51 `30002`, NIP-65 `10002`, and NIP-51 `10050`; Bahia must not invent relay routing kinds (`docs/nostr-event-implementation-guide.md:115-134`).
- The browser discovery store already reads bootstrap seed relays/trusted service pubkeys, queries trusted discovery plus relay-set events until EOSE, normalizes `bahia-browser-v1`/`bahia-service-v1`, and caches discovery (`web/src/lib/stores/discovery.svelte.js:1-236`).
- Browser subscription plumbing already exposes the lifecycle this strategy must preserve: scoped subscriptions, EVENT/EOSE/CLOSED/AUTH callbacks, validation, event-id dedupe, and explicit close (`web/src/lib/nostr/pool-subscriptions.js:1-98`).
- Backend projection publishes ContextVM system discovery plus `bahia-browser-v1` and `bahia-service-v1`, but both relay sets are currently sourced from the same browser relay slice (`internal/adapters/nostr/projector.go:2050-2164`).
- NIP-34 support parses repository announcement kind `30617` relay hints and queries repository state kind `30618` with EOSE/degraded metadata, but repository relay hints are not yet the explicit first routing choice (`web/src/lib/nostr/repositories.js:1-104`; `web/src/lib/nostr/branches.js:1-200`).
- SoulFactory has separate `OpenClawRelays` and `NgitRelays`, but `NgitRelays` falls back to OpenClaw relays and ngit publication currently uses only the first relay (`internal/soulfactory/workspace.go:17-326`).
- PSTF `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP` verifies sidecar-first bootstrap, EOSE-bounded query-to-live behavior, fail-closed behavior, canonical-author filtering, CLI relay precedence, and encrypted capability gating; some text still references historical `31974` and should be clarified (`pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/acceptance_criteria.json:1-320`).
- External protocol facts were checked against official NIPs on 2026-06-06: NIP-34 repository `relays` tags, NIP-51 relay sets/lists, NIP-65 `10002` read/write lists, NIP-11 metadata, NIP-42 AUTH, and optional NIP-66 monitor/liveness events.

## Current Data Flow
1. Runtime page shell provides seed relay URLs and trusted service pubkeys.
2. `discovery.svelte.js` connects to seed relays, queries ContextVM discovery plus NIP-51 relay sets until EOSE, and accepts only trusted service-authored events.
3. Backend config feeds `projector.publishSystemDiscovery()`, which signs the discovery snapshot and publishes service-authored relay sets.
4. Browser/control-plane clients consume normalized discovery, perform EOSE-bounded historical catch-up, then keep live subscriptions open.
5. Operator clients currently use explicit relay inputs for final transport and filter responses by configured service pubkey when present.
6. Repository UI parses NIP-34 relay hints, but branch/state queries still need a clear policy to prefer those hints over global Bahia relays.

## Blocking Gaps
- `bahia-service-v1` exists as an event contract but is not independently sourced from service relay policy.
- ContextVM request/reply traffic does not have a named relay-set purpose separate from public browser bootstrap.
- Bahia does not publish a service-authored NIP-65 relay preference list for wider Nostr routing.
- Repository/ngit relays and agent control relays are configurable but not fully separated in policy or tests.
- PSTF wording still carries historical `31974` language even though current docs make `11316`-`11320` plus `30002` canonical.

## Approach

### Core decisions
1. **No new relay routing kinds.** Use ContextVM discovery `11316`-`11320`, NIP-51 relay sets `30002`, NIP-65 `10002`, NIP-51 `10050` where DM is enabled, NIP-34 repository relay tags, NIP-11 metadata, NIP-42 AUTH, and optional NIP-66 monitor events.
2. **Separate relay purpose from physical relay URL.** Public browser, ContextVM request/reply, service publish/backfill, user/operator, repository/ngit, DM, FIPS public advert, and FIPS/Bahia private endpoint relays are distinct policy purposes even when a deployment intentionally reuses one relay URL.
3. **Make `bahia-contextvm-v1` additive and permanently safe to fall back from.** New service deployments should publish it. New clients should prefer it for ContextVM mutation traffic and fall back to `bahia-browser-v1` with degraded metadata if it is absent. The plan does not require a hard cutover date because older deployments may never publish the new set.
4. **Keep publication single-service-key for this slice.** Relay strategy events are authored by the configured Bahia service key. Multi-key rotation remains a separate key-management design; this plan only requires clients to validate against their configured trusted service pubkey list during discovery.
5. **Define AUTH-unavailable behavior before adding new relay sets.** If a relay requires AUTH and no valid signer/key is available, that relay is excluded from the current operation, surfaced in relay health/error metadata with the CLOSED/OK reason, and retried only through normal reconnect/backoff or a new operation after credentials change. If remaining relays cannot satisfy the operation's publish/read success rules, the operation fails deterministically; no REST or legacy fallback is allowed after relay acceptance.
6. **Treat NIP-11/NIP-66 as advisory metadata.** NIP-11 probing and NIP-66 monitor events may annotate, rank, or warn, but they do not establish trust, do not override service pubkey checks, and cannot remove all configured relays.
7. **Preserve event-driven lifecycle.** Historical state is EOSE-bounded; realtime state uses long-lived subscriptions; publish success checks relay `OK`; CLOSED/AUTH are explicit; timer-based subscription refresh or completion detection is out of scope.

### Relay taxonomy
| Purpose | Owner | Canonical mechanism | Initial event / tag | Notes |
|---|---|---|---|---|
| Public browser bootstrap/read models | Bahia service | NIP-51 `30002` | `bahia-browser-v1` | Sidecar public URL remains first by Bahia convention when sidecar-first is enabled. |
| ContextVM request/reply | Bahia service | NIP-51 `30002` | `bahia-contextvm-v1` | Preferred for mutation traffic; falls back to `bahia-browser-v1` when absent. |
| Service publish/backfill | Bahia service | NIP-51 `30002`; advisory NIP-65 `10002` | `bahia-service-v1`; service `10002` | Source from service relay config, not public browser relays. |
| User/operator preferences | User/operator pubkey | NIP-65 `10002` | user-authored list | General author routing only; not service strategy authorization. |
| Repository/ngit | Repository maintainer / SoulFactory | NIP-34 `30617` `relays`; `30618` state | repository announcements | Repository operations prefer repo relay hints before global relays. |
| DM receive routing | Receiving identity | NIP-51 `10050` | DM relay list | Only for DM-enabled Bahia features; public bootstrap does not imply DM readiness. |
| FIPS public adverts | FIPS/Bahia operator | Existing FIPS event contract + explicit bridge config | FIPS overlay advert relays | May be public and separate from Bahia endpoint/control relays. |
| FIPS/Bahia endpoint/control | Bahia service/operator | ContextVM relay sets / explicit bridge config | `bahia-contextvm-v1` or bridge config | Sharing with public relays is an explicit deployment exposure decision. |
| Relay capability/liveness | Relay or trusted monitor | NIP-11; optional NIP-66 `10166`/`30166` | metadata/monitor events | Advisory only. |

### Recommended target state
- Backend relay policy has independent semantic sources for public browser bootstrap, ContextVM request/reply, and service publish/backfill. Implementation may choose exact config names consistent with `internal/config/config.go`; it must not repurpose `nostr.relays` as public browser relays.
- The service publishes distinct `bahia-browser-v1`, `bahia-contextvm-v1`, and `bahia-service-v1` NIP-51 relay sets, plus an advisory service NIP-65 `10002` list.
- Browser discovery normalizes ContextVM relays, preserves existing browser-relay fail-closed behavior, and records degraded metadata when falling back from absent `bahia-contextvm-v1`.
- Operator discovery keeps explicit final relay precedence and may add a separate trusted bootstrap-discovery path only when both bootstrap relay seeds and trusted service pubkeys are configured.
- Repository and SoulFactory flows treat NIP-34/ngit relays as repository-specific policy, not as a generic control-plane relay substitute.
- FIPS public advert relay guidance is documented as part of the taxonomy; no separate implementation work is needed unless a bridge currently defaults sensitive endpoint traffic to browser relay sets.

## Work Items

### Item 1 — Land relay taxonomy, canonical wording, and FIPS boundary docs
**Goal:** Make the relay strategy explicit before implementation begins.  
**Done when:** Docs define each relay purpose, owner, NIP mechanism, security boundary, and the no-new-routing-kinds rule; FIPS public/private relay exposure is documented; PSTF wording clarifies that `31974` references are historical and that `11316`-`11320` plus `30002` are canonical.  
**Key files:** `docs/plans/relay-strategy-2026-06-06.md`; `docs/control-planes.md`; `docs/nostr-event-implementation-guide.md`; `docs/protocol-compatibility.md`; `docs/designs/fips-bahia-integration.md`; `pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/*`.  
**Dependencies:** None.  
**Size:** Medium.

### Item 2 — Add independent backend relay policy sources
**Goal:** Separate service, browser, and ContextVM relay policy in config and validation.  
**Done when:** There are independent semantic sources for service publish/backfill, public browser bootstrap/read, and ContextVM request/reply relays; ContextVM relays default to browser relays only as additive compatibility; legacy private mirror fields remain non-configurable; AUTH-unavailable semantics from this plan are reflected in validation/error paths.  
**Key files:** `internal/config/config.go`; `internal/adapters/nostr/projector.go`; config docs/tests.  
**Dependencies:** Item 1.  
**Size:** Medium.

### Item 3 — Publish distinct canonical NIP-51 relay sets
**Goal:** Align backend discovery projection with the relay taxonomy.  
**Done when:** Service-authored `bahia-browser-v1`, `bahia-contextvm-v1`, and `bahia-service-v1` are published from their respective policy sources; sidecar-first mode fails publication when public browser relays are absent; publish outcomes check relay `OK` and surface partial failures; all relay-set events are signed by the configured service key.  
**Key files:** `internal/adapters/nostr/projector.go`; `internal/config/config.go`; backend projector/discovery tests.  
**Dependencies:** Items 1 and 2.  
**Size:** Medium.

### Item 4 — Normalize ContextVM relays in browser discovery
**Goal:** Let clients consume the new ContextVM relay set without breaking existing deployments.  
**Done when:** Browser discovery reads `bahia-contextvm-v1`, exposes normalized ContextVM relays, falls back to `browser_relays` with degraded metadata if absent, and keeps encrypted capability gated separately from public bootstrap.  
**Key files:** `web/src/lib/stores/discovery.svelte.js`; control-plane store consumers; existing discovery/store tests.  
**Dependencies:** Item 3.  
**Size:** Medium.

### Item 5 — Publish service NIP-65 relay preferences
**Goal:** Give wider Nostr clients standard service read/write hints without replacing Bahia bootstrap relay sets.  
**Done when:** The service key publishes `10002` with service write relays and request/read relays where the service accepts tagged requests; clients treat it as advisory and continue using NIP-51 relay sets for Bahia bootstrap.  
**Key files:** `internal/adapters/nostr/projector.go`; `internal/kinds/kinds.go`; `docs/protocol-compatibility.md`.  
**Dependencies:** Items 2 and 3.  
**Size:** Medium.

### Item 6 — Add trusted operator discovery fallback
**Goal:** Preserve explicit operator relay precedence while enabling Nostr-native discovery when final relay config is absent.  
**Done when:** Explicit CLI relays remain highest priority, env relays remain second, and a separate bootstrap-discovery path can query trusted `30002` sets only when bootstrap relays and trusted service pubkeys are both configured; discovery chooses `bahia-contextvm-v1` then `bahia-browser-v1`; missing trust or relay events fail deterministically. Exact flag/env names are chosen during implementation to match existing CLI conventions.  
**Key files:** `cmd/cli/main.go`; `cmd/cli/operator_nostr.go`; `pkg/client/operator_nostr.go`.  
**Dependencies:** Item 4.  
**Size:** Medium.

### Item 7 — Verify NIP-42 AUTH and relay-unavailable handling across clients
**Goal:** Make relay authentication behavior consistent across browser, backend, and operator clients.  
**Done when:** Browser `onAuth` behavior remains event-driven; backend/operator pools attempt AUTH when credentials are available; auth-required relays without credentials are excluded from the current operation with visible relay health/error metadata; operations fail when remaining relays cannot satisfy their success rule; no fallback mutation path is attempted after relay acceptance.  
**Key files:** `web/src/lib/nostr/pool-subscriptions.js`; `pkg/client/operator_nostr.go`; `internal/adapters/nostr` relay/subscriber code; FIPS subscriber/bridge references from `docs/reviews/dns-fips-architectural-audit.md`.  
**Dependencies:** Items 1 and 2.  
**Size:** Medium.

### Item 8 — Route NIP-34 repository operations through repository relays
**Goal:** Treat repository relays as NIP-34-specific routing hints.  
**Done when:** Repository selections preserve `relayUrls` from `30617` `relays` tags; branch/state lookup queries repository relay hints before global control-plane read relays; missing repository relays trigger documented fallback with degraded metadata; incomplete EOSE behavior remains visible.  
**Key files:** `web/src/lib/nostr/repositories.js`; `web/src/lib/nostr/branches.js`; repository picker components/tests.  
**Dependencies:** Item 4.  
**Size:** Medium.

### Item 9 — Separate SoulFactory/OpenClaw control relays from ngit relays
**Goal:** Keep agent runtime/control relays distinct from repository publication relays.  
**Done when:** `OpenClawRelays` are documented/tested as agent runtime/control relays; `NgitRelays` are required for NIP-34 workspace publication when workspace publishing is enabled; implementation verifies whether installed `ngit` supports multiple relay arguments and either uses all configured relays or tracks the tool limitation in Beads.  
**Key files:** `internal/soulfactory/workspace.go`; `cmd/openclaw-soulfactory-sidecar/main.go`; SoulFactory tests/docs.  
**Dependencies:** Item 1.  
**Size:** Medium.

### Item 10 — Add relay metadata inputs as advisory follow-up
**Goal:** Integrate NIP-11, NIP-66, and DM relay-list support without putting optional metadata on the critical path.  
**Done when:** A follow-up Bead or implementation slice defines best-effort NIP-11 probing, configured-trust NIP-66 monitor use, and NIP-51 `10050` publication only for DM-enabled features; each is explicitly advisory unless a later PSTF slice approves stronger behavior.  
**Key files:** `internal/config/config.go`; `internal/adapters/nostr`; `web/src/lib/nostr`; notification/DM publisher code; settings/relay-health UI if exposed.  
**Dependencies:** Item 1.  
**Size:** Small planning follow-up; implementation sizes split per feature.

### Item 11 — Add verification and user-facing documentation for implemented slices
**Goal:** Ensure tests and docs prove the intended relay behavior, not legacy or placeholder behavior.  
**Done when:** Contract/unit/e2e tests cover distinct browser/contextvm/service sets, fail-closed missing required sets, AUTH/CLOSED/EOSE paths, NIP-34 relay preference, and advisory metadata behavior for any optional metadata slice that ships; user-facing docs are updated for changed Nostr integration, CLI flags, and relay settings behavior.  
**Key files:** web discovery tests; backend projector/discovery tests; `pstf/features/*`; `docs/user-guide/nostr-integration.md`; `docs/nostr-commands.md`; `docs/event-spec.md`; `docs/protocol-compatibility.md`; `docs/user-guide/cli-reference.md` if CLI behavior changes.  
**Dependencies:** Runs alongside each implementation item; PSTF wording cleanup starts with Item 1.  
**Size:** Medium to Large depending on shipped slices.

## Migration and Risk Controls
- **Additive rollout:** Publish `bahia-contextvm-v1` before clients prefer it. Clients keep permanent fallback to `bahia-browser-v1` with degraded metadata for older deployments.
- **Config compatibility:** Do not repurpose `nostr.relays` as public browser relays. Keep service vs browser semantics distinct even when URLs overlap.
- **Single-key scope:** This plan assumes one configured service signing key for publishing relay strategy events. Multi-key rotation must be a separate Bead/design if needed.
- **NIP-66 safety:** Safe default is no trusted monitors. Monitor data cannot override configured relays or service-pubkey trust.
- **Operator safety:** Discovery fallback requires explicit bootstrap relays plus trusted service pubkeys; no default TOFU.
- **EOSE semantics:** Use existing bounded query/degraded metadata patterns where appropriate, but do not add polling refresh loops or timeout-as-completion logic.
- **ngit uncertainty:** Verify repeated relay support in `ngit`; track tool limitations in Beads rather than comments or silent single-relay assumptions.

## Open Questions
- Which deployments need multi-key service rotation for relay strategy publication? If required, create a dedicated key-management Bead before implementing Items 3 and 5.
- Which NIP-66 monitor pubkeys, if any, should Bahia trust by default? The safe default for this plan is none.

## References
- `docs/control-planes.md`
- `docs/nostr-event-implementation-guide.md`
- `docs/protocol-compatibility.md`
- `docs/designs/nostr-native-system-discovery.md`
- `pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/acceptance_criteria.json`
- `web/src/lib/stores/discovery.svelte.js`
- `web/src/lib/nostr/pool-subscriptions.js`
- `internal/adapters/nostr/projector.go`
- `web/src/lib/nostr/repositories.js`
- `web/src/lib/nostr/branches.js`
- `internal/soulfactory/workspace.go`
- NIP-11: https://github.com/nostr-protocol/nips/blob/master/11.md
- NIP-34: https://github.com/nostr-protocol/nips/blob/master/34.md
- NIP-42: https://github.com/nostr-protocol/nips/blob/master/42.md
- NIP-51: https://github.com/nostr-protocol/nips/blob/master/51.md
- NIP-65: https://github.com/nostr-protocol/nips/blob/master/65.md
- NIP-66: https://github.com/nostr-protocol/nips/blob/master/66.md
