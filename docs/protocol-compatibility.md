# Bahia Protocol Compatibility Matrix

This document summarizes the protocols and event families Bahia currently uses and how they fit the production product shape.

## Important scope note

Bahia is not REST-only and no longer uses Bahia-specific request/status/result/read-model kinds as the live public control-plane contract.

Current production shape:

- Bahia is a **deployment/runtime control plane**.
- The **relay sidecar is the primary realtime/public boundary**.
- Browser/operator identity is **signer-first**.
- Mutation intent uses **ContextVM JSON-RPC over Nostr kind `25910`**, normally encrypted with CEP-4 / NIP-59 wrappers (`1059` or `21059`).
- Long-running truth is observed through **canonical Nostr observables** (`30900`, `4903`, `30315`, `11316`-`11320`, `30002`, `30078`, and relevant standard NIPs).
- REST and HTTP MCP remain compatibility/tooling surfaces, but they must return Nostr correlation metadata for long-running work.

For the canonical control-plane contract, prefer:

- `docs/control-planes.md`
- `docs/nostr-commands.md`
- `docs/event-spec.md`
- `docs/user-guide/nostr-integration.md`

---

## Quick Reference

| Protocol / surface | Purpose | Current status |
|--------------------|---------|----------------|
| ContextVM over Nostr | Canonical mutation intent and JSON-RPC acknowledgments | ✅ primary |
| Canonical Nostr observables | State, audit, status, discovery, relay topology | ✅ primary |
| Relay sidecar | Browser/backend public relay boundary | ✅ primary |
| CEP-4 / NIP-59 gift-wrap | Sensitive ContextVM message encryption | ✅ primary for sensitive mutation payloads |
| NIP-98 | Bahia HTTP authentication and OCI push auth | ✅ implemented |
| NIP-05 | Identity enrichment / verification | ✅ implemented |
| NIP-46 | Signer / bunker support | ✅ implemented in Signet + browser signer flows; some CLI-specific auth UX remains compatibility work |
| NIP-51 / NIP-65 | Relay sets and relay list metadata | ✅ canonical bootstrap/routing inputs |
| NIP-34 | Repository relay hints and repository state | ✅ repository/ngit-specific routing input |
| NIP-11 / NIP-66 | Relay metadata and optional monitor events | ✅ advisory capability/liveness metadata only |
| NIP-86 + NIP-98 | Optional relay-owner HTTP administration with signed HTTP authorization | 🟡 optional administration surface, not ContextVM transport |
| Loom | Distributed job execution protocol | ✅ implemented as external protocol interop |
| Hive-CI | Workflow event ingestion protocol | ✅ implemented as external protocol interop |
| OCI Distribution API | Registry push/pull | ✅ implemented |
| Blossom | Blob/log storage backend | ✅ implemented |
| Cashu | Worker payment surface | ✅ implemented |
| REST API | Narrowed CRUD/query/log compatibility surface | ✅ implemented |
| HTTP MCP | Tooling surface with async Nostr correlation metadata | ✅ implemented |

---

## Control-Plane Transport Hierarchy

### 1. ContextVM over Nostr

ContextVM kind `25910` is the canonical mutation request/response envelope. Bahia method names use `<domain>/<operation>`; examples include `service/deploy`, `worker/cordon`, `dns/zone-create`, `backup/run`, `ml/recipe-run`, and `adoption/import`.

Sensitive messages should be wrapped with CEP-4 / NIP-59 gift-wrap (`1059` or `21059`). The verified inner ContextVM event pubkey is the authorization subject after unwrap.

A ContextVM response is command acknowledgment only. Clients must follow canonical observables for progress, terminal state, audit, and convergence.

### 2. Canonical Nostr observables

| Kind(s) | Purpose |
|---------|---------|
| `30900` | Canonical control-plane state projection |
| `30078` | NIP-78 app-specific configuration, registries, and UI/operator projection data |
| `30315` | NIP-38 operational status |
| `4903` | Immutable audit fact / attestation / provenance breadcrumb |
| `11316`-`11320` | ContextVM discovery and capability announcements |
| `30002` | NIP-51 relay sets; canonical Bahia bootstrap relay topology |
| `10002` | NIP-65 service relay preferences; advisory wider-Nostr read/write hints only |
| `5` | NIP-09 deletion events |

### 3. Relay preferences and bootstrap

Bahia publishes service-key NIP-51 kind `30002` relay sets for canonical bootstrap and topology. `bahia-browser-v1` remains the browser-safe discovery/read set, `bahia-contextvm-v1` is the preferred ContextVM request/reply set, and `bahia-service-v1` is the backend service publish/backfill set.

The Bahia service key also publishes an advisory NIP-65 kind `10002` relay list. Its `r` tags mark ContextVM request relays as `read` and service publish/backfill relays as `write`, giving wider Nostr clients standard author-routing hints. This event does not authorize relay use, does not replace ContextVM discovery, and must not replace the NIP-51 `30002` relay sets for Bahia bootstrap.

