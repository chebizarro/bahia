# Phase 5: CLI, pkg/client, MCP — Design

- Status: proposed (2026-10-02)
- Issue: bahia-irsry.13
- Depends on: bahia-irsry.11 (Phase 3, in progress), bahia-irsry.12 (Phase 4, web; designed in parallel)
- Blocks: bahia-irsry.11.19 (ContextVM CRUD handler deletion)
- Audit: `docs/investigations/nostr-first-architecture-audit-2026-09-29.md` — findings C-29, C-38; root causes RC-1, RC-4
- Charter: `docs/architecture.md` invariants 1–7
- Phase 2 baseline: local bbolt store (`internal/adapters/nostr/localstore`), per-(relay, filter) cursors, NIP-77 sync, single relay stack, outbox with per-relay OK tracking
- Phase 3 baseline: intent processor, TrustSet, bounded 30315 intent-status, dual-dispatch, `nostr.intent_domains` config, warm-start

---

## 1. Read Path

### 1.1 Problem

Today the CLI reads everything over REST from the daemon (`pkg/client/client.go:473–891`): `ListServices`, `ListEnvironments`, `ListStates`, `ListDriftedStates`, `ListWorkers`, `GetRunLogs`, `ListPolicies`, `ListSecrets`, `ListOrgs`, `ListConfigDrift`, etc. (C-29). The MCP server reads from Postgres repositories while its writes are signed relay requests (C-38). Both depend on Postgres and the daemon's REST API being available.

### 1.2 Target: REQ against a local eventstore

**Decision: reads become `REQ` against a per-process local bbolt eventstore, populated from relay subscriptions with `since = persisted cursor`.**

The CLI and MCP both use the daemon's relay (the sidecar) as their event source. Phase 2's `localstore.Store` already provides:
- `SaveEvent` with replaceable-event collapsing and NIP-09 deletion support
- `QueryEvents(filter)` with paged iteration
- `Cursor(relayURL, filterHash)` / `AdvanceCursor` for per-(relay, filter) cursor persistence
- `PruneRegularEvents(cutoff)` for bounded storage

The canonical state for every domain is a `30900` event signed by the service pubkey. A read is:
```
REQ {kinds:[30900], authors:[<service-pubkey>], "#domain":["service"]}
```
filtered locally after EOSE.

### 1.3 CLI read flow

For each CLI read command (e.g. `bahia services list`):

```
1. Open local store
   └─ Path: $BAHIA_DATA_DIR/store/ or $XDG_DATA_HOME/bahia/store/
   └─ Per-service-pubkey namespace to avoid cross-daemon contamination

2. Connect to relay pool
   └─ Same relay resolution as today (--relay, BAHIA_NOSTR_RELAYS, bootstrap discovery)

3. Subscribe with cursor
   └─ filter = {kinds:[30900], authors:[<service-pubkey>], "#domain":[<domain>], since: cursor}
   └─ Store each event via localstore.SaveEvent
   └─ Advance cursor on each event

4. Wait for EOSE
   └─ Timeout: --eose-timeout (default 5s, env BAHIA_EOSE_TIMEOUT)
   └─ EOSE from ≥1 relay = "caught up"
   └─ Timeout without EOSE = query stale local store, print warning to stderr

5. Query local store
   └─ QueryEvents with domain-specific filter
   └─ Decode 30900 content into domain structs
   └─ Apply CLI-side filtering (--org, --name, etc.)

6. Render and exit
   └─ Output table/json/yaml as today
   └─ Close pool and store
```

**Freshness guarantee:** EOSE means "the relay has sent everything matching this filter." For one-shot CLI commands this is sufficient — the relay holds the authoritative canonical state (published by the daemon). The `since = cursor` ensures incremental sync; the first run fetches everything, subsequent runs fetch only changes.

**NIP-77 sync:** Optional, via `--negentropy` flag. Useful for repair after local store corruption or long offline periods. Not default because CLI invocations are typically short-lived and the cursor-based catch-up is sufficient.

### 1.4 Encrypted reads (secrets, org membership)

Secrets and org membership are stored as NIP-59 gift-wrapped events. The CLI reads these by:

1. Subscribing to `{kinds:[1059], "#p": [<operator-pubkey>], since: cursor}` — events gift-wrapped to the operator.
2. Unwrapping with the operator's NIP-44 decrypt (local nsec or NIP-46 bunker — the bunker signer already implements `Encrypt`/`Decrypt`, see `cmd/cli/operator_nostr.go:cliEncryptedCapableSigner`).
3. Verifying the inner event's signature and storing the decrypted inner event in the local store (the local store is per-user, so decrypted content stays local).

For secrets, the inner `30900` content is itself NIP-44-encrypted to the service pubkey (double encryption per Phase 3 §1.7). The CLI can read the _metadata_ (name, scope) from tags but cannot decrypt the _value_ without the service key. Secret value reveal remains a ContextVM request to the daemon, unchanged.

### 1.5 Domain-to-filter mapping

| Domain | Filter | Notes |
|--------|--------|-------|
| Services | `{kinds:[30900], authors:[svc], "#domain":["service"]}` | |
| Environments | `{kinds:[30900], authors:[svc], "#domain":["environment"]}` | |
| State | `{kinds:[30900], authors:[svc], "#domain":["state"]}` | Includes drift detection |
| Workers | `{kinds:[30900], authors:[svc], "#domain":["worker"]}` | Or Loom adverts (kind 31990) |
| Policies | `{kinds:[30900], authors:[svc], "#domain":["policy"]}` | |
| DNS | `{kinds:[30900], authors:[svc], "#domain":["dns"]}` | Zones, endpoints, backends, policies |
| LLM | `{kinds:[30900], authors:[svc], "#domain":["llm"]}` | Routes, releases, state |
| ML | `{kinds:[30900], authors:[svc], "#domain":["ml"]}` | Models, versions, endpoints |
| Builds | `{kinds:[30900], authors:[svc], "#domain":["build"]}` | |
| Artifacts | `{kinds:[30900], authors:[svc], "#domain":["artifact"]}` | |
| Packages | `{kinds:[30900], authors:[svc], "#domain":["package"]}` | |
| Backups | `{kinds:[30900], authors:[svc], "#domain":["backup"]}` | |
| Orgs | `{kinds:[1059], "#p":[operator]}` | Unwrap, filter domain=org |
| Secrets (metadata) | `{kinds:[1059], "#p":[operator]}` | Unwrap, filter domain=secret |
| Notifications | `{kinds:[1059], "#p":[operator]}` | Unwrap, filter domain=notification |
| Config-fabric drift | `{kinds:[30900], authors:[svc], "#domain":["config-fabric"]}` | Desired vs applied comparison |
| Intent status | `{kinds:[30315], authors:[svc], "#p":[operator]}` | For mutation feedback |

