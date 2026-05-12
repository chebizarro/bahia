# Nostr-Native System Discovery — Design Doc

> **Status**: Draft  
> **Date**: 2026-05-11  
> **Replaces**: `GET /api/v1/system/info` (HTTP discovery endpoint)  
> **PSTF Feature**: `SYSTEM_DISCOVERY_RELAY_BOOTSTRAP`  
> **Prerequisite HITL Decisions**: HITL-001 (remove `nostr.relays`), HITL-004 (NIP-51 kind 30002 for operator relay visibility)

---

## 1. Context & Scope

### Problem

The web app, CLI, and settings page all bootstrap from `GET /api/v1/system/info` — an HTTP request/response endpoint that contradicts Bahia's Nostr-native event-driven architecture. The AGENTS.md guardrails explicitly prohibit request/response patterns and polling APIs. This endpoint is the last major HTTP-bound bootstrap dependency.

### Goal

Replace `/api/v1/system/info` with signed Nostr replaceable events so that:

- Browser bootstrap requires no HTTP beyond the initial page serve
- CLI/operator discovery requires no HTTP at all
- Settings/operator UIs consume relay-delivered discovery events
- The discovery contract is cryptographically authenticated (signed by service pubkey)
- The migration is staged and reversible

### What `/api/v1/system/info` serves today

| Section | Fields | Primary consumers |
|---|---|---|
| `nostr` | `browser_relays`, `sidecar_url`, `browser_encrypted_request_relays`, `service_pubkey`, `service_npub`, `publish_enabled` | Browser bootstrap, CLI relay resolution |
| `control_plane` | `version`, `capabilities`, `request_kinds`, `status_kinds`, `result_kinds`, `read_model_kinds`, `correlation_tags`, MCP metadata | Browser control-plane store, MCP agents |
| `registries` | OCI, Harbor, configured, public registries | Settings page |
| `blossom` | `enabled`, `url`, `servers`, `storage_class` | Settings page |
| `runtime` | `type`, `environments` | Settings page |
| `oci` | `enabled`, `public_host` | Settings page |
| `features` | 18 feature flags incl. explicit `false` legacy flags | Browser bootstrap gating, encrypted transport gating |

---

## 2. Discovery Event Design

### 2.1 Kind 31974 — Bahia System Discovery

A **parameterized replaceable event** (NIP-33) carrying the full non-relay discovery contract.

```
Kind:    31974
Author:  <service-pubkey>
d-tag:   "bahia-system-v1"
Content: JSON snapshot (see below)
Tags:    ["d", "bahia-system-v1"]
```

**Content schema:**

```json
{
  "schema": "bahia.system-discovery.v1",
  "control_plane": {
    "version": "bahia-controlplane-v1",
    "capabilities": ["service_deployments", "relay_read_models", ...],
    "request_kinds":    { "deploy_request": 5961, ... },
    "status_kinds":     { "deployment_status": 6961, ... },
    "result_kinds":     { "deployment_result": 7961, ... },
    "read_model_kinds": { "service_state": 31961, ... },
    "correlation_tags": ["service", "environment", "artifact", ...],
    "mcp": { "async_correlation": true, "fields": [...] }
  },
  "registries": [
    { "id": "bahia-oci", "name": "Bahia Registry", "base_url": "...", "type": "native", "default": true, "enabled": true },
    ...
  ],
  "blossom": { "enabled": true, "url": "...", "servers": [...], "storage_class": "..." },
  "runtime": { "type": "docker", "environments": ["production", "staging"] },
  "oci":     { "enabled": true, "public_host": "..." },
  "features": {
    "oci": true,
    "relay_read_models": true,
    "encrypted_nostr_requests": true,
    "mcp_transport": true,
    "legacy_sse": false,
    "legacy_jwt_exchange": false,
    ...
  }
}
```

**Why one event, not many:**
- Current consumers expect these fields atomically (the browser caches one payload)
- Splitting creates version skew between co-dependent fields (e.g., `features.llm_control_plane` and `control_plane.request_kinds.llm_*`)
- Total payload is well under relay size limits (~2-4 KB)
- One subscription, one EOSE boundary, one latest-wins replacement

