# DNS & FIPS Architectural Audit — Nostr-First Compliance

**Date:** 2026-05-23  
**Scope:** All DNS Phase 3 and FIPS integration files  
**Reference:** `AGENTS.md` — Non-Negotiable Architecture, Forbidden Code Smells, Review Checklist  

---

## Executive Summary

The FIPS integration code is **architecturally sound** — it follows Nostr-native subscription patterns with proper event validation, EOSE handling, AUTH support, reconnect with backoff, and idempotent event handling.

The DNS Phase 3 code has **one Critical architectural violation**: a full REST API layer (`dns_catalog.go`, router routes, `+page.ts`, `dns.svelte.js`) exposes DNS read-model data over HTTP GET endpoints. The Svelte frontend fetches exclusively from these REST endpoints. This contradicts the Nostr-first architecture mandate.

The `pkg/discovery/resolver.go` has a **Major anti-pattern**: a configurable `refreshInterval` ticker that periodically tears down and reissues subscriptions — a polling-style refresh mechanism.

**Violation counts:**
- **Critical:** 4 (REST API layer for DNS data — 4 files involved)
- **Major:** 1 (subscription refresh ticker in resolver)
- **Minor:** 3 (missing NIP-11 in resolver, missing event validation in resolver, frontend policies route without backend)

---

## Critical Violations

### CRIT-1: REST API DNS Catalog Handler

**File:** `internal/api/handlers/dns_catalog.go` (lines 1–257)  
**Severity:** Critical — Architectural violation  

**What's wrong:**  
This file implements four HTTP GET handlers that serve DNS read-model data over REST:
- `GET /dns/catalog` — `ListCatalog()`
- `GET /dns/catalog/{fqdn}` — `GetCatalogEndpoint()`
- `GET /dns/zones` — `ListZones()`
- `GET /dns/drift` — `ListDrift()`

These are classic request/response endpoints exposing Nostr-projected state. Per AGENTS.md: *"Nostr is an event stream, not a request/response API"* and *"Do not build… fake request/response wrappers over relays."*

The DNS endpoint read model is already projected into Nostr Kind 31976 events (as seen in `projector.go` line 1806+). Clients should subscribe to these events directly via relay subscriptions, not fetch them through an HTTP intermediary.

**Why it violates AGENTS.md:**
- Wraps Nostr-projected data in REST request/response semantics
- Creates a polling target for clients instead of a subscription source
- Breaks the "subscribing and reacting" paradigm

**Recommendation:**  
Remove the REST catalog endpoints entirely. Clients (including the Svelte dashboard) should subscribe to Kind 31976 parameterized replaceable events via Nostr relay WebSocket, filtering by author pubkey. The projector already publishes these events. If a server-side read model is needed for the MCP layer (which is a tool interface, not a client-facing API), that's acceptable — but the HTTP handler for direct browser consumption is the violation.

---

### CRIT-2: REST DNS Route Registration

**File:** `internal/api/router/router.go` (lines 254–259)  
**Severity:** Critical — Architectural violation  

**What's wrong:**
```go
if deps.DNSCatalog != nil {
    r.Get("/dns/catalog", deps.DNSCatalog.ListCatalog)
    r.Get("/dns/catalog/{fqdn}", deps.DNSCatalog.GetCatalogEndpoint)
    r.Get("/dns/zones", deps.DNSCatalog.ListZones)
    r.Get("/dns/drift", deps.DNSCatalog.ListDrift)
}
```

These route registrations wire the REST handlers into the HTTP router. They should be removed along with the handler.

**Recommendation:**  
Remove the DNS route block. If the MCP JSON-RPC interface needs DNS tools (which it already has via `dns_tools.go`), those are accessed through the MCP transport, not REST routes.

---

### CRIT-3: Svelte DNS Page Fetches from REST API

**File:** `web/src/routes/dns/+page.ts` (lines 1–59)  
**Severity:** Critical — Architectural violation  

**What's wrong:**
```typescript
const DNS_PATHS = {
  zones: '/api/v1/dns/zones',
  endpoints: '/api/v1/dns/catalog',
  drift: '/api/v1/dns/drift',
  policies: '/api/v1/dns/policies'  // This route doesn't even exist in the router!
};
```