### 1.6 Logs and SSE

Deployment run logs (`GetRunLogs`) and live log streaming (`StreamLiveLogs`) are fundamentally HTTP-native (SSE/chunked transfer). These **stay as REST reads** against the daemon. The daemon keeps the `/api/v1/deployments/runs/{id}/logs` and `/api/v1/services/{id}/environments/{envId}/logs` endpoints. Run logs are Blossom-stored content, not relay events.

### 1.7 `pkg/client` changes

`pkg/client` gains a new `NostrClient` alongside the existing HTTP `Client`:

```go
// NostrClient reads Bahia state from relays via a local eventstore.
type NostrClient struct {
    store       *localstore.Store
    pool        *nostr.SimplePool   // Phase 2 pool
    servicePub  nostr.PubKey
    relays      []string
    eoseTimeout time.Duration
    signer      nostr.Signer        // for encrypted domain reads
}

func NewNostrClient(cfg NostrClientConfig) (*NostrClient, error)
func (c *NostrClient) Close() error

// Read methods — replace REST equivalents
func (c *NostrClient) ListServices(ctx context.Context) ([]domain.Service, error)
func (c *NostrClient) GetService(ctx context.Context, id string) (*domain.Service, error)
// ... one per domain read
```

The HTTP `Client` is retained until all callers migrate. Then deleted (§5).

---

## 2. Mutations

### 2.1 Today's mutation path

The CLI's mutation commands go through `operator_nostr.go` → `pkg/client.OperatorControlPlaneClient` → ContextVM `25910` request/response over relays. The daemon receives the `25910`, processes it in `encrypted_route_handlers.go`, and returns a `25910` response. The CLI already has the signer model (local nsec, NIP-46 bunker) and relay resolution.

### 2.2 Target: direct 30900 intent publication

**Decision: CLI mutations sign and publish `30900` intent events directly, then wait for bounded `30315` intent-status.**

```
1. Build intent
   └─ Mint intent_id (UUIDv7)
   └─ Construct 30900 event per Phase 3 §1.3 (domain, schema, op, org, d tags)
   └─ Content = full desired state (level-triggered, §1.2)

2. Sign
   └─ Local nsec: event.Sign(secretKey)
   └─ NIP-46 bunker: signer.SignEvent(ctx, &event)

3. Gift-wrap (sensitive domains only)
   └─ Secrets, org membership, notifications: NIP-59 wrap to service pubkey
   └─ Per Phase 3 §1.7

4. Publish
   └─ Outbox-style: publish to all configured relays
   └─ Track per-relay OK responses
   └─ At least one OK required (fail if all relays reject)

5. Wait for intent-status
   └─ Subscribe: {kinds:[30315], authors:[service-pubkey],
   │    "#p":[operator-pubkey], "#intent_id":[intent_id]}
   └─ Timeout: --result-timeout (default 30s, env BAHIA_RESULT_TIMEOUT)
   └─ Parse status: accepted | rejected | conflict | superseded

6. Exit
   └─ accepted → exit 0, print canonical state
   └─ rejected/conflict → exit 1, print reason
   └─ timeout → exit 2, print "intent published but no status within timeout"
   └─ superseded → exit 1, print "a newer intent won"
```

**Error and exit-code table:**

| Context | Code | Meaning | Client action |
|---------|------|---------|---------------|
| CLI exit | `0` | Intent accepted, canonical state confirmed | — |
| CLI exit | `1` | Intent rejected, conflict, or superseded | Read error message; for conflict, re-read entity and retry |
| CLI exit | `2` | Intent published to relay but no `30315` status within `--result-timeout` | Intent may still be processing; check with `bahia outbox list` or re-run |
| CLI exit | `3` | No relay accepted the event (all OK=false) | Check relay connectivity and sidecar write policy |
| ContextVM JSON-RPC | `-32011` | Request already accepted; response cannot be replayed (no idempotency key, or execution interrupted) | Retry with the same idempotency key to get the stored response, or use a new key to re-execute |
| ContextVM JSON-RPC | `-32600` | Invalid request (malformed, missing required fields) | Fix request and retry |
| MCP tool result | `{"status":"pending"}` | Intent published but `30315` status not received within timeout | Poll via `get_intent_status` tool with the returned `intent_id`, or retry |


### 2.3 Key/signer model

The CLI already supports both local nsec and NIP-46 bunker signers (`cliNIP46Signer` in `operator_nostr.go`). Phase 5 reuses this infrastructure without changes to the key management code. The signer interface `nostr.Signer` (with `Encrypt`/`Decrypt` on the `cliEncryptedCapableSigner` extension) covers all Phase 5 signing needs.

### 2.4 Relationship to ContextVM operator client

The existing `pkg/client.OperatorControlPlaneClient` with its `CreateServiceNostr`, `UpdateServiceNostr`, etc. methods wraps ContextVM `25910` request/response. Phase 5 replaces this with a new `IntentPublisher` that signs `30900` intents directly. The old operator client methods are deleted per slice (§9).