**Why not multi-character tag filtering:**
Standard Nostr relay filters support `#<single-letter-tag>` only. Do not add tags like `["capability", "..."]` and claim relay-side filterability. Use only:
- `["d", "bahia-system-v1"]` for parameterized replaceable identity
- Optional `["t", "bahia"]`, `["t", "system-discovery"]` for secondary human/indexer discoverability

### 2.2 Kind 30002 — NIP-51 Relay Sets

Standard NIP-51 relay set events for relay URL discovery. Each is a parameterized replaceable event authored by the service pubkey.

| d-tag | Purpose | Tags |
|---|---|---|
| `bahia-browser-v1` | Public browser bootstrap relays (sidecar URL first if sidecar-first) | `["relay", "wss://..."]` per URL |
| `bahia-requests-v1` | Request-domain relays for encrypted event traffic | `["relay", "wss://..."]` per URL |
| `bahia-service-v1` | Relays the service publishes to (operator/settings visibility, per HITL-004) | `["relay", "wss://..."]` per URL |

**Sidecar URL handling:** The sidecar URL is included as the first entry in the `bahia-browser-v1` relay set, not given its own d-tag. As a **Bahia-specific convention**, consumers treat the first relay in the set as preferred (NIP-51 preserves tag order but does not define preference semantics). This avoids an artificial consumer distinction that no current code requires.

**Naming rationale:** `bahia-requests-v1` instead of `bahia-encrypted` avoids the "encrypted relay" framing rejected by HITL-003. The relays themselves are not encrypted; they carry events whose *content* is NIP-44 encrypted.

### 2.3 What each consumer subscribes to

| Consumer | Filter | Expected events |
|---|---|---|
| Browser bootstrap | `{kinds: [31974, 30002], authors: [<service-pubkey>]}` | System discovery + all relay sets |
| CLI relay resolution | `{kinds: [30002], authors: [<service-pubkey>], "#d": ["bahia-browser-v1"]}` | Browser relay set only |
| Settings page | `{kinds: [31974, 30002], authors: [<service-pubkey>]}` | Full discovery + all relay sets |
| Encrypted transport | `{kinds: [30002], authors: [<service-pubkey>], "#d": ["bahia-requests-v1"]}` | Request-domain relay set |

### 2.4 Service identity

The service pubkey is no longer an explicit field in the discovery payload — it is the **author** of the discovery events. `service_npub` can be derived client-side from the event's `pubkey` field. This is cryptographically stronger than a self-reported field in a JSON response.

---

## 3. Browser Bootstrap Without HTTP

### The bootstrap problem

Nostr subscriptions require a relay URL. Today, that URL comes from the HTTP endpoint. Removing it creates a chicken-and-egg problem.

### Solution: Runtime-injected bootstrap seed

The initial HTML page serve delivers a minimal bootstrap seed — not via an API call, but as part of the application shell itself:

```js
// Injected by the server into the HTML template at serve time
window.__BAHIA_BOOTSTRAP__ = {
  schema: "bahia.bootstrap.v1",
  relay_urls: ["wss://sidecar.example.com/ws", "wss://backup.example.com/ws"],
  service_pubkeys: ["<hex-pubkey-1>"]
};
```

**Why runtime-injected, not build-time:**
- Relay URLs may change without a frontend rebuild
- Service pubkey rotation requires updating the seed without redeployment
- Build-time env vars (`$env/static/public`) are appropriate for dev/local only

**Why `service_pubkeys` is a list:**
Key rotation requires accepting events from either the old or new pubkey during a transition window. Consumers accept the first valid discovery event from *any* trusted pubkey in the list.

### Browser bootstrap flow

```
1. Page loads → read window.__BAHIA_BOOTSTRAP__
2. Connect to seed relay_urls via WebSocket
3. Subscribe: {kinds: [31974, 30002], authors: service_pubkeys}
4. Collect events until EOSE
5. Apply latest-wins per (kind, pubkey, d-tag)
6. Validate: must have ≥1 kind 31974 event AND ≥1 kind 30002 bahia-browser-v1 event
7. Fail closed if validation fails
8. Normalize into current systemInfo.data shape
9. controlplane.svelte.js consumes normalized discovery (unchanged)
10. Proceed with EOSE-bounded read-model bootstrap → live subscriptions
```

