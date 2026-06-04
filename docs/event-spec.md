# Bahia Nostr Event Specification

## Overview

Bahia publishes signed Nostr events for control-plane intent, state, status, discovery, relay topology, and audit. The production contract is Nostr-native and ContextVM-first:

- **Intent/mutation**: ContextVM JSON-RPC kind `25910`, usually encrypted with CEP-4 / NIP-59 gift-wrap (`1059` or `21059`).
- **State**: canonical control-plane projections in kind `30900`, plus NIP-78 app-specific data in kind `30078`.
- **Status**: NIP-38 operational status kind `30315`.
- **Audit**: Cascadia/Bahia audit kind `4903`.
- **Discovery**: ContextVM announcements `11316`-`11320` and NIP-51 relay sets `30002`.
- **Deletion**: NIP-09 kind `5` where relay-level deletion semantics apply; durable domain tombstones may still be represented as canonical replacement state with `deleted=true`.

Legacy Bahia custom families (`5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `31000`-`31024`, `38390`-`38431`, `5980`, `7980`) are migration inventory only. Production code should not subscribe to them as live control-plane contracts.

## Production Event Families

| Kind(s) | Layer | Description |
|---------|-------|-------------|
| `25910` | Intent | ContextVM JSON-RPC request/response messages. Method names use `<domain>/<operation>`. |
| `1059`, `21059` | Intent transport | CEP-4 / NIP-59 gift-wrap envelopes for encrypted ContextVM messages. |
| `30900` | State | Canonical addressable control-plane state projection. |
| `30078` | State/app data | NIP-78 app-specific configuration, registries, and UI/operator projection data. |
| `30315` | Status | NIP-38 operational status for agents, workers, services, and long-running actions. |
| `4903` | Audit | Immutable audit fact / attestation / provenance breadcrumb. |
| `11316`-`11320` | Discovery | ContextVM server, tools, resources, templates, and prompts announcements. |
| `30002` | Collection/discovery | NIP-51 relay sets for browser, service, and operational relay topology. |
| `5` | Deletion | NIP-09 deletion event for relay-level deletion semantics. |

## ContextVM Intent Structure

A non-encrypted ContextVM request is a signed Nostr event with JSON-RPC content:

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

Sensitive requests use the same inner event encrypted into a random-key gift-wrap event:

```json
{
  "kind": 1059,
  "pubkey": "<random-wrap-pubkey>",
  "tags": [["p", "<bahia-service-pubkey>"]],
  "content": "<nip44-encrypted-inner-contextvm-event>"
}
```

Implementations may use `21059` when ephemeral gift-wrap support is available.

## Method Naming

Methods follow `<domain>/<operation>`:

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

JSON-RPC responses acknowledge request handling. Long-running completion comes from canonical observable events.

## Canonical State Projection — Kind `30900`

Kind `30900` is addressable. The `d` tag identifies the entity coordinate, and `domain` + `schema` tags identify the projection contract.

```json
{
  "kind": 30900,
  "pubkey": "<bahia-service-pubkey>",
  "content": "{\"service_id\":\"api\",\"environment_id\":\"prod\",\"status\":\"healthy\"}",
  "tags": [
    ["d", "service:api:prod"],
    ["domain", "service"],
    ["schema", "bahia.service-state.v1"],
    ["service", "api"],
    ["environment", "prod"],
    ["status", "healthy"]
  ]
}
```

For `(kind, pubkey, d)`, latest replacement wins. Clients query historical state, wait for `EOSE`, and keep the subscription open for realtime updates.

## Operational Status — Kind `30315`

Bahia uses NIP-38 status events for operational status and progress where a status fact is more appropriate than a full state replacement.

```json
{
  "kind": 30315,
  "pubkey": "<bahia-service-pubkey>",
  "content": "deploying api to prod",
  "tags": [
    ["d", "cascadia:service:api:prod"],
    ["status", "deploying"],
    ["service", "api"],
    ["environment", "prod"],
    ["e", "<contextvm-request-event-id>", "", "reply"]
  ]
}
```

## Audit — Kind `4903`

Audit events are append-only facts for provenance, compliance, deployment evidence, build evidence, and operational attestations.

```json
{
  "kind": 4903,
  "pubkey": "<bahia-service-pubkey>",
  "content": "{\"action\":\"deployment.completed\",\"service_id\":\"api\",\"environment_id\":\"prod\"}",
  "tags": [
    ["domain", "deployment"],
    ["type", "change-record"],
    ["schema", "bahia.audit.deployment.v1"],
    ["service", "api"],
    ["environment", "prod"],
    ["e", "<contextvm-request-event-id>"]
  ]
}
```

Relays and clients should treat audit as long-retention evidence. Audit deletion should require explicit policy review.

## Discovery and Relay Topology

Bahia discovery is ContextVM-compatible:

| Kind | Purpose |
|------|---------|
| `11316` | Server announcement and transport metadata |
| `11317` | Tools list |
| `11318` | Resources list |
| `11319` | Resource templates list |
| `11320` | Prompts list |
| `30002` | NIP-51 relay sets such as browser, service, and operational relay sets |

Legacy discovery kind `31974` is migration input only.

## Loom and Hive-CI External Protocol Events

Bahia still interoperates with external protocols that define their own kinds. These are not Bahia legacy control-plane families:

| Protocol | Kind | Direction | Description |
|----------|------|-----------|-------------|
| Loom | `10100` | inbound | Worker advertisement |
| Loom | `5100` | outbound | Compute job request |
| Loom | `30100` | inbound | Loom job status update |
| Loom | `5101` | inbound | Loom job result |
| Hive-CI | `5401` | inbound | Trusted CI workflow run fact |
| Hive-CI | `5402` | inbound | Trusted CI workflow result fact |

Where Bahia needs to expose current Loom/Hive-derived truth to browsers or agents, it projects that truth into canonical observables (`30900`, `30315`, `4903`, or `30078`) rather than inventing a Bahia-specific live kind family.

## Internal Operational Event Types

Bahia also emits typed in-process audit events used by projectors, automation subscribers, and local observability wiring. These internal events are not Nostr kind allocations. They must not contain secret values, raw environment values, Docker TLS material, bearer credentials, or NIP-98 credentials.

| Type | Description | Key fields |
|------|-------------|------------|
| `adoption.scan_completed` | Adoption dry-run scan completed | `target_count`, `candidate_count`, `target_error_count`, redaction counts, `duration_ms` |
| `adoption.imported` | One adoption candidate was persisted | `service_id`, `environment_id`, `artifact_id`, `target_name`, `status` |
| `runtime.deploy` | Direct runtime deploy completed | `service_id`, `environment_id`, `artifact_id`, `runtime_target`, `observation_id`, `health_status` |
| `runtime.restart` | Direct runtime restart completed | `service_id`, `environment_id`, `runtime_target`, `observation_id`, `health_status` |
| `runtime.stop` | Direct runtime stop completed | `service_id`, `environment_id`, `runtime_target`, `observation_id`, `health_status` |
| `llm_route.created` / `llm_route.updated` | LLM route registry changed | `route_id` |
| `llm_release.registered` | Immutable LLM release registered | `route_id`, `release_id` |
| `llm_deployment_intent.created` / `.approved` / `.rejected` | LLM deployment intent lifecycle changed | `route_id`, `environment_id`, `release_id`, `intent_id` |
| `llm_deployment_run.created` / `.status_changed` / `.completed` | LLM deployment run lifecycle changed | `intent_id`, `run_id` |
| `llm_route.observation` | LLM backend/gateway observation recorded | `route_id`, `environment_id`, `release_id`, `run_id` |
| `llm_route_state.changed` / `llm_route.drift_detected` | LLM route state projection changed | `route_id`, `environment_id`, `release_id`, `intent_id`, `run_id` |
| `llm_gateway_route.synced` | Gateway model route synchronized | `route_id`, `environment_id` |

## Startup Migration App

The startup migration app in `internal/nostrmigration` converts historical Bahia custom events to the canonical contract before production runtime processes live traffic.

It performs these steps:

1. Scans the local Nostr event repository for `LegacyKinds()`.
2. Optionally backfills legacy events from configured relays and waits for `EOSE`.
3. Resolves each legacy event to a canonical disposition.
4. Skips if a canonical event with `migrated-from=<legacy_event_id>` already exists for the target kind.
5. Builds a canonical event with tags including `migration=bahia-nostr-native-v1`, `legacy-kind`, `migrated-from`, `schema`, `domain`, and layer metadata.
6. Signs with the Bahia service key.
7. Publishes to relays and treats accepted publishes or duplicate acknowledgments as success.
8. Records the canonical event locally and logs a summary.

This is idempotent and safe to run every startup. If the migration fails because the publisher or service private key is missing, or because relay backfill cannot reach `EOSE`, fix configuration or relay health; do not restore legacy runtime subscribers.

## Historical Legacy Mapping Summary

| Legacy family | Historical purpose | Canonical target |
|---------------|--------------------|------------------|
| `5961`-`6006`, `38390`-`38431`, `5401`, `5102` | CRU/request operations | ContextVM `25910` methods, or NIP-09 `5` for deletion semantics |
| `6961`-`6997` | status/progress | `30315`, `4903`, correlated ContextVM responses, or domain observables |
| `7961`-`7997` | terminal results | ContextVM responses plus `30900`/`4903`/`30315` observables |
| `31961`-`32003`, `31974` | read models/discovery | `30900`, `30078`, `11316`-`11320`, or `30002` depending on semantics |
| worker cleanup lifecycle | resource-pressure cleanup state | `30900` with `schema=bahia.state.worker-cleanup.v1`, `domain=worker`, and narrow `worker`/`status` tags |
| `31000`-`31024`, `31310`-`31311` | audit/activity | `4903` |
| `5980`, `7980` | encrypted request/result envelope | CEP-4 / NIP-59 `1059` or `21059` around ContextVM `25910` |
| `31100`-`31105` | deprecated bridge commands | removed; no live canonical runtime path |