### 2.5 `expected_updated_at` for conflict detection

For updates, the CLI reads the current canonical state (§1), extracts `updated_at`, and includes it as `expected_updated_at` in the intent content. The daemon's intent processor checks this against its local state (Phase 3 §1.5). A mismatch produces a `conflict` status event. The CLI prompts: "Entity was modified since your last read. Re-read and retry."

### 2.6 Idempotency

Each intent carries an `intent_id` (UUIDv7). The daemon's idempotency store (Phase 3 §3.2 step 1) prevents double-processing. If the CLI times out and retries, it re-publishes the same `30900` event (same `intent_id`); the daemon either skips it (already processed) or processes it once.

---

## 3. MCP

### 3.1 Today's MCP architecture

The MCP server (`internal/mcp/server.go`) runs in-daemon, wired as `deps.MCP` on the REST router's `/mcp` endpoint. It has ~60 tool handlers. Read tools call `s.registry.GetService(...)`, `s.registry.ListEnvironments(...)`, etc. — all Postgres-backed. Write tools go through `Publish*Request` publishers that emit ContextVM `25910` commands or signed relay events.

### 3.2 Decision: in-daemon MCP, local-store reads

**All MCP tools remain in-daemon.** The MCP server gains a `localstore.Store` dependency and reads from it instead of Postgres repositories.

```go
type Server struct {
    // Phase 5: primary read path
    store        *localstore.Store
    servicePub   nostr.PubKey

    // Phase 5: primary write path
    intentProc   *controlplane.IntentProcessor

    // Retained: domain services for non-event reads (logs, payments)
    registry     *service.RegistryService  // shrinks per slice
    // ...
}
```

**Read tools** (e.g. `handleListServices`, `handleGetService`): query `store.QueryEvents(filter)` with the same domain-to-filter mapping as §1.5, decode `30900` content into the MCP result format.

**Write tools** (e.g. `handleCreateService`, `handleDeploy`): call `intentProc.ProcessInProcess(ctx, intent)` — the same in-process path Phase 3's dual-dispatch uses. This means MCP writes go through the same pipeline, with the same authorization and idempotency guarantees, without a relay round-trip.

### 3.3 Out-of-process MCP

An out-of-process MCP server (e.g. a standalone `bahia-mcp` binary) would use `pkg/client.NostrClient` for reads and `pkg/client.IntentPublisher` for writes — the same code paths as the CLI. This design does not implement the standalone binary but ensures the code paths are factored for reuse.

### 3.4 ContextVM -32011 handling, idempotency keys, and progressToken (bahia-irsry.48 item 2)

**`-32011` is an existing code.** `ContextVMDuplicateRequestErrorCode` (`internal/controlplane/contextvm_local_run.go:89`) means "this request was already accepted but its response cannot be replayed" — either the request had no idempotency key, or its execution never completed (crash/restart). It is a ContextVM request-ledger concept and must not be reused for intent-status timeout.

**Decision: every remaining ContextVM call from CLI and MCP must carry an idempotency key (`_meta.progressToken` or explicit `idempotency_key`). Intent-status timeout is not a JSON-RPC error.**

#### Remaining ContextVM calls (assistant, secret-reveal, log-fetch)

These stay as `25910` request/response. Each call **must** include an idempotency key so that:
- If the daemon crashes mid-processing, a retry with the same key replays the stored response instead of returning `-32011`.
- The CLI and MCP must handle `-32011` by prompting: "request was interrupted; retry with the same idempotency key, or use a new key to re-execute."

The CLI generates a UUIDv7 idempotency key per command invocation and passes it as `_meta.progressToken` in the ContextVM request. If the user retries the same logical operation, the CLI can accept an `--idempotency-key` flag to reuse the prior key.

The MCP server passes the MCP-layer `progressToken` (when present) as the ContextVM `_meta.progressToken`. If no `progressToken` is provided by the MCP client, the server mints a UUIDv7. This ensures every ContextVM call is keyed.

#### Intent-based MCP write tools (new Phase 5 path)

For MCP tools that route through `IntentProcessor.ProcessInProcess`:
1. Processing is synchronous (in-process) — returns the result immediately.
2. If the tool must wait for a relay-published intent's `30315` status (out-of-process path, §3.3), it:
   - Sends `notifications/progress` with the `progressToken` from the MCP request, reporting "intent published, awaiting daemon processing."
   - Waits for the `30315` intent-status event (same subscription as §2.2 step 5).
   - On timeout, returns a **successful result** with `{"status": "pending", "intent_id": "<uuidv7>", "event_id": "<hex>"}`. The MCP client can poll via `get_intent_status` or retry. This is not an error — the intent was accepted by the relay.

### 3.5 Which tools stay ContextVM

After Phase 5, the only ContextVM tools are:
- **Assistant chat/transcript**: streaming responses, not event-based state
- **Secret value reveal**: service-key decryption, not available to clients
- **Deployment run log fetch**: Blossom-stored content, streamed via ContextVM for daemon-side authentication

Everything else becomes local-store read + intent-processor write.

---

## 4. Config-Fabric Publication

### 4.1 Today

`bahia config publish` calls `apiClient.PublishConfig(ctx, request)` → `POST /api/v1/config-fabric/events` → the daemon signs and publishes a config-fabric desired-state event. The daemon acts as a signing proxy.

### 4.2 Target: operator-signed config events

**Decision: the CLI signs and publishes config-fabric desired-state events directly, using the operator's key.**

The implemented config-fabric consumer uses NIP-51 kind `30000` for membership
lists and NIP-78 kind `30078` for policy documents (not a `30900` intent).
Both are addressable desired-state events signed by the operator:
```json
{
  "kind": 30078,
  "pubkey": "<operator-pubkey>",
  "tags": [
    ["d", "service:<service-id>:<policy-name>"],
    ["service", "<service-id>"],
    ["scope", "<scope>"],
    ["schema", "cascadia.config.<policy-name>.v1"],
    ["version", "3"]
  ],
  "content": "{\"service_id\":...,\"scope\":...,\"version\":3,\"schema\":...,\"policy\":{...}}"
}
```