**No HTTP involved after step 1** (the page serve itself).

### Fail-closed conditions

- No bootstrap seed present → error, no silent fallback
- No relay connected after seed → error
- EOSE reached with no kind 31974 event → error
- EOSE reached with no `bahia-browser-v1` relay set → error
- Event from untrusted pubkey → ignored
- Invalid event signature → ignored
- `features.relay_read_models` absent or false in discovery → error (same as today)

---

## 4. CLI/Operator Bootstrap Without HTTP

### Precedence chain (extended from current)

```
1. --relay flag                    (explicit, highest priority)
2. BAHIA_NOSTR_RELAYS env          (explicit)
3. Nostr discovery (new) — requires BOTH a relay seed from (1) or (2) AND a trusted pubkey from `--service-pubkey` / `BAHIA_SERVICE_PUBKEY`
4. [Migration only] HTTP fallback via GetSystemInfo()
```

### Trust model: explicit trust, not TOFU

Operator commands trigger deployments, restarts, stops, and adoption flows. Trust-on-first-use is too weak as a default.

**Default behavior:** If `--relay` provides a relay but `--service-pubkey` / `BAHIA_SERVICE_PUBKEY` is not set:
- During migration (phases 0-2): fall back to HTTP `GetSystemInfo()` if available
- After HTTP removal (phase 3): fail with a clear error

**Optional future TOFU:** If product approves it, gate behind `--allow-insecure-discovery` with a persisted pin store per deployment. This is out of scope for the initial design.

### CLI Nostr discovery flow

```
1. Resolve relay URLs from --relay / env (existing logic)
2. Resolve service pubkey from --service-pubkey / env (new)
3. Connect to relay(s)
4. Subscribe: {kinds: [30002], authors: [service_pubkey], "#d": ["bahia-browser-v1"]}
5. Wait for EOSE
6. Extract relay URLs from bahia-browser-v1 event tags
7. Use those relays for operator control-plane requests
```

---

## 5. Event Scoping & Validation

### Publisher-side

- Events signed with the Bahia service private key (same key currently used for `derivePublicKey()`)
- Kind 31974 content is the canonical JSON snapshot built from `config.Config`
- Kind 30002 tags reflect the configured relay sets
- Events published to the sidecar relay boundary only (not mirrored upstream by default)

### Consumer-side validation

All consumers MUST enforce:

| Check | Rationale |
|---|---|
| Event `pubkey` ∈ trusted service pubkeys | Prevents spoofed discovery |
| Event ID = SHA256(serialized) | NIP-01 integrity |
| Valid schnorr signature over event ID | NIP-01 authenticity |
| Timestamp not wildly in the future (>10 min ahead) | Prevents pre-dated spoofing; no lower bound — replaceable events may be old |
| Correct `kind` (31974 or 30002) | Filter validation |
| Correct `d` tag value | Prevents cross-version confusion |
| Latest-wins by (kind, pubkey, d-tag) | Parameterized replaceable semantics |
| Content parses as valid JSON with `schema` field | Forward compatibility |

### Publication scope & mirroring policy

Discovery events SHOULD be published only to the sidecar relay boundary — the same scope as Bahia's canonical read-model events (kind 31961-31973). Discovery events SHOULD NOT be mirrored to upstream public relays by default, because the payload reveals:

- Internal registry URLs and configuration
- Runtime environment names
- Feature flag surface
- Blossom server topology

If broader publication is desired, it must be an explicit operator decision, not a default.

### Key rotation

- Bootstrap seed accepts a list of `service_pubkeys`
- During rotation, the publisher dual-publishes discovery events signed with both old and new keys
- Consumers accept events from any trusted pubkey in the seed list
- After rotation completes, remove the old key from the seed and republish with the new key only

---

## 6. Migration Sequencing & Risks

### Phase 0 — Parallel publication (additive, no consumers change)

