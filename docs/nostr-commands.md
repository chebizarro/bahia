# Bahia Nostr Control-Plane Events

Bahia's production Nostr control plane is now ContextVM-first. Mutation intent uses ContextVM JSON-RPC kind `25910`, usually encrypted with ContextVM CEP-4 / NIP-59 wrappers (`1059` or `21059`). Long-running truth is observed through canonical Nostr events, not through legacy Bahia request/status/result kind families.

Legacy Bahia kinds such as `5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `38390`-`38431`, `5980`, and `7980` are migration inventory only. New clients must not publish or subscribe to those numbers as production runtime contracts.

## Production Kind Families

| Family | Kind(s) | Direction | Purpose |
|--------|---------|-----------|---------|
| ContextVM intents | `25910` | inbound/outbound | JSON-RPC mutation requests, immediate acknowledgments, and responses |
| Encrypted ContextVM transport | `1059`, `21059` | inbound/outbound | CEP-4 / NIP-59 gift-wrap envelopes around inner ContextVM messages |
| ContextVM discovery | `11316`-`11320` | outbound replaceable | Server, tools, resources, templates, and prompts announcements |
| Canonical state | `30900`, `30078` | outbound replaceable/addressable | Control-plane state projections and NIP-78 app-specific data |
| Canonical audit/status | `4903`, `30315` | outbound | Immutable audit facts and NIP-38 operational statuses |
| Relay sets | `30002` | outbound addressable | NIP-51 relay topology and bootstrap sets |
| Relay preferences | `10002` | outbound replaceable | Advisory NIP-65 service read/write hints; not Bahia bootstrap |
| DM relay lists | `10050` | optional replaceable | NIP-51 DM receive routing only for explicitly configured DM-enabled features and identities |
| Repository state | `30617`, `30618` | inbound/outbound by repository owner | NIP-34 repository announcements and state; repository relay hints are preferred for repository operations |
| Deletions | `5` | outbound/inbound | NIP-09 delete events where relay-level deletion semantics apply |

## ContextVM Mutation Methods

ContextVM methods use the `<domain>/<operation>` convention. The relay indexes the transport; Bahia interprets the JSON-RPC method and params after signature verification and, when encrypted, after unwrap.

| Domain | Example methods |
|--------|-----------------|
| `service` | `deploy`, `rollback`, `restart`, `stop`, `update`, `delete` |
| `environment` | `create`, `update`, `delete` |
| `artifact` | `register` |
| `policy` | `create`, `update`, `delete`, `evaluate` |
| `worker` | `cordon`, `uncordon`, `drain`, `undrain`, `maintenance-enter`, `maintenance-exit`, `labels-update`, `policy-apply` |
| `llm` / `ml` | `route-create`, `release-register`, `deploy`, `approve`, `rollback`, `model-import`, `recipe-run`, `inference-deploy` |
| `dns` | `zone-create`, `policy-apply`, `record-set`, `drift-remediate`, `backend-register` |
| `backup` | `run`, `restore`, `verify`, `retention-enforce`, `repository-probe` |
| `adoption` | `scan`, `import` |
| `assistant` | `prompt`, `approve`, `cancel` |
| `ci` | `workflow-run`, `cancel`, `retry` |

## Example: Deploy Service

Inner ContextVM request:

```json
{
  "kind": 25910,
  "pubkey": "<operator-pubkey>",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "service/deploy"],
    ["service", "api"],
    ["environment", "prod"],
    ["artifact", "api:v2"]
  ],
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"deploy-api-prod-01\",\"method\":\"service/deploy\",\"params\":{\"service_id\":\"api\",\"environment_id\":\"prod\",\"artifact_id\":\"api:v2\",\"_meta\":{\"progressToken\":\"deploy-api-prod-01\"}}}"
}
```

When sensitive, publish that inner message as a CEP-4 / NIP-59 gift wrap (`1059` or `21059`) tagged to the Bahia service pubkey.

## Observable Follow-up

A successful ContextVM response is an acknowledgment that Bahia accepted or rejected the command intent. It is not long-running completion. Clients must follow canonical observables:

```json
{
  "kinds": [30900, 30315, 4903],
  "authors": ["<bahia-service-pubkey>"],
  "#service": ["api"],
  "#environment": ["prod"]
}
```

Use these rules:

1. Subscribe with scoped filters before publishing when practical.
2. Process stored events until `EOSE` for historical catch-up.
3. Keep the subscription open for realtime convergence.
4. Deduplicate by event id.
5. Apply replaceable semantics for `(kind, pubkey, d)` on `30900`, `30078`, `11316`-`11320`, and `30002`.
6. Treat relay `OK`, `CLOSED`, and `AUTH` messages as protocol outcomes, not log noise.

## Canonical Observable Tags

Use tags for routing and correlation so subscribers do not need to parse content to filter:

- `["p", "<requester_pubkey>"]`
- `["e", "<contextvm_request_event_id>", "", "reply"]`
- `["d", "<domain>:<entity>:<id>"]` for addressable projections
- `["domain", "service" | "worker" | "dns" | "backup" | "ml" | ...]`
- `["schema", "<schema-id>"]`
- `["service", "<service_id>"]`
- `["environment", "<environment_id>"]`
- `["artifact", "<artifact_id>"]`
- `["route", "<llm_route_id>"]`
- `["release", "<llm_release_id>"]`
- `["intent", "<intent_id>"]`
- `["run", "<run_id>"]`
- `["status", "running" | "success" | "failed" | ...]`

## Discovery and Relay Sets

Clients bootstrap with:

- ContextVM server announcement `11316`.
- ContextVM capability announcements `11317`-`11320`.
- NIP-51 relay sets `30002` such as browser, ContextVM request, and service relay sets.
- NIP-65 relay lists where available for broader Nostr routing; Bahia's service-authored `10002` is advisory only and does not replace `30002` bootstrap sets.
- NIP-34 `30617` repository `relays` tags and `30618` state for repository-specific routing; these hints are preferred before global Bahia read relays for repository operations.
- Optional NIP-51 `10050` DM relay lists only when a Bahia feature and receiving identity are explicitly DM-enabled; browser, ContextVM, and service relay sets do not imply DM receive readiness.
- NIP-11 metadata and optional NIP-66 monitor events only as advisory capability/liveness metadata; they do not establish service trust or override configured relay policy.
- Optional NIP-86 relay-owner administration over HTTP with NIP-98 authorization only for configured Bahia-owned or Bahia-authorized relays; it is not ContextVM mutation transport and does not replace NIP-42 websocket AUTH.

Legacy Bahia discovery kind `31974` is not a production bootstrap contract. It may appear only in migration inputs or compatibility fixtures.

## Migration App

Bahia includes a startup migration app in `internal/nostrmigration`. It converts stored and optionally relay-backfilled legacy events into canonical ContextVM/canonical observable events before production runtime handles live traffic.

The migration app:

1. Scans the local Nostr event repository for `LegacyKinds()`.
2. Optionally backfills legacy events from relays and waits for `EOSE`.
3. Resolves each legacy event to a canonical disposition.
4. Skips events that already have a canonical output tagged `migrated-from=<legacy_event_id>`.
5. Builds a canonical event tagged with `migration=bahia-nostr-native-v1`, `legacy-kind`, `migrated-from`, `schema`, `domain`, and layer metadata.
6. Signs with the Bahia service key.
7. Publishes to relays, treating accepted publishes and relay duplicate acknowledgments as success.
8. Records the canonical event locally and logs a summary.

Because idempotency is based on `migrated-from`, the app is safe to run at every startup. Operators should fix migration failures rather than re-enabling legacy runtime subscribers.

## Historical Legacy Families

These families are retained only for migration manifests, historical conversion tests, and fail-closed fixtures:

| Legacy range | Historical purpose | Canonical target |
|--------------|--------------------|------------------|
| `5961`-`6006`, `38390`-`38431`, `5401`, `5102` | CRU/request operations | ContextVM `25910` methods, or NIP-09 `5` for deletion semantics |
| `6961`-`6997` | progress/status | `30315`, `4903`, correlated ContextVM responses, or domain observables |
| `7961`-`7997` | terminal results | ContextVM responses plus `30900`/`4903`/`30315` observables |
| `31961`-`32003` | read models | `30900`, `30078`, `11316`-`11320`, or `30002` depending on semantics |
| worker cleanup lifecycle | Fleet Health cleanup status | `30900` with `schema=bahia.state.worker-cleanup.v1`; mutation remains encrypted ContextVM `worker/cleanup` |
| `31000`-`31024`, `31310`-`31311` | audit/activity | `4903` |
| `5980`, `7980` | encrypted request/result envelope | CEP-4 / NIP-59 `1059` or `21059` around ContextVM `25910` |
| `31100`-`31105` | deprecated bridge commands | removed; no canonical live runtime path |