The relay-sidecar config consumer watches these desired-state events and emits
schema-v3 config-status `30900` records at the stable
`config-status:<service>:<policy>:<scope>` coordinate. They are not `30315`
intent-status records. The daemon's configured fleet-ops pubkeys and explicit
sidecar config-trusted pubkeys authorize these two config kinds only.

### 4.3 Config rollback

`bahia config rollback <event-id>` reads the prior desired-state event from the
local store or CLI outbox by event ID, requires the same operator author,
increments the version for the operator's `(service, policy, scope)` coordinate,
and publishes a new desired-state event with the old content. No REST call needed.

### 4.4 Config drift

`bahia config drift` reads both desired-state and applied-state config events from the local store and computes drift locally. The drift computation is a simple comparison: for each `(service, policy, scope)` coordinate, compare the desired version/event-id against the applied version/event-id.

---

## 5. REST Deletion Plan

### 5.1 Deletion order

REST read routes are deleted per domain **after** both the CLI (this phase) and the web (Phase 4) no longer call them. Each deletion is a separate slice.

| Priority | REST routes | CLI replacement | MCP replacement | Condition |
|----------|-------------|-----------------|-----------------|-----------|
| 1 | `GET /services`, `GET /services/{id}` | `NostrClient.ListServices` | `store.QueryEvents` | P5 slice R1 |
| 2 | `GET /environments`, `GET /environments/{id}` | `NostrClient.ListEnvironments` | `store.QueryEvents` | P5 slice R1 |
| 3 | `GET /state`, `GET /state/drifted`, env/service state | `NostrClient.ListStates` | `store.QueryEvents` | P5 slice R2 |
| 4 | `GET /policies`, `GET /policies/{id}` | `NostrClient.ListPolicies` | `store.QueryEvents` | P5 slice R2 |
| 5 | `GET /workers`, `GET /workers/{pubkey}` | `NostrClient.ListWorkers` | `store.QueryEvents` | P5 slice R3 |
| 6 | `GET /builds/{id}`, service builds | `NostrClient.ListBuilds` | `store.QueryEvents` | P5 slice R3 |
| 7 | `GET /artifacts/{id}`, service artifacts | `NostrClient.ListArtifacts` | `store.QueryEvents` | P5 slice R3 |
| 8 | `GET /orgs`, members, invites | `NostrClient` (encrypted) | `store.QueryEvents` (encrypted) | P5 slice R4 |
| 9 | `GET /notifications/channels`, log | `NostrClient` (encrypted) | `store.QueryEvents` (encrypted) | P5 slice R4 |
| 10 | Secrets list (metadata only) | `NostrClient` (encrypted) | `store.QueryEvents` (encrypted) | P5 slice R4 |
| 11 | `GET /config-fabric/drift` | Local computation | Local computation | P5 slice R5 |
| 12 | LLM routes/releases/state, ML reads | `NostrClient` | `store.QueryEvents` | P5 slice R5 |
| 13 | SBOM, signatures reads | `NostrClient` | `store.QueryEvents` | P5 slice R5 |
| 14 | `POST /config-fabric/events`, `POST /config-fabric/rollback` | Direct intent publish | Intent processor | P5 slice C1 |
| 15 | Build write routes, ML/LLM write routes | Intent publish | Intent processor | P5 slice W1 |
| 16 | `--http-fallback` flag and HTTP `Client` | Deleted | — | P5 final |

### 5.2 What the daemon keeps

After all Phase 5 deletions, the daemon's HTTP surface is:

| Endpoint | Purpose | Why it stays |
|----------|---------|-------------|
| `GET /health` | Liveness probe | Kubernetes/monitoring |
| `GET /ready` | Readiness probe | Kubernetes/monitoring |
| `GET /metrics` | Prometheus metrics | Observability |
| `/v2/*` | OCI registry proxy | HTTP-native protocol (Docker registry v2) |
| `GET /blossom/blob/{hash}` | Blossom blob download | Content-addressable HTTPS proxy |
| `POST /blossom/*` (NIP-98) | Blossom upload | NIP-98 authenticated HTTP upload |
| Live logs SSE | `GET /services/{id}/environments/{envId}/logs` | SSE streaming, HTTP-native |
| Run logs | `GET /deployments/runs/{id}/logs` | Blossom-stored content fetch |
| Legacy reconciliation | `POST /soulfactory/legacy-reconciliation/*` | One-time migration tool |
| DNS provider callbacks | Provider-specific webhook endpoints | External provider requirement |

### 5.3 `pkg/client` deletion

After all REST reads are replaced by `NostrClient` and all REST writes are replaced by `IntentPublisher`:
1. Delete all `Client.List*`, `Client.Get*`, `Client.Create*`, `Client.Set*`, `Client.Delete*` methods from `pkg/client/client.go`.
2. Delete `Client.do`, `Client.applyAuthorization`, `apiResponse` — the HTTP request infrastructure.
3. Delete `NIP98PrivateKeyProvider`, `NIP98SignerProvider` — NIP-98 HTTP auth is no longer needed for CLI-to-daemon communication.
4. Retain `NormalizeNostrPrivateKey` — still used for key input validation.
5. Delete `OperatorControlPlaneClient` and all `*Nostr` methods in `pkg/client/operator_nostr.go` — replaced by `IntentPublisher`.
6. Delete `contextvm_request.go`, `contextvm_cipher.go`, `contextvm_result_delivery_e2e_test.go`, `contextvm_wrap_size_test.go`.
7. Rename or keep `pkg/client` for the `NostrClient` and `IntentPublisher`.

### 5.4 `cmd/cli` deletion