**What ships:**
- Extract shared discovery builder from `SystemHandler.GetInfo` into `internal/controlplane/system_discovery.go`
- Add kind 31974 constant (`KindSystemDiscovery`) to controlplane
- Publish kind 31974 + kind 30002 events on startup, relay reconnect, and config change
- HTTP endpoint unchanged, now delegates to shared builder

**Risk:** Low — purely additive, no consumer changes.

### Phase 1 — Dual-path consumers (feature-flagged)

**What ships:**
- Browser: new `discoveryStore` with Nostr-backed loader behind feature flag; normalizes into current `systemInfo.data` shape
- CLI: add `--service-pubkey` / `BAHIA_SERVICE_PUBKEY` inputs; Nostr discovery path with HTTP fallback
- Settings page: subscribe to kind 30002 `bahia-service-v1` for operator relay visibility
- Bootstrap seed injection in SvelteKit server hook

**Risk:** Medium — dual-path complexity, but HTTP fallback limits blast radius.

### Phase 2 — Deprecation

**What ships:**
- Browser default switches to Nostr-first discovery
- HTTP endpoint logs usage, emits deprecation header
- CLI Nostr discovery becomes default when pubkey is configured
- PSTF artifacts and docs updated to declare Nostr-native discovery as authoritative

**Risk:** Medium — external consumers of `/api/v1/system/info` break if they haven't migrated. Deprecation period provides warning.

### Phase 3 — Removal

**What ships:**
- Delete `SystemHandler.GetInfo`, HTTP route, `getSystemInfo()` API method
- Delete `pkg/client.GetSystemInfo()` and `SystemInfo` types (or move them to internal normalization)
- Remove HTTP fallback from CLI
- Update all docs, PSTF, and AGENTS.md references

**Risk:** High if external consumers exist. Must be gated by deprecation telemetry from phase 2.

### Rollback

- Phases 0-1: No rollback needed (HTTP still primary)
- Phase 2: Flip feature flag back to HTTP-first
- Phase 3: Contract rollback — requires re-adding the endpoint, treat as a release decision

---

## 7. Settings, Registries & Encrypted Metadata

### Settings page migration

| Current source | New source | Event |
|---|---|---|
| `systemInfo.nostr.relays` ("Server Relays") | Service-authored relay set | Kind 30002, d-tag `bahia-service-v1` |
| `systemInfo.nostr.service_npub` ("Service Identity") | Event author pubkey (NIP-19 encode) | Kind 31974 author field |
| `systemInfo.nostr.publish_enabled` | Discovery features | Kind 31974 `features.publish_enabled` (preserved as explicit field; not equivalent to `relay_read_models`) |
| `systemInfo.oci.*` | Discovery snapshot | Kind 31974 `oci` section |
| `systemInfo.registries` | Discovery snapshot | Kind 31974 `registries` section |
| `systemInfo.blossom.*` | Discovery snapshot | Kind 31974 `blossom` section |

### Registry information

Registries remain in the kind 31974 content. Public registries (ghcr, dockerhub, quay) are static and can be hardcoded client-side or included in the discovery snapshot. The design preserves the current approach of including them in the discovery payload.

### Encrypted request transport

Encrypted capability gating reads from:
- Kind 30002 `bahia-requests-v1` → relay URLs for request-domain traffic
- Kind 31974 `features.encrypted_nostr_requests` → feature flag
- Kind 31974 author pubkey → service pubkey for NIP-44 encryption

The browser encrypted transport module (`encrypted-controlplane.js`) continues checking all three inputs coherently. Encrypted capability is NOT inferred from public relay presence (per HITL-003).

---

## 8. Recommended File Touch Points

### New files

| File | Purpose |
|---|---|
| `internal/controlplane/system_discovery.go` | Shared discovery builder: assembles snapshot from `config.Config`, produces kind 31974 JSON and kind 30002 tag sets |
| `internal/controlplane/system_discovery_test.go` | Unit tests for snapshot assembly, kind constant validation |
| `web/src/lib/stores/discovery.svelte.js` | Nostr-backed discovery store with seed bootstrap, EOSE handling, normalization |
| `web/tests/unit/discovery-store.test.js` | Unit tests for Nostr discovery bootstrap |

### Modified files