The SvelteKit page load function issues four parallel `fetch()` calls to REST endpoints on initial page load. This is the consumer side of the REST violation.

Additionally, `policies: '/api/v1/dns/policies'` references a route that **does not exist** in `router.go` — it will always 404.

**Recommendation:**  
Replace with a Nostr WebSocket subscription. The page should connect to a relay, subscribe to Kind 31976 events filtered by the Bahia service pubkey, and reactively update the UI as events arrive. Use EOSE to mark historical catch-up complete. This aligns with the Nostr-native architecture.

---

### CRIT-4: Svelte DNS State Store Uses REST Fetch

**File:** `web/src/lib/stores/dns.svelte.js` (lines 1–129)  
**Severity:** Critical — Architectural violation  

**What's wrong:**
```javascript
const API_PREFIX = '/api/v1';

async function request(path, fetcher = fetch) {
  const response = await fetcher(`${API_PREFIX}${path}`, {
    headers: { Accept: 'application/json' }
  });
  // ...
}
```

The entire state store is built around imperative REST fetches:
- `fetchZones()` → `GET /dns/zones`
- `fetchEndpoints()` → `GET /dns/catalog?...`
- `fetchDrift()` → `GET /dns/drift`
- `fetchPolicies()` → `GET /dns/policies`

The `+page.svelte` has a manual "Refresh" button (line 149) and `refreshTab()` function (line 95) that re-fetches data — this is the "waiting and checking" pattern explicitly forbidden by AGENTS.md.

**Recommendation:**  
Rewrite the store to use a Nostr relay WebSocket subscription. The store should maintain a live subscription to Kind 31976 events and update state reactively as events arrive and change. The "Refresh" button becomes unnecessary — data updates automatically via the subscription. The `requestSeq` / race-condition handling becomes unnecessary when events are streamed.

---

## Major Violations

### MAJ-1: Subscription Refresh Ticker in Resolver

**File:** `pkg/discovery/resolver.go` (lines 49–55, 227–232)  
**Severity:** Major — Anti-pattern (polling-adjacent)  

**What's wrong:**
```go
func WithRefreshInterval(d time.Duration) Option {
    return func(r *Resolver) {
        r.refreshInterval = d
    }
}

// In run():
if r.refreshInterval > 0 {
    ticker = time.NewTicker(r.refreshInterval)
    refresh = ticker.C
}
// ...
case <-refresh:
    resubscribe = true
```

When `refreshInterval > 0`, the resolver periodically tears down its subscription and reissues it on a timer. This is a polling-style refresh mechanism. AGENTS.md says: *"Timers are not allowed for: event delivery, relay response waiting, completion detection."*

A Nostr subscription is long-lived by design. If the concern is stale state after reconnect, the correct approach is to handle EOSE for backfill + keep the subscription open for realtime — which the code already does in the non-refresh path.

**Mitigating factors:** The default is `refreshInterval: 0` (disabled), so this is opt-in. But the mechanism exists and could be activated.

**Recommendation:**  
Remove `WithRefreshInterval` and the refresh ticker entirely. If subscription staleness is a concern, rely on relay reconnect with backoff (which the outer loop already handles when the events channel closes). Document that relay reconnect is the staleness recovery mechanism.

---

## Minor Violations

### MIN-1: Missing NIP-11 Relay Metadata Check in Resolver

**File:** `pkg/discovery/resolver.go`  
**Severity:** Minor — Missing Nostr pattern  

**What's wrong:**  
The resolver connects to relays and subscribes to Kind 31976 events without first querying NIP-11 relay metadata. AGENTS.md requires: *"query NIP-11 relay metadata before assuming capabilities."*

Compare with `internal/fipsbridge/bridge.go` line 189 (`fetchRelayMetadata`) which correctly queries NIP-11.

**Recommendation:**  
Add NIP-11 metadata fetching before subscribing, similar to `bridge.go`'s `fetchRelayMetadata()`. Log warnings if a relay doesn't support the required kind.

---

### MIN-2: Missing Full Event Validation in Resolver

**File:** `pkg/discovery/resolver.go` (lines 270–300)  
**Severity:** Minor — Incomplete validation  