1. Delete `serverURL`, `apiClient`, `operatorHTTPFallback` variables from `main.go`.
2. Delete `--server`, `--http-fallback` flags.
3. Delete all `apiClient.List*`/`apiClient.Get*` calls in CLI commands — replace with `NostrClient` calls.
4. Delete all `run*Nostr` functions in `operator_nostr.go` — replace with `IntentPublisher` calls.
5. Delete `buildCLIOperatorClient` — replaced by direct intent publishing with the signer.
6. Retain signer resolution (`resolveNostrPrivateKeyInput`, `resolveNIP46OperatorInput`) — still needed.
7. Retain relay resolution — still needed.

---

## 6. Cut-over: Deleting ContextVM CRUD Handlers (bahia-irsry.11.19)

### 6.1 Prerequisite: Phase 4 + Phase 5 complete

The ContextVM CRUD handlers in `internal/controlplane/encrypted_route_handlers.go` serve three callers:
1. **Web** — migrated in Phase 4 to sign intents directly from the browser.
2. **CLI/pkg/client** — migrated in Phase 5 to sign intents directly.
3. **MCP** — migrated in Phase 5 to use in-process `IntentProcessor.ProcessInProcess`.

When all three callers have migrated for a domain, the ContextVM handler for that domain is dead code.

### 6.2 Deletion sequence

Phase 3 already deletes ContextVM mutation handlers per domain slice (Phase 3 §7, slice R1). Phase 5's contribution is:
1. The CLI no longer sends `25910` for any domain.
2. The MCP no longer routes through ContextVM for any domain.

The `nostr.intent_domains` config list is made **default-on** (all domains) when Phase 5 completes. The config key is then deleted.

### 6.3 What ContextVM keeps

After R1 (Phase 3 Wave 6), ContextVM retains only:
- **Assistant** — `assistant/chat`, `assistant/transcript`: streaming chat responses, not entity CRUD.
- **Secret value reveal** — `secrets/reveal`: service-key decryption, not available to external clients.
- **Deployment run log fetch** — `logs/stream`: Blossom-stored content, streamed via ContextVM for daemon-side authentication.

These are not CRUD handlers and have no intent equivalent. They remain on ContextVM indefinitely.

---

## 7. Failed Local Outbox Inspection (bahia-irsry.50 item 5)

### 7.1 Problem

The local outbox (`internal/adapters/nostr/localstore/outbox.go`) tracks events that failed to publish. Today there is no user-facing way to inspect or retry these entries.

### 7.2 CLI view

**New command: `bahia outbox`**

```
bahia outbox list              # List pending and failed outbox entries
bahia outbox list --failed     # List only failed entries
bahia outbox list --pending    # List only pending entries
bahia outbox retry <event-id>  # Re-enqueue a failed entry for retry
bahia outbox retry --all       # Retry all failed entries
bahia outbox prune             # Prune old settled entries
bahia outbox counts            # Show pending/failed counts
```

**Implementation:**
- The CLI opens the same outbox bbolt database the daemon uses (or a separate CLI-local outbox for CLI-published events).
- `outbox list` calls `Outbox.ListPending(target, cursor, limit)` and displays: event ID, entity type, entity ID, state, enqueued time, last error, relay delivery status.
- `outbox list --failed` filters to `state = "failed"`.
- `outbox retry` re-enqueues the failed entry by resetting its state to pending.
- `outbox counts` calls `Outbox.Counts()` and prints pending/failed totals.