| File | Change |
|---|---|
| `internal/controlplane/reactor.go` | Add `KindSystemDiscovery = 31974` constant |
| `internal/api/handlers/system.go` | Delegate to shared builder; keep HTTP shape unchanged during phases 0-2 |
| `internal/api/handlers/system_test.go` | Verify HTTP output uses shared builder |
| `internal/adapters/nostr/` (publisher) | Extend existing Bahia event publication to emit kind 31974 + kind 30002 on startup/reconnect/change |
| `cmd/cli/operator_nostr.go` | Add `--service-pubkey` / env resolution; Nostr discovery path with HTTP fallback |
| `cmd/cli/operator_nostr_test.go` | Add precedence + failure tests for Nostr discovery |
| `pkg/client/client.go` | Keep `SystemInfo` during migration; optionally add Nostr discovery method |
| `web/src/lib/stores/system.svelte.js` | Dual-path: Nostr-first with HTTP fallback, normalize to current shape |
| `web/src/lib/stores/controlplane.svelte.js` | Remove any `nostr.relays` fallback; validate works with normalized Nostr discovery |
| `web/src/routes/settings/+page.svelte` | Read server relays from kind 30002 subscription; read other metadata from normalized discovery |
| `web/src/lib/nostr/client.js` | Add `BAHIA_KINDS.SYSTEM_DISCOVERY = 31974`; ensure kind 30002 support |
| `web/src/lib/nostr/encrypted-controlplane.js` | Read encrypted relay URLs from kind 30002 `bahia-requests-v1` instead of system info |
| `docs/control-planes.md` | Document discovery protocol, kind constant, relay-set d-tags, bootstrap seed, trust model |
| `pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/feature_spec.json` | Update feature boundary and intended behavior |
| `pstf/features/SYSTEM_DISCOVERY_RELAY_BOOTSTRAP/hitl_decisions.md` | Add HITL decisions for seed mechanism, trust model, mirroring policy |

### Files to eventually delete (Phase 3)

| File | Reason |
|---|---|
| `internal/api/handlers/system.go` (GetInfo method) | HTTP endpoint removed |
| `internal/api/router/router.go` (system info route) | Route registration removed |
| `web/src/lib/api/client.js` (getSystemInfo method) | HTTP client method removed |
| `pkg/client/client.go` (GetSystemInfo + SystemInfo types) | Go HTTP client removed |

---

## 9. Test Strategy

### Unit tests

| Test | Validates |
|---|---|
| `system_discovery_test.go` — snapshot assembly | Shared builder produces correct JSON from config; kind maps match controlplane constants; feature flags follow derivation rules; legacy flags remain explicit false |
| `system_discovery_test.go` — relay set assembly | kind 30002 events have correct d-tags and relay tags; sidecar URL is first in browser set; encrypted relays are separate |
| `system_discovery_test.go` — kind constant | `KindSystemDiscovery = 31974` does not collide with any existing controlplane kind |
| `discovery-store.test.js` — seed bootstrap | Store reads seed config; connects to seed relays; subscribes with correct filters; normalizes events to systemInfo shape |
| `discovery-store.test.js` — EOSE handling | Store resolves after EOSE with valid events; fails closed on missing discovery event; fails closed on missing relay set |
| `discovery-store.test.js` — validation | Rejects events from untrusted pubkey; rejects invalid signatures; applies latest-wins for replaceable events |
| `discovery-store.test.js` — cache/dedupe | Preserves current cache semantics; deduplicates concurrent loads; supports force reload |
| `discovery-store.test.js` — key rotation | Accepts events from any pubkey in trusted set |
| `operator_nostr_test.go` — precedence | `--relay` > env > Nostr discovery > HTTP fallback; `--service-pubkey` required for Nostr path |
| `operator_nostr_test.go` — no TOFU | Fails when relay available but no service pubkey and no HTTP fallback |

### Integration tests

| Test | Validates |
|---|---|
| Publisher integration | Service publishes kind 31974 + 30002 to sidecar on startup; events are retrievable via subscription |
| Multi-consumer contract | Single set of published events satisfies browser bootstrap, CLI relay resolution, and settings page consumption |
| Migration dual-path | Browser Nostr-first with HTTP fallback works; CLI Nostr-first with HTTP fallback works |