**What's wrong:**  
The resolver's `endpointFromEvent()` validates:
- ✅ Event kind
- ✅ Author pubkey
- ✅ Event ID hash (NIP-01 `CheckID()`)
- ✅ Schnorr signature (`CheckSignature()`)
- ✅ Future timestamp skew
- ✅ Required `d` tag
- ✅ Content JSON parsing

This is thorough. However, compared to the FIPS subscriber which uses the shared `ValidateInboundEvent()` helper (line 255 in `fips_subscriber.go`), this resolver has its own inline validation. If `ValidateInboundEvent` is the canonical validator, the resolver should use it for consistency.

**Recommendation:**  
Consider refactoring to use the shared `ValidateInboundEvent()` helper from the nostr adapter package, or verify that the inline validation covers all the same checks.

---

### MIN-3: Frontend References Non-Existent `/dns/policies` Route

**File:** `web/src/routes/dns/+page.ts` (line 13)  
**Severity:** Minor — Dead code / partial integration  

**What's wrong:**
```typescript
policies: '/api/v1/dns/policies'
```

This route does not exist in `router.go`. The request will always fail (silently caught by `Promise.allSettled`). The policies tab will always show "No DNS policies projected yet."

**Recommendation:**  
Either register the route (if policies are supposed to be served via REST — though that would be another CRIT violation), or note that this is part of the larger "replace REST with Nostr subscriptions" remediation.

---

## Clean Code — Files That Correctly Follow Nostr-First Patterns

### ✅ `internal/adapters/nostr/fips_subscriber.go`
**Exemplary Nostr-native implementation.** This file is a model for how to build Nostr subscribers:
- Scoped filter with `kinds` and `d` tag (line 232)
- Full event validation via shared `ValidateInboundEvent()` (line 255)
- Proper EOSE handling — both per-relay and aggregate (lines 206–215)
- CLOSED handling with AUTH attempt on `auth-required` reason (lines 236–250)
- Reconnect with exponential backoff via `DefaultBackoff()` (line 178)
- Idempotent event handling — dedup by pubkey allowlist, replaceable event semantics via d-tag (line 275)
- Pubkey normalization handles both npub and hex (lines 362–396)
- Clean lifecycle: `Start()`/`Stop()` with WaitGroup (lines 140–165)
- No polling, no timeouts for event delivery, no fake request/response

### ✅ `internal/fipsbridge/bridge.go`
**Solid Nostr-native bridge daemon:**
- NIP-11 relay metadata fetching (line 189, `fetchRelayMetadata`)
- Scoped subscription filter with kind + author + optional tag filters (line 226)
- EOSE handling — logs historical catch-up completion (line 248)
- CLOSED handling with AUTH retry (lines 262–280)
- Event validation via shared `ValidateInboundEvent()` (line 284)
- Dedup by event ID (line 290) and replaceable event coordinate semantics (lines 291–295)
- Reconnect with exponential backoff capped at 30s (lines 182–192)
- Atomic hosts file writes (line 296)
- `time.After` used only for reconnect backoff delay — allowed per AGENTS.md

### ✅ `internal/fipsbridge/hosts.go`
**Clean infrastructure code.** Atomic file writes, managed section parsing, no protocol violations.

### ✅ `internal/fipsbridge/bridge_test.go` & `hosts_test.go`
**Deterministic tests** — inject events directly, verify state, no sleeps, no async waiting.

### ✅ `internal/adapters/nostr/fips_subscriber_test.go`
**Deterministic tests** — direct `handleEvent()` calls with signed test events, assertion on repo state.

### ✅ `internal/adapters/dns/fips.go`
**Clean backend adapter.** Manages FIPS hosts file entries. No protocol violations — this is a local file adapter, not a network component.

### ✅ `internal/adapters/dns/fips_test.go`
**Thorough deterministic tests** for hosts file read/write/sync operations.

### ✅ `internal/reconcile/dns_projector.go`
**Clean projection logic.** This is a pure read-model projector that derives DNS endpoints from infrastructure state. No protocol violations — it's a computation layer, not a network layer. Properly includes mesh endpoint projection via FIPS overlay addresses.

### ✅ `internal/domain/dns.go`
**Clean domain model.** Proper types, validation, coordinate generation. Includes `ZoneVisibilityMesh` and `DNSBackendTypeFIPS` for FIPS support.

