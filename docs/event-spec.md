# Bahia Nostr Event Specification

## Overview

Bahia publishes signed Nostr events for control-plane intent, state, status, discovery, relay topology, and audit. The production contract is Nostr-native and ContextVM-first:

- **Intent/mutation**: ContextVM JSON-RPC kind `25910`, usually encrypted with CEP-4 / NIP-59 gift-wrap (`1059` or `21059`).
- **State**: canonical control-plane projections in kind `30900`, plus NIP-78 app-specific data in kind `30078`.
- **Status**: NIP-38 operational status kind `30315`.
- **Assistant transcript**: service-authored kind `30316` with a service-held symmetric-key AEAD envelope in `content` and key-reference/rotation tags.
- **Audit**: Cascadia/Bahia audit kind `4903`.
- **Discovery/collections**: ContextVM announcements `11316`-`11320`, NIP-51 relay sets `30002`, and NIP-51 curation sets `30004` where a domain needs curated reference inventories such as SBOM availability.
- **Deletion**: NIP-09 kind `5` where relay-level deletion semantics apply; durable domain tombstones may still be represented as canonical replacement state with `deleted=true`.

Legacy Bahia custom families (`5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `31000`-`31024`, `38390`-`38431`, `5980`, `7980`) are migration inventory only, excluding explicitly documented SoulFactory interop kinds `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, and `38386` where their numbers overlap those ranges. Production code should not subscribe to legacy Bahia families as live control-plane contracts.

## Production Event Families

| Kind(s) | Layer | Description |
|---------|-------|-------------|
| `25910` | Intent | ContextVM JSON-RPC request/response messages. Method names use `<domain>/<operation>`. Encrypted routed+authorized requests may first receive a no-`id` `notifications/progress` JSON-RPC notification with `status=processing` when discovery advertises `encrypted_controlplane.progress_ack` and `contextvm-jsonrpc-v2`. |
| `1059`, `21059` | Intent transport | CEP-4 / NIP-59 gift-wrap envelopes for encrypted ContextVM messages. |
| `30900` | State | Canonical addressable control-plane state projection. |
| `30078` | State/app data | NIP-78 app-specific configuration, registries, and UI/operator projection data; SBOM reference events use `schema=bahia.sbom.ref.v1`. |
| `30004` | Collection | NIP-51 Curation Set for SBOM availability lists and other curated reference inventories. |
| `30315` | Status | NIP-38 operational status for agents, workers, services, and long-running actions. |
| `30316` | Assistant transcript | Append-only encrypted assistant transcript entries; `content` is `bahia.assistant-transcript.v1` service-held symmetric-key AEAD metadata plus ciphertext, with `session`, `turn`, `role`, `seq`, `key_ref`, `key_version`, `key_rotation`, and `envelope` tags. |
| `4903` | Audit | Immutable audit fact / attestation / provenance breadcrumb. |
| `11316`-`11320` | Discovery | ContextVM server, tools, resources, templates, and prompts announcements. |
| `30002` | Collection/discovery | NIP-51 relay sets for browser, ContextVM, service, and operational relay topology. |
| `10002` | Relay preference | Advisory NIP-65 service relay read/write hints for wider Nostr routing. |
| `10050` | DM relay list | Optional NIP-51 DM receive routing only for explicitly configured DM-enabled Bahia features and identities. |
| `30617`, `30618` | Repository | NIP-34 repository announcements and state; repository relay hints are repository-specific routing inputs. |
| `31950`, `31951`, `31952`, `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, `38386` | SoulFactory interop | Direct Nostr agent templates, drafts, souls, provisioning/lifecycle events, runtime capabilities, runtime-control requests, and correlated results. |
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
| `security` | `scan`, `rescan`, `findings-list`, `schedules-list` |
| `soul-factory` | `provision`, `action` |

`soul-factory/provision` and `soul-factory/action` are the canonical mutation entry points for new Soul Factory clients. During the staged migration Bahia adapts each verified request into the existing event-driven Soul Factory reactor, retaining the original `25910` event id for correlation. The response acknowledges acceptance only. Provisioning progress and terminal outcomes produce canonical `30900` state (`d=soul-factory:provisioning:<request-event-id>`, schema `bahia.state.soul-factory-provisioning.v1`) plus append-only `4903` audit facts. Action projection remains staged.

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

Desired-state runtime projections may add `desired_hash`, renderer/target metadata, environment or deployment-unit revision metadata, apply metadata summaries, and `observation_id`. These additions are optional and backward-compatible. Projection content must remain sanitized: no secret plaintext, generated Compose env-file content, raw Docker transport material, Docker TLS material, bearer credentials, or NIP-98 credentials.

## Operational Status — Kind `30315`

Bahia uses NIP-38 status events for operational status and progress where a status fact is more appropriate than a full state replacement. Desired-state deploy status uses additive `step` metadata on this same kind; the shared Compose/Docker sequence is `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`.

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
| `30002` | NIP-51 relay sets such as browser, ContextVM, service, and operational relay sets; canonical Bahia bootstrap topology |
| `30004` | NIP-51 Curation Set for complete SBOM availability lists, using `domain=sbom` and `schema=bahia.sbom.available-list.v1` |
| `10002` | NIP-65 relay preferences; advisory service read/write hints for wider Nostr routing |
| `10050` | Optional NIP-51 DM relay list for explicitly configured DM-enabled features and receiving identities; never inferred from browser, ContextVM, or service relay sets |
| `30617` / `30618` | NIP-34 repository announcement and state; repository relay hints are preferred before global Bahia read relays for repository operations |

The service-authored `10002` list marks ContextVM request relays with `read` and service publish/backfill relays with `write`. It does not replace ContextVM discovery or the NIP-51 `30002` relay sets. NIP-11 relay metadata and optional NIP-66 monitor events are advisory capability/liveness metadata only; they do not establish service trust, override trusted service pubkeys, or authorize removing all configured relays. NIP-86 is an optional HTTP relay-owner administration API protected with NIP-98 authorization, not a Nostr event kind and not a ContextVM mutation transport.

Legacy discovery kind `31974` is migration input only.

## SBOM Reference and Availability Events

SBOM payload bytes are stored outside Nostr, typically in Blossom. Nostr carries durable references and availability state:

- Kind `30078` is the canonical SBOM reference app-data event. It uses `domain=sbom`, `schema=bahia.sbom.ref.v1`, and `d=sbom:ref:<subject-key>:<format>:<payload-sha256>`. Content is the in-toto-style SBOM attestation envelope. Tags identify the subject, format, storage backend, location URI, payload SHA-256 (`x`), media type, generator, and NTIA status.
- Kind `30004` is the canonical SBOM availability list. It uses `domain=sbom`, `schema=bahia.sbom.available-list.v1`, and `d=sbom:available:<subject-type>:<subject-key>`. The list is replaced as a complete set for one subject version and includes `a` tags to `30078` reference coordinates plus `sbom` summary tags.
- Kind `30079` is historical read-only SBOM index data. Production publishers must not create new `30079` events.

Relay `OK` acceptance is required before a publisher treats either the `30078` reference or `30004` availability list as published.

## Security OSV/SBOM Events

Security scanning uses existing canonical event families; Epic 1 does not allocate a new Security kind. ContextVM requests acknowledge scan intent or read persisted Security data, while durable scan truth is observed through scoped subscriptions.

### ContextVM methods

| Method | Purpose | Terminal truth |
|--------|---------|----------------|
| `security/scan` | Request a scan for an SBOM reference, package coordinate, PURL, or Git commit target. | `30315`, `30900`, `30078`, and `4903` Security observables. |
| `security/rescan` | Request a new run for a previously known target or latest SBOM target. | New scan status and summary observables correlated to the request and target. |
| `security/findings-list` | Read persisted normalized findings for a target, run, policy scope, or severity filter. | Immediate read response only; it does not imply scan completion. |
| `security/schedules-list` | Read policy-derived scan schedules and freshness metadata. | Immediate read response only; schedule due work is observable through scan events. |

A scan request uses a stable idempotency key in the ContextVM `d` tag and `_meta.progressToken` when available. Tags include `p=<bahia-service-pubkey>`, `method=security/scan`, `op=scan`, `domain=security`, `schema=bahia.intent.security-scan.v1`, `target_type`, optional `target_key_hash` when the client can precompute it, and resource tags such as `subject_type`, `subject`, `artifact`, `package`, `environment`, or `policy` when known. Sensitive target data should be wrapped in `1059` or `21059`.

Example package scan intent:

```json
{
  "kind": 25910,
  "pubkey": "<operator-pubkey>",
  "tags": [
    ["p", "<bahia-service-pubkey>"],
    ["method", "security/scan"],
    ["op", "scan"],
    ["domain", "security"],
    ["schema", "bahia.intent.security-scan.v1"],
    ["d", "security-scan-pypi-django-4-2-20260614"],
    ["target_type", "package"],
    ["package", "pypi/django"],
    ["environment", "prod"]
  ],
  "content": "{\"jsonrpc\":\"2.0\",\"id\":\"security-scan-pypi-django-4-2-20260614\",\"method\":\"security/scan\",\"params\":{\"target\":{\"type\":\"package\",\"ecosystem\":\"PyPI\",\"name\":\"django\",\"version\":\"4.2.0\"},\"policy_scope\":{\"environment_id\":\"prod\"},\"_meta\":{\"progressToken\":\"security-scan-pypi-django-4-2-20260614\"}}}"
}
```

### Security status, summary, finding, and audit schemas

- `30315` scan status uses `domain=security`, `schema=bahia.status.security-scan.v1`, `d=security:scan:<run_id>`, `run=<run_id>`, `target_type`, `target_key_hash`, `status`, optional `step`, and correlation `e`/`p` tags. Status values include `accepted`, `queued`, `running`, `completed`, `failed`, `cancelled`, and `degraded`.
- `30900` scan summaries use `schema=bahia.security.scan-summary.v1` and `d=security:scan-summary:<run_id>`. Latest target summaries use `schema=bahia.security.target-summary.v1` and `d=security:target:<target_key_hash>`. Content includes target identity, run metadata, scan freshness, provider, severity counts, finding counts, policy status, and error/degraded metadata.
- `30078` finding details use `domain=security`, `schema=bahia.security.findings.v1`, and `d=security:findings:<run_id>:<chunk_or_finding_hash>`. Content contains normalized public-safe findings, affected coordinates, OSV IDs, severity/source metadata, withdrawn status, and target/run references. Raw hydrated OSV cache records and sensitive repository details are persisted privately rather than published as public Nostr content.
- `4903` audit facts use `domain=security`, `schema=bahia.audit.security.v1`, and `type` values such as `security-scan`, `security-policy-breach`, and `security-publication`. Audit content records lifecycle actions including scan accepted/running/completed/failed, payload hash mismatch, OSV adapter failure, publish retry, and policy breach fingerprint changes.

Security publishers must verify relay `OK` accepted flags and messages for every `30315`, `30900`, `30078`, and `4903` event. `CLOSED` and `AUTH` outcomes are explicit protocol states; auth-required relays require NIP-42 handling or deterministic failure, not silent fallback.

### Subscription filters

SBOM-triggered scanning subscribes narrowly to canonical SBOM facts, for example:

```json
{
  "kinds": [30078, 30004],
  "authors": ["<bahia-service-pubkey>"],
  "#domain": ["sbom"],
  "#schema": ["bahia.sbom.ref.v1", "bahia.sbom.available-list.v1"],
  "#subject_type": ["artifact"],
  "#subject": ["sha256:<subject-digest>"]
}
```

Operators following an explicit scan subscribe to correlated Security observables:

```json
{
  "kinds": [30315, 30900, 30078, 4903],
  "authors": ["<bahia-service-pubkey>"],
  "#domain": ["security"],
  "#e": ["<contextvm-request-event-id>"],
  "#target_key_hash": ["<security-target-hash>"]
}
```

Process historical `EVENT`s until `EOSE`, then keep the subscription open for realtime updates when convergence is needed. Deduplicate by event id, apply replaceable semantics for `30900` and `30078` by `(kind, pubkey, d)`, and do not poll REST/MCP for completion.

### Policy-breach notification convention

The in-process event type `security.policy_breached` is the notification trigger for actionable Security policy breaches. It is not a Nostr kind. It is emitted only when the persisted breach fingerprint for `policy_id + target_key_hash` is new or materially changed. A clean scan marks prior breach records resolved without sending another breach notification unless a tracked implementation issue explicitly adds and documents a separate resolved notification type.

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
| `security.policy_breached` | Security policy breach became new or materially changed | `policy_id`, `target_key_hash`, `fingerprint`, `severity_counts`, `violated_rules` |

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
| `5961`-`6006`, `38390`-`38431`, `5401`, `5102` excluding SoulFactory interop `5950`, `1950`, `38384` | CRU/request operations | ContextVM `25910` methods, or NIP-09 `5` for deletion semantics |
| `6961`-`6997` excluding SoulFactory interop `6950` | status/progress | `30315`, `4903`, correlated ContextVM responses, or domain observables |
| `7961`-`7997` excluding SoulFactory interop `7950`, `1951`, `38386` | terminal results | ContextVM responses plus `30900`/`4903`/`30315` observables |
| `31961`-`32003`, `31974` | read models/discovery | `30900`, `30078`, `11316`-`11320`, or `30002` depending on semantics |
| `30079` | historical SBOM index | read-only compatibility; canonical SBOM availability uses NIP-51 `30004` |
| worker cleanup lifecycle | resource-pressure cleanup state | `30900` with `schema=bahia.state.worker-cleanup.v1`, `domain=worker`, and narrow `worker`/`status` tags |
| `31000`-`31024`, `31310`-`31311` | audit/activity | `4903` |
| `5980`, `7980` | encrypted request/result envelope | CEP-4 / NIP-59 `1059` or `21059` around ContextVM `25910` |
| `31100`-`31105` | deprecated bridge commands | removed; no live canonical runtime path |