### 4. REST and HTTP MCP

- HTTP MCP (`/mcp`, `/api/v1/mcp`) exposes the same tool surface and must return Nostr correlation metadata for long-running work.
- REST remains for narrowed CRUD/query/log/registry compatibility.
- HTTP responses must not claim long-running completion when the durable truth is relay-delivered canonical observables.
- Fallback to REST after a signed ContextVM event has been accepted by any relay is unsafe and must be avoided.

---

## Legacy Bahia Custom Kinds

Historical Bahia-specific ranges are retained only for startup migration, historical conversion tests, and fail-closed fixtures:

| Legacy family | Historical purpose | Canonical target |
|---------------|--------------------|------------------|
| `5961`-`6006`, `38390`-`38431`, `5102` | CRU/request operations | ContextVM `25910` methods, or NIP-09 `5` for deletion semantics |
| `6961`-`6997` | progress/status | `30315`, `4903`, correlated ContextVM responses, or domain observables |
| `7961`-`7997` | terminal results | ContextVM responses plus `30900`/`4903`/`30315` observables |
| `31961`-`32003`, `31974` | read models/discovery | `30900`, `30078`, `11316`-`11320`, or `30002` depending on semantics |
| worker cleanup lifecycle | Fleet Health cleanup status | `30900` with `schema=bahia.state.worker-cleanup.v1`; no legacy live command kind is introduced |
| `31000`-`31024`, `31310`-`31311` | audit/activity | `4903` |
| `5980`, `7980` | encrypted request/result envelope | CEP-4 / NIP-59 `1059` or `21059` around ContextVM `25910` |
| `31100`-`31105` | deprecated bridge commands | removed; no live canonical runtime path |

Production clients must not publish or subscribe to these numbers as runtime contracts. Legacy discovery kind `31974` is historical/migration-only; production bootstrap uses ContextVM discovery `11316`-`11320` plus NIP-51 relay sets `30002`.

---

## Startup Migration App Compatibility

Bahia ships a startup migration app in `internal/nostrmigration` so deployed relays and local repositories can be converted without keeping legacy runtime support in the core app.

Migration behavior:

1. Scan the local Nostr event repository for `LegacyKinds()`.
2. Optionally backfill legacy events from configured relays and wait for `EOSE`.
3. Resolve each legacy event to a canonical disposition.
4. Skip if a canonical event already exists with `migrated-from=<legacy_event_id>` for the target kind.
5. Build a canonical event tagged with `migration=bahia-nostr-native-v1`, `legacy-kind`, `migrated-from`, `schema`, `domain`, and layer metadata.
6. Sign with the Bahia service private key.
7. Publish to relays and accept both fresh `OK` acknowledgments and duplicate `OK` acknowledgments as success.
8. Record the canonical event locally and log a summary.

The app is idempotent and may run every startup. Non-dry-run migration requires a publisher and service private key. Relay backfill failures, missing `EOSE`, or missing signing configuration are deployment issues to fix; they are not reasons to restore legacy subscribers.

---

## External Protocol Interop

### Loom Protocol Events

Bahia integrates with Loom as an external distributed compute protocol. These kinds are not Bahia legacy control-plane ranges.

| Kind | Name | Direction | Description |
|------|------|-----------|-------------|
| `10100` | Worker Advertisement | Inbound | Replaceable event advertising worker capabilities |
| `5100` | Job Request | Outbound | Request to execute a deployment/build job |
| `30100` | Job Status Update | Inbound | Loom progress update |
| `5101` | Job Result | Inbound | Final Loom job result |

If Bahia exposes Loom-derived state to Bahia clients, it projects that state into canonical observables (`30900`, `30315`, `4903`, or `30078`).

### Hive-CI Protocol Events

Bahia's CI/deployment bridge is aligned to the Hive-CI protocol and its Loom execution hand-off.

| Kind | Name | Direction | Description |
|------|------|-----------|-------------|
| `5401` | Workflow Run | Inbound | Trusted CI trigger / workflow-run fact |
| `5402` | Workflow Result | Inbound | Trusted build outcome fact |
| `5100` | Loom Job Request | Outbound | Actual compute dispatch for build/deploy work |

`5900` is not part of Bahia's Hive-CI integration; it belongs to an older NIP-90 DVM CI runner path.

---

## Browser and Agent Expectations

Clients and agents should:

1. Bootstrap via ContextVM discovery (`11316`-`11320`) and NIP-51 relay sets (`30002`).
2. Publish mutations as ContextVM `25910`, encrypted with `1059`/`21059` for sensitive payloads.
3. Require relay `OK` for the signed event.
4. Subscribe to canonical observables with scoped filters before or immediately after publishing.
5. Wait for `EOSE` for historical catch-up, then keep subscriptions open.
6. Treat ContextVM responses as acknowledgments, not durable long-running completion.
7. Avoid REST polling and legacy-kind subscriptions for runtime truth.

