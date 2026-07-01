# Bahia Nostr Control-Plane Events

Bahia's production Nostr control plane is now ContextVM-first. Mutation intent uses ContextVM JSON-RPC kind `25910`, usually encrypted with ContextVM CEP-4 / NIP-59 wrappers (`1059` or `21059`). When discovery advertises `encrypted_controlplane.progress_ack` plus `contextvm-jsonrpc-v2`, routed and authorized encrypted requests receive an early no-`id` `notifications/progress` JSON-RPC notification before the terminal response. Long-running truth is observed through canonical Nostr events, not through legacy Bahia request/status/result kind families.

Legacy Bahia kinds such as `5961`-`6006`, `6961`-`6997`, `7961`-`7997`, `31961`-`32003`, `38390`-`38431`, `5980`, and `7980` are migration inventory only, excluding explicitly documented SoulFactory interop kinds `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, and `38386` where their numbers overlap those ranges. New clients must not publish or subscribe to legacy Bahia numbers as production runtime contracts.

## Production Kind Families

| Family | Kind(s) | Direction | Purpose |
|--------|---------|-----------|---------|
| ContextVM intents | `25910` | inbound/outbound | JSON-RPC mutation requests, immediate acknowledgments, and responses |
| Encrypted ContextVM transport | `1059`, `21059` | inbound/outbound | CEP-4 / NIP-59 gift-wrap envelopes around inner ContextVM messages |
| ContextVM discovery | `11316`-`11320` | outbound replaceable | Server, tools, resources, templates, and prompts announcements |
| Canonical state | `30900`, `30078` | outbound replaceable/addressable | Control-plane state projections and NIP-78 app-specific data |
| Canonical audit/status | `4903`, `30315` | outbound | Immutable audit facts and NIP-38 operational statuses |
| Relay sets | `30002` | outbound addressable | NIP-51 relay topology and bootstrap sets |
| SBOM availability lists | `30004` | outbound addressable | NIP-51 complete availability lists for subject-scoped SBOM references |
| Relay preferences | `10002` | outbound replaceable | Advisory NIP-65 service read/write hints; not Bahia bootstrap |
| DM relay lists | `10050` | optional replaceable | NIP-51 DM receive routing only for explicitly configured DM-enabled features and identities |
| Repository state | `30617`, `30618` | inbound/outbound by repository owner | NIP-34 repository announcements and state; repository relay hints are preferred for repository operations |
| SoulFactory interop | `31950`, `31951`, `31952`, `5950`, `6950`, `7950`, `1950`, `1951`, `30317`, `38384`, `38386` | inbound/outbound by operators, SoulFactory controllers, and runtimes | Direct Nostr agent templates, drafts, souls, provisioning/lifecycle events, runtime capabilities, runtime-control requests, and correlated results |
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
| `security` | `scan`, `rescan`, `findings-list`, `schedules-list` |

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
  "kinds": [30900, 30315, 4903, 30078, 30004],
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
5. Apply replaceable semantics for `(kind, pubkey, d)` on `30900`, `30078`, `11316`-`11320`, `30002`, and `30004`.
6. Treat relay `OK`, `CLOSED`, and `AUTH` messages as protocol outcomes, not log noise.

## Desired-State Runtime Metadata

Compose/Docker desired-state deploys do not add new Nostr kinds or d-tag coordinates. They enrich existing ContextVM responses and canonical observables with optional metadata that older readers can ignore:

- Status `step` values: `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`.
- Result/state fields or tags when available: `desired_hash`, `renderer`, `target`, runtime target/apply summaries, environment or unit revision metadata, and `observation_id`.
- Sanitized content only: no resolved secret values, generated `.bahia/env/*.env` contents, raw Docker transport material, Docker TLS material, bearer credentials, or NIP-98 credentials.

Historical `6961`/`7961`/`7962` and `31961`/`31967`/`31968` shapes may contain analogous fields in migration fixtures, but production clients follow `30315`, ContextVM responses, `30900`, `4903`, and `30078`.

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
- `["step", "building_desired_state" | "locking_environment" | "rendering" | "applying" | "observing" | "projecting" | ...]`
- `["desired_hash", "<sha256:...>"]`, `["renderer", "compose" | "docker" | ...]`, and `["target", "<stable-runtime-service-key>"]` when desired-state metadata is available

## SBOM Observables

SBOM generation and import intents are ContextVM methods such as `sbom/generate` and `sbom/import`. The JSON-RPC response is an acknowledgment only. Durable truth comes from:

- `30078` SBOM reference app-data events with `domain=sbom`, `schema=bahia.sbom.ref.v1`, and `d=sbom:ref:<subject-key>:<format>:<payload-sha256>`.
- `30004` NIP-51 availability lists with `domain=sbom`, `schema=bahia.sbom.available-list.v1`, and `d=sbom:available:<subject-type>:<subject-key>`.
- `30315` status and `4903` audit events for progress and provenance.

The legacy `30079` SBOM index kind is read-only compatibility data and is not a publication target.

## Security OSV/SBOM Observables

Security scans are requested with ContextVM methods and followed through canonical observables. Use `security/scan` for explicit SBOM/package/PURL/commit scans, `security/rescan` for another run of a known target, and `security/findings-list` or `security/schedules-list` only for persisted read surfaces. The JSON-RPC response acknowledges request handling; it is not scan completion.

A Security scan target is identified by `target_type` plus a canonical target key hash. Implementations derive that hash from the target key rules in `pstf/features/SECURITY_OSV_SBOM/feature_spec.json` and the Security plan. Use the hash in tags where full target keys are too long.

Durable Security events use:

- `30315` status: `domain=security`, `schema=bahia.status.security-scan.v1`, `d=security:scan:<run_id>`, `run`, `target_type`, `target_key_hash`, `status`, optional `step`, and request correlation tags.
- `30900` summaries: `schema=bahia.security.scan-summary.v1` for `d=security:scan-summary:<run_id>` and `schema=bahia.security.target-summary.v1` for `d=security:target-summary:<target_key_hash>`.
- `30078` findings: `domain=security`, `schema=bahia.security.findings.v1`, and `d=security:findings:<run_id>:<chunk_or_finding_hash>`.
- `4903` audit: `domain=security`, `schema=bahia.audit.security.v1`, with type values `security-scan`, `security-policy-breach`, and `security-publication`.

Follow a scan with a scoped filter such as:

```json
{
  "kinds": [30315, 30900, 30078, 4903],
  "authors": ["<bahia-service-pubkey>"],
  "#domain": ["security"],
  "#target_key_hash": ["<target-key-hash>"],
  "#e": ["<contextvm-request-event-id>"]
}
```

Security service ingestion of SBOM truth uses existing SBOM filters over `30078` and `30004`; it processes stored events until `EOSE`, keeps realtime subscriptions open when enabled, and handles `CLOSED`/`AUTH` without falling back to polling. Every Security observable publish must verify relay `OK accepted=true` and retain rejection messages for operator diagnostics and retry state.

Security Epic 3 exposes these surfaces through encrypted ContextVM only. It does not add REST compatibility endpoints; REST callers must bridge through ContextVM and follow the canonical Security observables for progress and terminal truth.

Policy breaches dispatch the internal notification event type `security.policy_breached` only for new or materially changed persisted fingerprints. The public Nostr evidence is a `4903` audit event; no new Nostr kind is created for notifications.

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

## Domain Mutation Surfaces (MCP → Nostr)

Each domain's mutations flow through MCP tools backed by signer-first controlplane publishers. The publisher signs a ContextVM kind `25910` event, publishes to the relay pool, and returns a `CommandReceipt` with the event ID and relay acceptance count. REST never participates in mutation — it serves only read models.

### Services

| MCP tool | ContextVM method | Publisher |
|----------|-----------------|-----------|
| `bahia_create_service` | `service/create` | `ServiceCommandPublisher.PublishServiceCreate` |
| `bahia_deploy` | `service/deploy` | `ServiceCommandPublisher.PublishDeployIntent` |
| `bahia_rollback` | `service/rollback` | `ServiceCommandPublisher.PublishRollback` |

**CLI path**: `bahia services actions deploy|restart|stop` → `cmd/cli/operator_nostr.go` → ContextVM `25910`

**Read models (REST GET only)**:
- `GET /api/v1/services` — list services
- `GET /api/v1/services/{id}` — get service details
- `GET /api/v1/services/{id}/environments` — list environments
- `GET /api/v1/services/{id}/deployments` — list deployments

### Policies

| MCP tool | ContextVM method | Publisher |
|----------|-----------------|-----------|
| `bahia_policy_create` | `policy/create` | `PolicyCommandPublisher.PublishPolicyCreate` |
| `bahia_policy_update` | `policy/update` | `PolicyCommandPublisher.PublishPolicyUpdate` |
| `bahia_policy_delete` | `policy/delete` | `PolicyCommandPublisher.PublishPolicyDelete` |

**Read models (REST GET only)**:
- `GET /api/v1/policies` — list policies
- `GET /api/v1/policies/{id}` — get policy details

### Deployments / Intents

| MCP tool | ContextVM method | Publisher |
|----------|-----------------|-----------|
| `bahia_deploy` | `service/deploy` | `ServiceCommandPublisher.PublishDeployIntent` |
| `bahia_create_deployment_intent` | `service/deploy` | Same publisher (alias) |

Deployment intents are a subset of service mutations. The MCP tool creates a signed deploy intent event; the reactor processes it into an actual deployment.

**Read models (REST GET only)**:
- `GET /api/v1/deployments` — list deployments
- `GET /api/v1/deployments/{id}` — get deployment details

### LLM Routes

| MCP tool | ContextVM method | Publisher |
|----------|-----------------|-----------|
| `bahia_llm_create_route` | `llm/route-create` | `LLMCommandPublisher.PublishRouteCreate` |
| `bahia_llm_update_route` | `llm/route-update` | `LLMCommandPublisher.PublishRouteUpdate` |
| `bahia_llm_register_release` | `llm/release-register` | `LLMCommandPublisher.PublishReleaseRegister` |
| `bahia_llm_deploy` | `llm/deploy` | `LLMCommandPublisher.PublishDeploy` |
| `bahia_llm_approve_deployment` | `llm/approve` | `LLMCommandPublisher.PublishApproval` |
| `bahia_llm_rollback` | `llm/rollback` | `LLMCommandPublisher.PublishRollback` |

**Read models (REST GET only)**:
- `GET /api/v1/llm/routes` — list LLM routes
- `GET /api/v1/llm/routes/{id}` — get route details
- `GET /api/v1/llm/releases` — list releases

### Artifacts

| MCP tool | ContextVM method | Publisher |
|----------|-----------------|-----------|
| `bahia_register_artifact` | `artifact/register` | `ArtifactCommandPublisher.PublishArtifactRegister` |

### Tool Approvals

| MCP tool | ContextVM method | Publisher |
|----------|-----------------|-----------|
| `bahia_approve_tool` | `tool/approve` | `ToolApprovalCommandPublisher.PublishToolApproval` |

### Anti-pattern: REST write endpoints

Do **not** add `POST`, `PUT`, or `DELETE` routes to `internal/api/router/router.go` for any of these domains. The correct mutation path is always:

```
MCP tool → controlplane.*CommandPublisher → Nostr relay → reactor → read model → REST GET
```

Adding REST write endpoints creates "fake request/response wrappers over relays" which is explicitly prohibited by AGENTS.md. If an MCP tool or CLI command doesn't exist for a mutation, create a new controlplane publisher and MCP tool — never a REST handler.

---

## Historical Legacy Families

These families are retained only for migration manifests, historical conversion tests, and fail-closed fixtures:

| Legacy range | Historical purpose | Canonical target |
|--------------|--------------------|------------------|
| `5961`-`6006`, `38390`-`38431`, `5401`, `5102` excluding SoulFactory interop `5950`, `1950`, `38384` | CRU/request operations | ContextVM `25910` methods, or NIP-09 `5` for deletion semantics |
| `6961`-`6997` excluding SoulFactory interop `6950` | progress/status | `30315`, `4903`, correlated ContextVM responses, or domain observables |
| `7961`-`7997` excluding SoulFactory interop `7950`, `1951`, `38386` | terminal results | ContextVM responses plus `30900`/`4903`/`30315` observables |
| `31961`-`32003` | read models | `30900`, `30078`, `11316`-`11320`, or `30002` depending on semantics |
| `30079` | SBOM index | read-only compatibility; canonical SBOM availability uses NIP-51 `30004` |
| worker cleanup lifecycle | Fleet Health cleanup status | `30900` with `schema=bahia.state.worker-cleanup.v1`; mutation remains encrypted ContextVM `worker/cleanup` |
| `31000`-`31024`, `31310`-`31311` | audit/activity | `4903` |
| `5980`, `7980` | encrypted request/result envelope | CEP-4 / NIP-59 `1059` or `21059` around ContextVM `25910` |
| `31100`-`31105` | deprecated bridge commands | removed; no canonical live runtime path |

