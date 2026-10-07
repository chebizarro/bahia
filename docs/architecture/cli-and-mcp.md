# CLI, `pkg/client` and MCP

The CLI (`cmd/cli`, binary `bahia`) and the daemon's MCP server
(`internal/mcp`) are Nostr clients: reads are REQ subscriptions into a local
event store, mutations are signed intents, and only three interactive
operations use ContextVM. `pkg/client` holds the reusable pieces
(`NostrClient`, `IntentPublisher`, the ContextVM request client and the
`30900` decoders).

## Reads: `NostrClient`

- A per-process **bbolt local event store** at
  `$XDG_DATA_HOME/bahia/store/<service-pubkey-prefix>/events.db`
  (`internal/adapters/nostr/localstore`), namespaced by service pubkey so two
  daemons never mix.
- Relays are resolved from `--relay` / `BAHIA_NOSTR_RELAYS`, or discovered from
  `--bootstrap-relay` / `BAHIA_NOSTR_BOOTSTRAP_RELAYS` (trusted operator relay
  discovery, `pkg/client/operator_discovery.go`).
- Each command subscribes with `since = persisted cursor` per (relay, filter):
  `{kinds:[30900], authors:[servicePubkey], "#t":[<topics>]}` for canonical
  state and `{kinds:[30000, 30078]}` for config-fabric documents. Filters scope
  by the single-letter `#t` topic only. Events are saved as they arrive and the
  cursor advances.
- **EOSE from ≥ 1 relay means fresh.** The wait is bounded by
  `--eose-timeout` / `BAHIA_EOSE_TIMEOUT` (default 5 s); on timeout the command renders from the stale
  store and warns on stderr. There are no polling or sleep loops.
- Confidential domains (orgs, members, secret metadata, notification channels,
  payments, security) are decrypted with the operator's signer through the OCK
  envelopes (`pkg/client/confidential_read.go`), see
  [confidential state](confidential-state.md).
- Deployment run logs and live logs are HTTP (see "Daemon HTTP surface").

## Writes: `IntentPublisher`

`bahia services create`, `bahia deploy`, `bahia dns ...` and every other
mutation build a `30900` intent ([intents and authority](intents-and-authority.md)),
sign it with the local key (`--nostr-key-file`, `BAHIA_NOSTR_KEY_FILE`,
`BAHIA_NOSTR_NSEC`, `BAHIA_NOSTR_PRIVATE_KEY`) or a NIP-46 bunker, gift-wrap it
for sensitive domains, enqueue it in the **CLI outbox**
(`$XDG_DATA_HOME/bahia/outbox.bolt`) before the first relay attempt, publish
with per-relay OK tracking, and then subscribe for the `30315` status at
`intent-status:<operator>:<coordinate>` for up to `--result-timeout`
(`BAHIA_RESULT_TIMEOUT`, default 30 s). `--idempotency-key` reuses an
`intent_id` so a retry is idempotent; for updates the CLI copies the canonical
record's `updated_at` into `expected_updated_at`.

| Exit code | Meaning |
|---|---|
| `0` | Status `accepted`; canonical state printed |
| `1` | `rejected`, `conflict` (re-read and retry) or `superseded` |
| `2` | Published to a relay but no status within the timeout; the intent may still be processing — `bahia outbox list` or re-run |
| `3` | No relay accepted the event (all `OK false`); check connectivity and the sidecar write policy |

`bahia outbox` inspects and retries the CLI outbox (`--daemon` reads the
daemon's outbox read-only) — see [outbox delivery](outbox-delivery.md).

## Config fabric

`bahia config publish --file <json>` signs NIP-51 `30000` lists and NIP-78
`30078` policy documents with the operator key and publishes them directly;
`bahia config rollback <event-id>` re-publishes a prior document by the same
author at the next version; `bahia config drift` compares desired and applied
config-status records from the local store.

## ContextVM

Three operations remain request/response over ContextVM (`25910` in NIP-59
gift wrap, `pkg/client/contextvm_request_client.go`): assistant turns, secret
value reveal (`services/secrets-reveal`) and deployment run log fetch
(`deployments/run-logs-get`). Every call carries an idempotency key
(`_meta.progressToken`, a UUIDv7 minted per invocation). JSON-RPC `-32011`
means the request was accepted but its response cannot be replayed (no key, or
execution interrupted): retry with the **same** key to replay, or a new key to
re-execute. `-32600` is a malformed request. The
`TestArchitectureContextVMMethodConstants` ratchet keeps this surface closed:
new mutations are intents, never new ContextVM methods.

## MCP

The MCP server runs **in the daemon** (`POST /mcp`, HTTP JSON-RPC, platform
admin, behind the DB gate) and shares its local event store and `IntentProcessor`:

- Read tools query the local event store with the same `30900` decoders as
  the CLI.
- Write tools build the intent with `IntentPublisher.BuildIntentEvent`, then
  call `IntentProcessor.ProcessInProcess` — the same validation, authorization
  and idempotency as a relay intent, with no relay round trip. The authenticated
  HTTP caller is the intent's actor. A rejected or conflicting intent returns
  `{"status":"rejected"|"conflict", ...}`; otherwise the tool returns
  `{"status":"pending","intent_id","event_id"}` plus, for status-only tools,
  the `30315` coordinate and the outcome read back from the store.
- `bahia_outbox_status` / `bahia_outbox_retry` expose the daemon outbox.
- The tool catalog is documented in `docs/user-guide/mcp-tools.md`.

## Daemon HTTP surface

The daemon serves exactly these HTTP routes (`internal/api/router/router.go`);
everything else returns 404:

| Route | Purpose |
|---|---|
| `GET /health`, `GET /ready` | Liveness; readiness with the health checks (`intent_readiness`, `relay_quorum`, `canonical_delivery`, `ock_rotation`, ...) |
| `GET /metrics` | Prometheus (behind auth when enabled) |
| `POST /mcp` | MCP JSON-RPC (platform admin) |
| `GET /api/v1/deployments/runs/{id}/logs` | Run logs (Blossom-stored content) |
| `GET /api/v1/services/{id}/environments/{envId}/logs` | Live logs (SSE) |
| `GET /api/v1/deployments/runs/{id}/cost`, `GET /api/v1/payments/history` | Payment reads answered from the local event store (no database) |
| `GET /api/v1/config-fabric/drift` | Config-fabric drift (platform admin) |
| `GET /api/v1/blossom/blob/{hash}` | Content-addressed blob proxy (unauthenticated; verifiable by hash) |
| `POST/DELETE .../managed-instances/{unit}/maintenance` | Managed-instance maintenance toggle |
| `POST /api/v1/artifacts/{id}/sbom` | SBOM import |
| `POST /api/v1/soulfactory/legacy-reconciliation/{preview,apply}` | Legacy Soul reconciliation (platform admin, dry-run first) |
| Virtualization routes (`RegisterVirtualizationRoutes`) | VM runtime reads |
| `/v2/*` | OCI registry proxy (when configured) |

`TestArchitectureDBLessDaemonBootGatesNilRepositoryRoutes` keeps every
database-backed route behind the DB gate so a daemon without Postgres boots
and serves relay-derived state.