For CLI-published events (Phase 5 intents), the CLI maintains its own outbox:
- On publish, the event goes into the CLI's outbox before any relay attempt.
- Per-relay OK tracking resumes on retry.
- `bahia outbox list` shows the CLI's outbox by default; `--daemon` flag shows the daemon's outbox (requires filesystem access to the daemon's data directory).

### 7.3 MCP tool

A new MCP read tool `outbox_status` exposes the daemon's outbox counts and failed entries to AI agents, enabling automated diagnosis of publish failures.

---

## 8. Decisions (binding)

| # | Question | Decision | Rationale |
|---|----------|----------|-----------|
| 1 | CLI local store vs daemon-proxied reads | **CLI-local bbolt store**, populated from relay subscriptions | CLI should work without the daemon running, just with relay access. Matches the Nostr-first architecture: every client is sovereign (C-29 fix) |
| 2 | CLI one-shot freshness model | **EOSE from ≥1 relay = fresh.** Stale local store with warning if no EOSE within timeout | EOSE is the relay's guarantee of completeness. The cursor makes subsequent invocations incremental. No polling or sleep loops |
| 3 | MCP in-daemon or out-of-process | **In-daemon.** Reads from shared localstore, writes via in-process IntentProcessor | In-daemon avoids a relay round-trip for writes. The same code paths (NostrClient, IntentPublisher) enable a future out-of-process binary |
| 4 | Config-fabric publication | **Operator-signed event, directly to relays.** No daemon signing proxy | The operator _is_ the authority for desired config. Daemon signing was a workaround for the CLI not having a Nostr publisher (now it does) |
| 5 | Logs and SSE | **Stay as REST/HTTP.** Not migrated to relay events | Streaming and content-addressable blob download are HTTP-native. No relay kind needed |
| 6 | `--http-fallback` sunset | **Deleted when Phase 5 completes.** No HTTP fallback after intent publishing is proven | The flag exists only for the ContextVM→REST transition. Direct intent publishing removes both ContextVM and REST |
| 7 | Secret value reveal | **Stays ContextVM.** Not intent-based | Service-key decryption requires daemon involvement. Not a CRUD operation |
| 8 | ContextVM `intent_domains` default | **Default-on (all domains)** when Phase 5 completes. Then config key deleted | Phase 4+5 ensure all callers sign intents directly. The config key is a migration aid, not a permanent feature |

---

## 9. Slice Plan

### Wave 1: Foundation (CLI Nostr reads + intent publisher)

| Slice | Goal | Done-when | Key files (ownership) | Parallel-safe with | Deletes |
|-------|------|-----------|----------------------|--------------------|---------| 
| **N1: NostrClient for pkg/client** | `NostrClient` type with local store, pool, cursor-based sync, EOSE wait, domain-to-filter mapping. Shared 30900→domain decoder | Unit tests: (a) subscribe→EOSE→query returns service list matching REST output; (b) cursor persistence — second invocation fetches only new events; (c) EOSE timeout — stale store queried with warning | `pkg/client/nostr_client.go` (new), `pkg/client/event_decoder.go` (new), `pkg/client/nostr_client_test.go` (new) | N2, N3 | Nothing yet |
| **N2: IntentPublisher for pkg/client** | `IntentPublisher` type: build 30900 intent, sign (nsec or NIP-46), gift-wrap for encrypted domains, publish to relays with OK tracking, subscribe for 30315 status, exit code mapping | Unit tests: (a) intent published and status received → exit 0; (b) rejection status → exit 1; (c) timeout → exit 2; (d) idempotent retry with same intent_id; (e) gift-wrapped intent for secret domain | `pkg/client/intent_publisher.go` (new), `pkg/client/intent_publisher_test.go` (new) | N1, N3 | Nothing yet |
| **N3: CLI outbox inspection** | `bahia outbox list/counts/retry/prune` commands. CLI-local outbox at `$XDG_DATA_HOME/bahia/outbox.bolt`; `--daemon` flag for read-only inspection of the daemon outbox. MCP `bahia_outbox_status` tool (counts-only default, `include_details` for authorized callers). **N2 enqueue API:** `localstore.OpenOutbox(cliOutboxDefaultPath())` then `Outbox.Enqueue(OutboxEntry{...})` — CLI outbox path is `$XDG_DATA_HOME/bahia/outbox.bolt` (fallback `$HOME/.local/share/bahia/outbox.bolt`) | Unit tests: (a) enqueue+list pending; (b) list failed filters; (c) retry re-enqueues; (d) counts match; (e) --daemon read-only; (f) prune dry-run | `cmd/cli/outbox.go`, `cmd/cli/outbox_test.go` (new), `internal/adapters/nostr/localstore/outbox.go` (extended), `internal/mcp/outbox_tools.go` (new), `internal/mcp/server.go` (OutboxReader dep) | N1, N2 | Nothing yet |

### Wave 2: CLI read migration (services, environments, state, policies)

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------| 
| **R1: Services + environments reads** | `bahia services list/get` and `bahia environments list/get` use `NostrClient`. Old REST calls removed from CLI | CLI service/env commands produce identical output from Nostr reads vs prior REST reads (golden test). `pkg/client.Client.ListServices`, `GetService`, `ListEnvironments`, `GetEnvironment`, `GetEnvironmentDetails` marked deprecated | `cmd/cli/main.go` (service/env read commands), `pkg/client/client.go` (deprecation marks) | R2 | `apiClient.ListServices` call in CLI, `apiClient.GetService` call, `apiClient.ListEnvironments`, `apiClient.GetEnvironment` |
| **R2: State + policies reads** | `bahia state list/drifted` and `bahia policies list/get` use `NostrClient` | State and policy CLI commands use Nostr reads. REST calls removed from CLI | `cmd/cli/main.go` (state/policy read commands) | R1 | `apiClient.ListStates`, `apiClient.ListDriftedStates`, `apiClient.ListPolicies`, `apiClient.GetPolicy` calls in CLI |

### Wave 3: CLI mutation migration + config-fabric

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------| 
| **M1: Service/environment mutations via intents** | `bahia services create/update` and `bahia environments create/update` use `IntentPublisher` instead of `OperatorControlPlaneClient` | Service/env create/update via intent publish with 30315 status wait. No ContextVM 25910. Test: create service, receive accepted status, verify canonical 30900 on local store | `cmd/cli/main.go` (service/env write commands), `cmd/cli/operator_nostr.go` (service/env `run*Nostr` functions deleted) | M2, C1 | `runServiceCreateNostr`, `runServiceUpdateNostr`, `runEnvironmentCreateNostr`, `runEnvironmentUpdateNostr` in `operator_nostr.go`. `CreateServiceNostr`, `UpdateServiceNostr`, `CreateEnvironmentNostr`, `UpdateEnvironmentNostr` in `pkg/client/operator_nostr.go` |
| **M2: Deployment + runtime mutations via intents** | `bahia deploy`, `bahia rollback`, `bahia services deploy/restart/stop` use `IntentPublisher` | Deploy/rollback/runtime actions via intent publish. `--http-fallback` becomes no-op for these commands | `cmd/cli/main.go` (deploy commands), `cmd/cli/operator_nostr.go` (deploy `run*Nostr` functions deleted) | M1, C1 | `runDeploymentIntentNostr`, `runRollbackIntentNostr`, `runRuntimeActionNostrFirst`, `runDeploymentApprovalNostr`, deploy/rollback ContextVM methods in `pkg/client/operator_nostr.go` |
| **C1: Config-fabric direct publish** | `bahia config publish` signs and publishes config-fabric events directly. `bahia config rollback` reads prior event and re-publishes. `bahia config drift` reads from local store | Config publish/rollback work without REST. Drift computed locally. Test: publish config event, verify it appears on relay and daemon applies it | `cmd/cli/config_fabric.go` (rewritten), `pkg/client/client.go` (`PublishConfig`, `RollbackConfig`, `ListConfigDrift` deprecated) | M1, M2 | `apiClient.PublishConfig`, `apiClient.RollbackConfig`, `apiClient.ListConfigDrift` REST calls. `POST /config-fabric/events`, `POST /config-fabric/rollback` REST routes in `router.go` |

### Wave 4: Remaining CLI reads + MCP migration

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------| 
| **R3: Workers + builds + artifacts reads** | `bahia workers list/get`, build/artifact reads use `NostrClient` | Workers/builds/artifacts CLI reads from Nostr. REST calls removed | `cmd/cli/main.go`, `cmd/cli/builds.go`, `cmd/cli/artifacts.go` | R4, P1 | `apiClient.ListWorkers`, `apiClient.GetWorker`, build/artifact REST read calls |
| **R4: Orgs + secrets + notifications reads** | `bahia orgs list/get/members`, `bahia secrets list` use `NostrClient` with encrypted domain unwrapping | Encrypted domain reads work from local store. Secret value reveal stays ContextVM | `cmd/cli/main.go` (org/secret commands) | R3, P1 | `apiClient.ListOrgs`, `apiClient.GetOrg`, `apiClient.ListOrgMembers`, `apiClient.ListSecrets` REST calls |
| **P1: MCP reads from local store** | MCP read tools (`handleListServices`, `handleGetService`, etc.) read from `store.QueryEvents` instead of `s.registry.*` | MCP read tools return identical results from local store vs Postgres. Test: MCP `list_services` returns same data from Nostr store as from Postgres repo | `internal/mcp/server.go` (read tool handlers), `internal/mcp/nostr_reads.go` (new, shared 30900→MCP result decoder) | R3, R4 | `s.registry.GetService`, `s.registry.ListEnvironments`, etc. repository calls in MCP read tools |

### Wave 5: MCP writes + remaining mutations + REST cleanup

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------| 
| **P2: MCP writes via IntentProcessor** | MCP write tools (`handleCreateService`, `handleDeploy`, etc.) call `intentProc.ProcessInProcess` instead of `Publish*Request` | MCP write tools use intent processor. progressToken reported during async waits. Test: MCP `create_service` → intent processed → canonical state returned | `internal/mcp/server.go` (write tool handlers) | M3, D1 | `ServiceCommandPublisher`, `PolicyCommandPublisher`, and other ContextVM-based `Publish*` interfaces from MCP `ServerDeps`. `agent_async_tools.go` publish functions for migrated domains |
| **M3: Remaining CLI mutations** | DNS, adoption, policy, build, artifact, package, org, secret, notification, worker mutations via intents | All CLI mutations use `IntentPublisher`. No `OperatorControlPlaneClient` calls remain | `cmd/cli/operator_nostr.go` (remaining functions), `cmd/cli/dns.go`, `cmd/cli/soulfactory.go` | P2, D1 | All remaining `run*Nostr` functions. `OperatorControlPlaneClient` type in `pkg/client/operator_nostr.go`. `cliOperatorClient` interface in `cmd/cli/operator_nostr.go` |
| **D1: REST read route deletion** | Delete all `/api/v1` read routes for domains now served by local store. Keep logs, health, metrics, OCI, Blossom | No `/api/v1/services`, `/api/v1/environments`, etc. read routes. Tests verify 404. Router handler construction simplified | `internal/api/router/router.go` (route deletions), `internal/api/handlers/*.go` (handler deletions) | P2, M3 | All `GET /api/v1/*` routes except logs. Handler files: `services.go`, `environments.go`, `state.go`, `policies.go`, `workers.go`, etc. `RouterDeps` fields for deleted domains |

### Wave 6: Final cleanup

| Slice | Goal | Done-when | Key files | Parallel-safe with | Deletes |
|-------|------|-----------|-----------|--------------------|---------| 
| **F1: REST write route deletion** | Delete remaining REST write routes (builds, ML, LLM, config-fabric, SBOM, tools). Keep only HTTP-native boundaries | Write routes return 404. Test: all deprecated mutation routes rejected | `internal/api/router/router.go` (write route deletions) | F2, F3 | `POST /builds`, `PATCH /builds/{id}/status`, `POST /ml/*`, `PUT /llm/routes/{id}`, `POST /tools/denylist`, `POST /deployments/runs`, `POST /deployments/runs/{id}/complete` |
| **F2: pkg/client HTTP cleanup** | Delete HTTP `Client`, NIP-98 providers, `OperatorControlPlaneClient`, ContextVM code | `pkg/client` exports only `NostrClient`, `IntentPublisher`, `NormalizeNostrPrivateKey`. No HTTP client code remains | `pkg/client/client.go` (deleted), `pkg/client/operator_nostr.go` (deleted), `pkg/client/contextvm_*.go` (deleted), `pkg/client/operator_discovery.go` (retained if needed for relay discovery) | F1, F3 | `Client` struct, `NIP98PrivateKeyProvider`, `NIP98SignerProvider`, `OperatorControlPlaneClient`, all `*Nostr` methods, `contextvm_*.go` files |
| **F3: CLI cleanup and `--http-fallback` deletion** | Delete `--server`, `--http-fallback` flags. Delete `apiClient` global. Clean up `operator_nostr.go` to only contain signer resolution | CLI uses only Nostr flags (--relay, --nostr-key-file, etc.). No HTTP flags remain. `buildCLIOperatorClient` deleted | `cmd/cli/main.go` (flag deletions, command rewiring), `cmd/cli/operator_nostr.go` (shrinks to signer resolution only) | F1, F2 | `serverURL`, `apiClient`, `operatorHTTPFallback` globals. `--server`, `--http-fallback` flags. `configureClientAuth`, `configureNIP46HTTPClientAuth` functions. `fallbackOrError`, `isPreAcceptanceOperatorFailure`, `rawTargetRequiresFallbackError` |
| **F4: `intent_domains` default-on** | Make `nostr.intent_domains` default to all domains. Delete the config key after verification | All domains processed via intents by default. Config key removed. Test: daemon starts without `intent_domains` config and processes intents for all domains | `internal/config/*.go` (config key deletion), `internal/controlplane/intent_subscriber.go` (default-on logic) | — | `nostr.intent_domains` config parsing. Conditional checks on `intent_domains` list |

---

## 10. Acceptance Tests

### 10.1 CLI reads from relay (Wave 2)

```
Test: CLIServiceListMatchesRelayState

Setup:
1. Daemon running with services published as 30900 events.
2. CLI configured with --relay pointing to daemon's sidecar.

Steps:
1. Run `bahia services list --output json` (new Nostr path).
2. Run the same query via REST (legacy path).

Assert:
- Both outputs contain the same service list (modulo field ordering).
- The CLI's local store has a persisted cursor.
- A second `bahia services list` fetches only events after the cursor.
```

### 10.2 CLI mutation via intent (Wave 3)

```
Test: CLIServiceCreateViaIntentWithStatusWait

Setup:
1. Daemon running with intent_domains including "service".
2. CLI configured with --nostr-key-file and --relay.

Steps:
1. Run `bahia services create --name test-svc --artifact-repo test`.
2. Observe stderr for "→ intent published, waiting for status..."
3. Daemon processes intent, publishes 30315 status and canonical 30900.

Assert:
- CLI exits with code 0.
- CLI stdout contains the created service details.
- The canonical 30900 event is on the relay.
- The 30315 status event has status=accepted.
- A second create with the same intent_id is idempotent (daemon skips, CLI gets
  accepted status from the existing 30315).
```

### 10.3 MCP reads from local store (Wave 4)

```
Test: MCPListServicesReturnsLocalStoreData

Setup:
1. Daemon with local store populated with service 30900 events.
2. MCP server wired to the local store.

Steps:
1. Call MCP tool `list_services`.

Assert:
- Returns the same service list as a direct store query.
- No Postgres repository call was made.
```

### 10.4 Config-fabric direct publish (Wave 3)

```
Test: CLIConfigPublishDirectlyToRelay

Setup:
1. Daemon with config-fabric consumer watching relays.
2. CLI configured with operator signer.

Steps:
1. Run `bahia config publish --file config.json`.
2. CLI signs a 30900 config-fabric event and publishes to relay.

Assert:
- CLI exits with code 0.
- The config event appears on the relay, signed by the operator.
- The daemon's config-fabric consumer detects it and applies the config.
- `bahia config drift` shows no drift for the published config.
```

### 10.5 Failed outbox inspection (Wave 1)

```
Test: CLIOutboxListShowsFailedEntries

Setup:
1. CLI outbox with one published and one failed entry.

Steps:
1. Run `bahia outbox list`.
2. Run `bahia outbox list --failed`.
3. Run `bahia outbox counts`.

Assert:
- `list` shows both entries with their state.
- `list --failed` shows only the failed entry with its last error.
- `counts` shows pending=0, failed=1.
```

### 10.6 End-to-end: no REST after Phase 5 (Wave 6)

```
Test: DaemonServesNoRESTReadRoutesAfterPhase5

Setup:
1. Daemon with all Phase 5 slices applied.

Steps:
1. HTTP GET /api/v1/services → 404.
2. HTTP GET /api/v1/environments → 404.
3. HTTP GET /api/v1/state → 404.
4. HTTP GET /health → 200.
5. HTTP GET /ready → 200.
6. HTTP GET /api/v1/deployments/runs/{id}/logs → 200 (retained).

Assert:
- All domain read routes return 404.
- Health, readiness, and log routes still work.
```

---

## 11. Risks

- **CLI store size.** A busy fleet may produce many 30900 events. Mitigation: the local store already supports `PruneRegularEvents(cutoff)`; the CLI prunes on startup (keep last 7 days of regular events; replaceables are never pruned — they collapse by design).
- **EOSE latency.** If the relay is slow or the cursor is far behind, the CLI blocks on EOSE. Mitigation: `--eose-timeout` with a sensible default (5s); the CLI renders from the stale store with a warning, so the user always gets output.
- **Encrypted domain complexity.** NIP-59 unwrapping adds latency and complexity to reads. Mitigation: the CLI caches decrypted inner events in its local store (per-user, local-only). Subsequent reads hit the cached plaintext.
- **MCP write latency.** In-process intent processing is synchronous, but if the domain handler has slow side effects (e.g. runtime deploy), the MCP tool blocks. Mitigation: the intent processor returns promptly (intent accepted); side effects are async. The MCP tool returns the intent status, and the agent can poll for completion via the `get_deployment_status` tool.
- **Dual-client window.** During migration, some CLI commands use `NostrClient` while others still use the HTTP `Client`. Mitigation: the wave plan migrates all reads before all writes, and each wave is a clean cut per domain group. No command uses both clients simultaneously.
- **Config-fabric operator key trust.** Today the daemon signs config events. After Phase 5, the operator signs them. The daemon's config-fabric consumer must trust the operator's pubkey. Mitigation: the consumer uses the `TrustSet` from Phase 3 — the same trust model that authorizes intents. The operator must be in the trust set (authorized_pubkeys or org member) to publish config events.

---

## 12. Open Questions

| # | Question | Impact | Proposed default |
|---|----------|--------|-----------------|
| 1 | Should the CLI store path be configurable per-service-pubkey, or one global store? | Multi-daemon operators may need separate stores | Per-service-pubkey namespace within one store path. Default: `$XDG_DATA_HOME/bahia/store/<service-pubkey-prefix>/` |
| 2 | Should `bahia outbox retry` retry all failed entries or require an event ID? | UX for operators with many failed entries | Support both: `bahia outbox retry --all` and `bahia outbox retry <event-id>` |
| 3 | Should the MCP outbox_status tool expose individual failed entries or just counts? | Security — failed entries may contain intent content | Counts only by default; `include_details: true` parameter for authorized callers |
| 4 | When should `--http-fallback` stop working? After Wave 3 (mutations migrated) or Wave 6 (full cleanup)? | Operator migration timeline | After Wave 5 (MCP + remaining mutations migrated). Wave 6 deletes the flag and its code |
| 5 | Should the CLI's local outbox be the same bbolt file as the daemon's, or separate? | File locking, multi-process access | Separate. CLI outbox at `$XDG_DATA_HOME/bahia/outbox/`. Daemon outbox at its configured data dir. `bahia outbox list --daemon` reads the daemon's outbox (read-only) |