### E2E tests

| Test | Validates |
|---|---|
| `controlplane-nostr-smoke.spec.js` (extended) | Page load → seed bootstrap → Nostr discovery → EOSE → controlplane live subscription — no `/api/v1/system/info` call |
| Settings page | Settings page displays service relays from kind 30002 subscription, not from HTTP system info |

### What NOT to test

- Do not test relay-side filtering of multi-character tags (not a real capability)
- Do not test TOFU pinning (out of scope for initial design)
- Do not test upstream mirroring of discovery events (not default behavior)

---

## 10. New HITL Decisions Required

Before implementation, the following decisions need human approval:

| Decision | Question | Options |
|---|---|---|
| HITL-006 | Is runtime-injected bootstrap seed (via HTML template) acceptable for production browser bootstrap? | A) Yes, runtime injection B) Build-time env vars only C) Both as options |
| HITL-007 | Should CLI require explicit service pubkey for Nostr discovery, or allow TOFU with opt-in? | A) Require explicit pubkey (recommended) B) Allow TOFU with `--allow-insecure-discovery` C) Defer CLI Nostr discovery |
| HITL-008 | Should discovery events be published to sidecar boundary only, or also mirrored upstream? | A) Sidecar only (recommended) B) Mirror upstream by default C) Operator-configurable |
| HITL-009 | Is kind 31974 approved for BahiaSystemDiscovery? | A) Approve 31974 B) Use different kind number |

---

## 11. Open Questions

1. **NIP-05 as future bootstrap:** Should `<domain>/.well-known/nostr.json` be a supported CLI bootstrap path? It's still HTTP, but it's a Nostr standard. Deferred to a future design iteration.

2. **Discovery event TTL:** Should the discovery event include an expiry hint (NIP-40 expiration tag)? Probably not — replaceable events are indefinite by design, and expiry would break bootstrap for clients that connect after the TTL.

3. **Config live reload:** If Bahia supports hot config reload in the future, should discovery events be republished on config change? The shared builder makes this trivial, but the trigger mechanism is out of scope.

4. **External consumers:** Are there external tools or integrations that depend on `/api/v1/system/info`? Phase 2 deprecation telemetry should answer this before phase 3.

---

## Appendix A: Kind Number Registry

| Kind | Name | Range | NIP |
|---|---|---|---|
| 30002 | Relay sets | Parameterized replaceable | NIP-51 |
| 31961 | BahiaServiceState | Parameterized replaceable | App-specific |
| 31962 | BahiaServiceRegistry | Parameterized replaceable | App-specific |
| 31963 | BahiaEnvironmentRegistry | Parameterized replaceable | App-specific |
| 31964-31973 | (other Bahia registries) | Parameterized replaceable | App-specific |
| **31974** | **BahiaSystemDiscovery** | **Parameterized replaceable** | **App-specific (new)** |

## Appendix B: Bootstrap Seed Schema

```typescript
interface BahiaBootstrapSeed {
  schema: "bahia.bootstrap.v1";
  relay_urls: string[];          // WebSocket URLs, ordered by preference
  service_pubkeys: string[];     // Hex pubkeys, accepts any during key rotation
}
```

## Appendix C: Migration Compatibility Matrix

| Consumer | Phase 0 | Phase 1 | Phase 2 | Phase 3 |
|---|---|---|---|---|
| Browser bootstrap | HTTP (unchanged) | Nostr-first, HTTP fallback | Nostr-first, HTTP deprecated | Nostr only |
| CLI relay resolution | HTTP (unchanged) | Nostr if pubkey set, else HTTP | Nostr default, HTTP deprecated | Nostr only |
| Settings page | HTTP (unchanged) | Kind 30002 for relays, HTTP for rest | Nostr-first | Nostr only |
| Encrypted transport | HTTP (unchanged) | Kind 30002 for relay URLs | Nostr-first | Nostr only |
| MCP agents | HTTP (unchanged) | HTTP (unchanged) | HTTP deprecated | Nostr or MCP tool discovery |