### ✅ `internal/domain/worker.go`
**Clean domain model.** Includes `FIPSOverlayAddr`, `FIPSEndpoints`, `MeshHealth` fields for FIPS integration. No protocol violations.

### ✅ `internal/config/config.go`
**Clean configuration.** `FIPSConfig` and `DNSConfig` (including `MeshZone`, `MeshEndpoints`) are well-structured with proper validation.

### ✅ `internal/app/app.go` (FIPS wiring)
**Clean wiring.** FIPS subscriber is created with the relay pool, registered as a background runner, and properly configured from `FIPSConfig`. No violations.

### ✅ `cmd/fips-bahia-bridge/main.go`
**Clean CLI entry point.** Proper signal handling, config loading, no violations.

### ✅ `internal/mcp/dns_tools.go`
**Acceptable MCP layer.** The DNS tools expose read-model queries and async command publishing (with idempotency keys). MCP is a tool interface for AI agents, not a client-facing REST API — tools that publish Nostr events with receipt tracking are architecturally appropriate.

### ✅ `internal/mcp/dns_resources.go`
**Acceptable MCP layer.** Exposes DNS endpoints as MCP resources. Same rationale as tools.

### ✅ `internal/api/handlers/mcp.go`
**Acceptable transport.** The MCP JSON-RPC handler is a transport mechanism for MCP protocol compliance. It's not a REST API for DNS data — it's a JSON-RPC transport that happens to use HTTP. The DNS tools/resources are accessed through this transport. This is architecturally distinct from the REST catalog violation.

### ✅ `internal/adapters/nostr/projector.go` (npub/mesh tags)
**Clean Nostr projection.** Line 1808 adds `npub` and `mesh` tags to projected Kind 31976 events when `WorkerPubkey` is set — correctly tagging FIPS-capable endpoints in the Nostr event stream. The `time.NewTicker` at line 364 is for a repair/reconciliation interval, which is an allowed timer use per AGENTS.md ("health checks / heartbeats").

---

## Timer Usage Audit

AGENTS.md allows timers for: reconnect backoff, health checks/heartbeats, autoscaling, outbound rate limiting.

| File | Timer | Usage | Verdict |
|------|-------|-------|---------|
| `fips_subscriber.go:185` | `time.After(delay)` | Reconnect backoff delay | ✅ Allowed |
| `bridge.go:187` | `time.After(backoff)` | Reconnect backoff delay | ✅ Allowed |
| `resolver.go:230` | `time.NewTicker(refreshInterval)` | Periodic subscription refresh | ❌ **MAJ-1** — polling-adjacent |
| `projector.go:364` | `time.NewTicker(repairInterval)` | Repair/reconciliation cycle | ✅ Allowed (health check) |

---

## Summary of Required Actions

| ID | Severity | File(s) | Action |
|----|----------|---------|--------|
| CRIT-1 | Critical | `internal/api/handlers/dns_catalog.go` | Remove entirely — replace with Nostr Kind 31976 subscription on client |
| CRIT-2 | Critical | `internal/api/router/router.go` (lines 254–259) | Remove DNS route registrations |
| CRIT-3 | Critical | `web/src/routes/dns/+page.ts` | Replace REST fetches with Nostr relay WebSocket subscription |
| CRIT-4 | Critical | `web/src/lib/stores/dns.svelte.js` | Replace REST store with reactive Nostr subscription store |
| MAJ-1 | Major | `pkg/discovery/resolver.go` | Remove `WithRefreshInterval` and refresh ticker |
| MIN-1 | Minor | `pkg/discovery/resolver.go` | Add NIP-11 metadata query before subscribing |
| MIN-2 | Minor | `pkg/discovery/resolver.go` | Use shared `ValidateInboundEvent()` for consistency |
| MIN-3 | Minor | `web/src/routes/dns/+page.ts` | Remove dead `/dns/policies` reference (or include in CRIT-3 remediation) |

**CRIT-1 through CRIT-4 are a single coherent violation:** the REST DNS catalog pipeline. The fix is to remove the server-side REST layer and replace the client-side fetches with direct Nostr relay subscriptions. This is the highest-priority remediation.
