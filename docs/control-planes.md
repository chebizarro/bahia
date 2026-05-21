# Bahia Control Planes

Bahia's supported control-plane contract is now sidecar-first and Nostr-native:

1. **Nostr relay sidecar** — primary async/realtime plane for browser state, operator requests, agent progress, and read models.
2. **Native MCP JSON-RPC** — synchronous tool discovery/invocation at `/mcp` and `/api/v1/mcp`.
3. **REST API** — narrowed CRUD/query/log surface protected by direct NIP-98 when auth is enabled; Bearer credentials are not accepted.

Removed legacy surfaces:

- `GET /api/v1/events/stream` dashboard SSE stream
- `POST /api/v1/auth/nostr` NIP-98-to-JWT browser exchange
- `/api/v1/agent/*` custom MCP-inspired HTTP tools

`Nostr discovery events (kind 31974 + NIP-51 kind 30002)` keeps `nostr_auth_exchange`, `legacy_sse`, `legacy_jwt_exchange`, and `legacy_agent_http` keys as `false` values so old clients can fail closed.

---

## Native MCP Transport

> **Base paths**: `/mcp` and `/api/v1/mcp`

MCP clients use JSON-RPC 2.0 over HTTP. Tool implementations are backed by `internal/mcp/server.go`; long-running tool results include Nostr correlation metadata (`request_event_id`, `request_kind`, `service_id`, `route_id`, `release_id`, `environment_id`, `intent_id`, `run_id`, status/result/read-model kinds) so agents can follow async truth on the relay. `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` advertises core `control_plane` discovery metadata for clients that need bootstrap information before subscribing; broader command families are documented here and in `docs/nostr-commands.md`.

Example:

```bash
curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'

curl -X POST http://localhost:8080/mcp \
  -H "Content-Type: application/json" \
  -d '{
    "jsonrpc":"2.0",
    "id":2,
    "method":"tools/call",
    "params": {
      "name": "bahia_deploy",
      "arguments": {
        "service_id": "...",
        "environment_id": "...",
        "artifact_id": "..."
      }
    }
  }'
```

---

## Nostr Sidecar Topology

Browser and Bahia control-plane traffic should target the relay sidecar first.

- Browser discovery: `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` → `nostr.browser_relays` / `nostr.sidecar_url`
- Bahia backend connection: `nostr.sidecar.backend_url` when set, otherwise `nostr.sidecar.public_url`
- Bahia-owned control-plane reactor/projector traffic uses only the sidecar backend URL in sidecar mode.
- Upstream relays: configure `nostr.relays` for public interop/audit traffic. If `nostr.sidecar.mirror_external=true`, Bahia treats the sidecar as the upstream mirror boundary and does not also connect directly to those URLs.
- Encrypted-request relay URLs and Loom relays remain explicitly configured for their own traffic and are not used for Bahia read-model publication.

This avoids duplicate event loops: Bahia publishes canonical 696x/796x/3196x/read-model traffic to the sidecar pool only, while optional upstream mirroring is isolated behind the sidecar boundary.

---

## Nostr Control Plane

The Nostr reactor subscribes to signed request events and publishes status, terminal results, and replaceable read models. Service registry operations delegate to `RegistryService`; adoption scan/import delegates to `AdoptionService`; direct-runtime `deploy|restart|stop` delegates to `RuntimeLifecycleService`; LLM route/release/deploy/approval/rollback operations delegate to `LLMRegistryService`. LLM deploy, adoption/import, and direct-runtime actions are Nostr-first async actions; REST is only a narrowed registry/query/compatibility surface.

| Series | Range | Purpose |
|--------|-------|---------|
| Service requests | 5961–5968 | Inbound service/environment operation requests |
| LLM requests | 5971–5975 | Inbound LLM route/release/deploy/approval/rollback requests |
| Tool provisioning loop | 5976, 5977, 6976, 7976, 7977 | Agent request, Bahia→operator approval handoff, progress, final result, and operator approval response |
| Adoption requests | 5978–5979 | Inbound adoption scan/import operator requests |
| Public compatibility writes | 5981–5989 | Public signed service/environment/artifact/policy write operations |
| Encrypted requests | 5980 | Browser → Bahia encrypted request-domain request |
| Service/action status | 6961–6963 | Service deployment/action progress/status updates |
| LLM status | 6973 | LLM deployment/rollback progress updates |
| Adoption status | 6978 | Adoption scan/import progress updates |
| Service results | 7961–7966 | Service terminal operation results |
| LLM results | 7971–7973 | LLM route/release/deployment terminal results |
| Adoption results | 7978–7979 | Adoption scan/import terminal results |
| Encrypted results | 7980 | Bahia → Browser encrypted request-domain result |
| Registry/read models | 31961–31970 | Replaceable browser/agent read models |
| AI/ML command/results | 38390–38399 | Phase-1 addressable AI/ML command and terminal result events |
| AI/ML read models | 31980–31989 | Phase-1 replaceable AI/ML registry, state, provenance, and capability read models |
| Backup status/observations | 6981–6984 | Backup run/restore/verification progress and runtime observations |
| Backup attestations | 31310–31311 | Signed backup run and verification attestations |
| Backup read models | 31991–31999 | Replaceable backup definition, policy, repository, run, verification, restore, and runtime observation read models |
| Backup command/results | 38400–38419 | Addressable backup command and terminal result events |
| DNS read models | 31976 | DNS endpoint catalog projection when `dns.enabled=true` |
| DNS requests (reserved) | 5941–5945, 6941, 7941–7945 | Reserved DNS operator command/status/result kinds; not accepted by the reactor in Phase 0 |

### Request Events (596x)

| Kind | Name | Description |
|------|------|-------------|
| 5961 | `DeployRequest` | Request to deploy an artifact |
| 5962 | `RollbackRequest` | Request to roll back a service |
| 5963 | `ServiceAction` | Lifecycle action; signer-first direct-runtime `deploy`, `restart`, `stop` |
| 5964 | `ServiceCreate` | Create a service |
| 5965 | `EnvironmentCreate` | Create an environment |
| 5966 | `DeploymentApproval` | Approve/reject a deployment |
| 5967 | `ObservationSubmit` | Submit runtime observation |
| 5968 | `DriftRemediate` | Request drift remediation |
| 5971 | `LLMRouteCreate` | Create an LLM route registry entry |
| 5972 | `LLMReleaseRegister` | Register an immutable LLM route release |
| 5973 | `LLMDeployRequest` | Request LLM route deployment |
| 5974 | `LLMDeploymentApproval` | Approve/reject an LLM deployment intent |
| 5975 | `LLMRollbackRequest` | Request LLM route rollback |
| 5976 | `ToolProvisionRequest` | Agent → Bahia tool provisioning workflow request |
| 5977 | `ToolApprovalRequest` | Bahia → operator approval handoff event for tool provisioning |
| 5978 | `AdoptionScanRequest` | Request adoption previews for managed endpoint targets |
| 5979 | `AdoptionImportRequest` | Request adoption import for managed endpoint targets |
| 5981 | `ServiceUpdate` | Update a service registry entry |
| 5982 | `ServiceDelete` | Delete a service registry entry |
| 5983 | `EnvironmentUpdate` | Update an environment registry entry |
| 5984 | `EnvironmentDelete` | Delete an environment registry entry |
| 5985 | `ArtifactRegister` | Register an artifact |
| 5986 | `PolicyCreate` | Create a deployment policy |
| 5987 | `PolicyUpdate` | Update a deployment policy |
| 5988 | `PolicyDelete` | Delete a deployment policy |
| 5989 | `PolicyEvaluate` | Evaluate deployment policies |

Reserved DNS operator request kinds `5941`–`5945` (`DNSZoneCreate`, `DNSPolicyApply`, `DNSRecordOverride`, `DNSDriftRemediate`, `DNSBackendRegister`) are allocated for future DNS orchestration phases. Phase 0 does not subscribe to or accept them in `internal/controlplane/reactor.go`; operators must not treat them as active commands until a later implementation wires request validation and status/result replies.

### Status and Result Events

| Kind | Name | Description |
|------|------|-------------|
| 6961 | `DeploymentStatus` | Service deployment progress |
| 6962 | `ServiceStatus` | Service health/state updates |
| 6963 | `ActionStatus` | Direct-runtime service action progress |
| 6973 | `LLMDeploymentStatus` | LLM deployment/rollback progress |
| 6976 | `ToolProvisionStatus` | Tool provisioning progress |
| 6978 | `AdoptionStatus` | Adoption scan/import progress |
| 7961 | `DeploymentResult` | Service deployment terminal result |
| 7962 | `ActionResult` | Service action terminal result |
| 7963 | `ServiceCreateResult` | Service creation terminal result |
| 7964 | `EnvironmentCreateResult` | Environment creation terminal result |
| 7965 | `ObservationResult` | Observation submission terminal result |
| 7966 | `RemediationResult` | Drift remediation terminal result |
| 7971 | `LLMRouteCreateResult` | LLM route creation terminal result |
| 7972 | `LLMReleaseRegisterResult` | LLM release registration terminal result |
| 7973 | `LLMDeploymentResult` | LLM deploy/approval/rollback terminal result |
| 7976 | `ToolProvisionResult` | Tool provisioning terminal result |
| 7977 | `ToolApprovalResponse` | Operator → Bahia approval response for tool provisioning |
| 7978 | `AdoptionScanResult` | Adoption scan terminal result |
| 7979 | `AdoptionImportResult` | Adoption import terminal result |

### Replaceable Read Models

| Kind | Name | d-tag | Description |
|------|------|-------|-------------|
| 31961 | `ServiceState` | `service_id:environment_id` | Current desired/observed service state |
| 31962 | `ServiceRegistry` | `service_id` | Service registry entry |
| 31963 | `EnvironmentRegistry` | `environment_id` | Environment registry entry |
| 31964 | `LLMRouteRegistry` | `route_id` | LLM route registry entry |
| 31965 | `LLMRouteState` | `route_id:environment_id` | Current desired/observed LLM route state |
| 31966 | `ArtifactRegistry` | `artifact_id` | Artifact registry entry |
| 31967 | `DeploymentIntentRegistry` | `intent_id` | Deployment intent registry entry |
| 31968 | `DeploymentRunRegistry` | `run_id` | Deployment run registry entry |
| 31969 | `BuildRegistry` | `build_id` | Build registry entry |
| 31970 | `PolicyRegistry` | `policy_id` | Policy registry entry |
| 31976 | `DNSEndpointState` | `endpoint:<family>:<name>:<environment>` or `endpoint:worker:<name>` | DNS endpoint catalog projection derived from healthy service, LLM, ML, and worker state when `dns.enabled=true` |

DNS endpoint read models are Bahia-signed replaceable events with `t=dns-endpoint` and `t=bahia`. Deletions are published as tombstone replacements with `deleted=true`; clients should bootstrap with kind `31976`, wait for EOSE, and keep the subscription open for realtime endpoint changes.

### Phase-1 AI/ML Event Namespace

The generic AI/ML fabric uses a new event family instead of extending the existing LLM `597x/697x/797x/3196x` compatibility namespace. The existing LLM kinds remain stable for `/llm` compatibility flows. New generic AI/ML command and result events **must not** use NIP-90's `5000-7000` Data Vending Machine range; NIP-90 reserves `5000-5999` for job requests, `6000-6999` for job results, and `7000` for feedback. Bahia phase-1 AI/ML events therefore use `38390-38399` command/result kinds plus `31980-31989` replaceable read models.

Rollout note: this namespace is the phase-1 Bahia policy and public spec candidate track. Implementations should preserve field names and compatibility notes carefully, but standardization work must not block the initial Hugging Face → vLLM and follow-on recipe slices.

#### AI/ML command/result events (`38390-38399`)

Command/result events are addressable and use `d=<idempotency-key-or-request-id>` so reconnects, relay replay, and client retries can collapse duplicate work without polling. Commands describe intent; terminal result kinds close the correlated workflow. Intermediate progress should be projected through read models and any future status events rather than HTTP completion checks.

| Kind | Name | Purpose |
|------|------|---------|
| 38390 | `MLRecipeRunRequest` | Request a recipe run |
| 38391 | `MLInferenceDeployRequest` | Request inference endpoint deployment |
| 38392 | `MLInferenceDeploymentApproval` | Approve or reject an inference deployment |
| 38393 | `MLInferenceRollbackRequest` | Request endpoint rollback |
| 38394 | `MLModelImportRequest` | Request model/model-version import |
| 38395 | `MLRecipeRunResult` | Recipe run terminal result |
| 38396 | `MLInferenceDeployResult` | Inference deployment terminal result |
| 38397 | `MLInferenceDeploymentApprovalResult` | Approval/rejection terminal result |
| 38398 | `MLInferenceRollbackResult` | Rollback terminal result |
| 38399 | `MLModelImportResult` | Model/model-version import terminal result |

Defer dataset import, evaluation, benchmark, fine-tune, and experiment command kinds until after the first implementation slice proves the namespace. Evaluation and benchmark state may still be projected later through `31987`.

Required command/result tag rules:

- Every command event uses `d=<idempotency-key-or-request-id>` and enough scoped tags for relay filtering, such as `model`, `model_version`, `recipe`, `run`, `endpoint`, `environment`, `deployment`, `artifact`, `worker`, `runtime`, `task`, or `accelerator`.
- Every result event includes `e=<request_event_id>` with reply semantics, `p=<requester_pubkey>`, `status=<queued|running|succeeded|failed|rejected>`, and the same relevant scoped resource tags.
- Result content must carry terminal payload or error details. Clients must check both the result `status` tag and content error fields; HTTP/MCP acknowledgement is not terminal truth.
- Consumers deduplicate by event id and by `(kind, pubkey, d-tag)` for addressable command replays. When two valid commands share the same idempotency `d` coordinate, processors must treat the latest accepted command as a replay of the same logical request, not a second independent workflow.

Example `38390` recipe request:

```json
{
  "kind": 38390,
  "content": {
    "recipe": "recipe:hf-vllm-import-deploy:1",
    "inputs": {
      "model_source": "hf://Qwen/Qwen2.5-Coder-32B-Instruct@<commit-sha>"
    },
    "parameters": {
      "target_environment": "prod",
      "auto_deploy": true
    }
  },
  "tags": [
    ["d", "recipe-run:qwen-coder-prod-20260516"],
    ["recipe", "recipe:hf-vllm-import-deploy:1"],
    ["source", "huggingface"],
    ["task", "chat_completions"],
    ["runtime", "vllm"]
  ]
}
```

Example `38396` deployment result:

```json
{
  "kind": 38396,
  "content": {
    "request_event_id": "<38391-event-id>",
    "endpoint": "endpoint:qwen-coder:prod",
    "run": "deployment-run:<uuid>",
    "message": "deployed"
  },
  "tags": [
    ["d", "result:<38391-event-id>"],
    ["e", "<38391-event-id>", "", "reply"],
    ["p", "<requester-pubkey>"],
    ["status", "succeeded"],
    ["endpoint", "endpoint:qwen-coder:prod"],
    ["environment", "prod"],
    ["deployment", "deployment-run:<uuid>"],
    ["runtime", "vllm"]
  ]
}
```

#### AI/ML replaceable read models (`31980-31989`)

Read models are replaceable/addressable projections for browser, agent, REST, and MCP compatibility views. Latest valid event wins for `(kind, pubkey, d-tag)`; clients should bootstrap with scoped filters, wait for EOSE, then keep subscriptions open for realtime changes.

| Kind | Name | d-tag coordinate examples | Purpose |
|------|------|---------------------------|---------|
| 31980 | `MLModelRegistry` | `model:<slug>` | Model registry/read model |
| 31981 | `MLModelVersionRegistry` | `model-version:<model-slug>:<version>` | Model version registry/read model |
| 31982 | `MLDatasetRegistry` | `dataset:<slug>:<version>` | Dataset registry/read model |
| 31983 | `MLRecipeRegistry` | `recipe:<name>:<version>` | Recipe registry/read model |
| 31984 | `MLRecipeRunState` | `recipe-run:<run-id>` | Recipe run state |
| 31985 | `MLInferenceEndpointRegistry` | `endpoint:<name>:<environment>` | Inference endpoint registry |
| 31986 | `MLInferenceEndpointState` | `endpoint-state:<name>:<environment>` | Inference endpoint desired/observed state |
| 31987 | `MLEvaluationExperimentState` | `evaluation:<id>` or `experiment:<id>` | Evaluation/experiment state |
| 31988 | `MLArtifactProvenanceGraph` | `artifact:<sha256>` | Artifact provenance graph |
| 31989 | `MLRuntimeCapabilityProfile` | `worker:<pubkey>:ai-capability` | Runtime/capability profile |

Example `31981` model version projection:

```json
{
  "kind": 31981,
  "content": {
    "source": {
      "kind": "huggingface",
      "uri": "hf://Qwen/Qwen2.5-Coder-32B-Instruct",
      "revision": "<commit-sha>"
    },
    "runtime_requirements": {
      "preferred_runtimes": ["vllm"],
      "min_vram_gb": 48
    }
  },
  "tags": [
    ["d", "model-version:qwen2.5-coder-32b:v1"],
    ["model", "model:qwen2.5-coder-32b"],
    ["version", "v1"],
    ["format", "safetensors"],
    ["runtime", "vllm"],
    ["sha256", "..."]
  ]
}
```

#### AI/ML REST/MCP correlation contract

REST and MCP may initiate compatible AI/ML tooling flows, but they must return correlation metadata instead of claiming completion for long-running work. A successful synchronous response includes the Nostr request event id, request kind, expected terminal result kind, relevant read-model kinds, the requester pubkey, and scoped tags such as `endpoint`, `environment`, `model_version`, `recipe`, or `run`. Clients subscribe with those tags, wait for EOSE for historical catch-up, process realtime result/read-model events, and never poll REST/MCP for completion.

### Backup Event Namespace

Backup control-plane events use a dedicated namespace so backup definitions, policies, repositories, runs, verification, restores, retention, observations, and attestations do not collide with existing Bahia kinds. This allocation intentionally avoids `31200`, which is already used for artifact signature attestations, and avoids `38395`/`38396`, which are AI/ML terminal result kinds.

Backup commands and results are addressable and use `d=<idempotency-key-or-request-id>` for command correlation. Processors must deduplicate by event id and by `(kind, pubkey, d-tag)` so reconnects, relay replay, and client retries do not create duplicate logical workflows.

#### Backup command events (`38400-38409`)

| Kind | Name | Purpose |
|------|------|---------|
| 38400 | `BackupRunRequest` | Request a backup run |
| 38401 | `BackupVerificationRequest` | Request verification for an existing backup run or snapshot |
| 38402 | `BackupRestoreRequest` | Request restore orchestration |
| 38403 | `BackupRestoreApproval` | Approve or reject a restore request |
| 38404 | `BackupRetentionEnforceRequest` | Request retention enforcement |
| 38405 | `BackupRepositoryRegisterRequest` | Register or update a backup repository record |
| 38406 | `BackupPolicyApplyRequest` | Apply a backup policy record |
| 38407 | `BackupRecipeApplyRequest` | Apply a backup recipe record |
| 38408 | `BackupDefinitionApplyRequest` | Apply a backup definition record |
| 38409 | `BackupRepositoryProbeRequest` | Probe backend repository health/capabilities |

#### Backup terminal result events (`38410-38419`)

| Kind | Name | Purpose |
|------|------|---------|
| 38410 | `BackupRunResult` | Backup run terminal result |
| 38411 | `BackupVerificationResult` | Verification terminal result |
| 38412 | `BackupRestoreResult` | Restore terminal result |
| 38413 | `BackupRestoreApprovalResult` | Restore approval/rejection terminal result |
| 38414 | `BackupRetentionResult` | Retention enforcement terminal result |
| 38415 | `BackupRepositoryRegisterResult` | Repository registration terminal result |
| 38416 | `BackupPolicyApplyResult` | Policy apply terminal result |
| 38417 | `BackupRecipeApplyResult` | Recipe apply terminal result |
| 38418 | `BackupDefinitionApplyResult` | Definition apply terminal result |
| 38419 | `BackupRepositoryProbeResult` | Repository probe terminal result |

#### Backup status and observation events (`6981-6984`)

| Kind | Name | Purpose |
|------|------|---------|
| 6981 | `BackupRunStatus` | Backup run progress |
| 6982 | `BackupRestoreStatus` | Restore progress |
| 6983 | `BackupVerificationStatus` | Verification progress |
| 6984 | `BackupObservation` | Runtime/backend observation |

#### Backup replaceable read models (`31991-31999`)

Read models are latest-wins projections for `(kind, pubkey, d-tag)`. Implemented mutable registry records use immutable UUID-backed d-tags so renames do not leave stale live coordinates. Clients bootstrap with scoped filters, wait for EOSE, then keep subscriptions open for realtime updates.

| Kind | Name | d-tag coordinate examples | Purpose |
|------|------|---------------------------|---------|
| 31991 | `BackupDefinitionRegistry` | `backup-definition:<name>` | Backup definition registry/read model |
| 31992 | `BackupPolicyRegistry` | `backup-policy:<policy-id>` | Backup policy registry/read model |
| 31993 | `BackupRepositoryRegistry` | `backup-repository:<repository-id>` | Backup repository registry/read model |
| 31994 | `BackupRetentionRegistry` | `backup-retention:<name>` | Retention registry/read model |
| 31995 | `BackupRecipeRegistry` | `backup-recipe:<recipe-id>` | Backup recipe registry/read model |
| 31996 | `BackupRunState` | `backup-run:<run-id>` | Backup run state |
| 31997 | `BackupVerificationState` | `backup-verification:<run-id>` | Backup verification state |
| 31998 | `BackupRestoreState` | `backup-restore:<restore-id>` | Backup restore state |
| 31999 | `BackupRuntimeObservationState` | `backup-observation:<worker-or-site>` | Backup runtime observation state |

#### Backup attestation events (`31310-31311`)

| Kind | Name | Purpose |
|------|------|---------|
| 31310 | `BackupRunAttestation` | Signed run provenance attestation |
| 31311 | `BackupVerificationAttestation` | Signed verification provenance attestation |

Required backup command tags:

- `d=<idempotency-key-or-request-id>` is required on every command.
- Include `p=<target-agent-or-service-pubkey>` when routing to a specific agent/service.
- Include narrow scoped tags as applicable: `t` or `task`, `target`, `backend`, `repository`, `policy`, `recipe`, `run`, `worker`, `site`, `environment`, and `verification`.
- Command content describes intent; it must not contain backend credentials or public production secret material.

Required backup result tags:

- `d=result:<request_event_id>` is required.
- `e=<request_event_id>` with reply semantics and `p=<requester_pubkey>` are required.
- `status=<queued|running|succeeded|failed|rejected>` is required.
- Results echo the relevant scoped command tags and include `run=<backup-run-id>` when durable run state exists.
- Result content carries terminal payload, verification evidence summary, relay publish outcome summary, or explicit error details. Clients must treat result status and content error fields as terminal truth, not REST/MCP acknowledgement.

Restore and verification consumers must not infer restore eligibility from snapshot existence alone. Read models that expose restore eligibility must derive it from terminal run success plus successful verification according to the active backup policy.

### Signer-First Operator Actions

Operator workflows are public signed control-plane requests. They are not RPC and must be consumed as event streams: publish the request, subscribe for `e=<request_event_id>` replies, process `696x`/`697x` status events as progress, and treat the corresponding `796x`/`797x` result event as terminal. Clients should not poll or use timeout-based completion; use EOSE for historical catch-up and keep the subscription open for realtime replies.

CLI behavior:

- `bahia adopt scan|import` and `bahia services actions deploy|restart|stop` use signer-first Nostr requests by default.
- Relay resolution is deterministic: repeatable `--relay` flags, then comma-separated `BAHIA_NOSTR_RELAYS`, then `Nostr discovery events (kind 31974 + NIP-51 kind 30002)` discovery from `nostr.browser_relays` plus `nostr.sidecar_url`.
- Live status chatter is written to stderr only in table mode; JSON/YAML stdout remains reserved for the final result payload.
- `--http-fallback` (or `BAHIA_OPERATOR_HTTP_FALLBACK=true`) is explicit compatibility mode and is only safe before any relay accepts the signed request, such as signer/relay discovery failure or publish with zero accepted relays.
- `--raw-target` is compatibility-only. It skips the public signer-first adoption path and requires explicit `--http-fallback`; use `--target` endpoint refs for the signer-first path.

Authorization uses event pubkeys only:

- `nostr.authorized_pubkeys` is the global fallback for all public operator requests.
- `adoption.allowed_pubkeys` additionally authorizes `5978`/`5979` adoption requests.
- `direct_runtime_actions.allowed_pubkeys` additionally authorizes direct-runtime `5963` requests.
- Subject/email operator allowlists remain HTTP/NIP-98 compatibility settings and are ignored by signer-first public events.

#### Adoption scan/import (`5978`/`5979`)

Adoption requests are public relay-visible content, so targets must reference server-managed runtime endpoints. Raw Docker transport material is forbidden.

Scan request content:

```json
{
  "targets": [
    {
      "name": "prod",
      "endpoint_ref": "prod-docker",
      "environment_name": "prod"
    }
  ]
}
```

Import request content:

```json
{
  "targets": [{ "name": "prod", "endpoint_ref": "prod-docker" }],
  "import_all": true,
  "selections": [
    {
      "target_name": "prod",
      "container_id": "abc123",
      "service_name_override": "api"
    }
  ]
}
```

Rules:

- `targets` is required and non-empty.
- Each target requires normalized `name` and non-empty `endpoint_ref`.
- `docker_host` is rejected on the public signer-first path.
- Import requires `import_all=true` or at least one `selection`.

Progress is published as `6978 AdoptionStatus` with `status=processing`, `operation=scan|import`, repeated `target`, `endpoint_ref`, and optional `environment_name` tags. Terminal results are:

- `7978 AdoptionScanResult` with content `[]AdoptionPreviewResponse`.
- `7979 AdoptionImportResult` with content `[]AdoptionImportResultResponse`.

Both result payloads reuse the HTTP-safe DTO projection: only safe env/labels are included, redacted key names are preserved, and managed endpoint `docker_host` values are omitted.

#### Direct-runtime actions (`5963`)

Signer-first direct-runtime actions reuse `5963 ServiceAction` with JSON content:

```json
{
  "action": "deploy",
  "service_id": "...",
  "environment_id": "...",
  "artifact_id": "..."
}
```

Rules:

- `action` must be one of `deploy`, `restart`, or `stop`.
- `service_id` and `environment_id` are required UUIDs.
- `artifact_id` is optional for `deploy` and invalid for `restart`/`stop`.
- Existing non-direct-runtime `5963` tag-based actions remain compatibility acknowledgements.

Progress is published as `6963 ActionStatus` with `status=processing`, `step=executing`, `action`, `service`, `environment`, and optional `artifact` tags. Success publishes `7962 ActionResult` with content `RuntimeActionResponse`, including the runtime observation when available. Failures publish `7962 ActionResult` with `status=failed`, `action`, resource tags, and error content.

### Encrypted Request/Result Events (5980/7980)

Sensitive browser route families and encrypted request-domain actions (notifications, orgs, payments, service secrets, stored deployment run logs, and artifact signature verification) use encrypted request/result events instead of public read models. These events are intentionally **not** accepted by the public relay sidecar policy and must be sent only to operator-configured relay URLs for encrypted request/result traffic.

Discovery/config contract:

- Backend-only relay URLs for encrypted request/result handling are configured as `nostr.encrypted_request_relays` and are not exposed by `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`.
- Browser-discoverable relay URLs for encrypted request/result handling are configured as `nostr.browser_encrypted_request_relays` and are exposed as `nostr.browser_encrypted_request_relays`.
- `Nostr discovery events (kind 31974 + NIP-51 kind 30002).features.encrypted_nostr_requests=true` means the backend has a service key, at least one backend `nostr.encrypted_request_relays` subscription target, and at least one browser encrypted-request relay URL advertised.
- Browser clients must keep public `nostr.browser_relays` / `nostr.sidecar_url` separate from `nostr.browser_encrypted_request_relays`; sensitive payloads must never be published to the public sidecar relay.

Event contract:

- Request kind: `5980`; result kind: `7980`.
- Request cleartext tags are limited to routing/correlation metadata such as `p=<service_pubkey>` and `encrypted=bahia-encrypted-v1`.
- Request `content` is NIP-44 encrypted to the Bahia service pubkey and contains `{version, operation, requester_pubkey, payload}`.
- Result tags include `e=<request_event_id>` with reply marker, `p=<requester_pubkey>`, `encrypted=bahia-encrypted-v1`, and terminal `status`.
- Result `content` is NIP-44 encrypted to the requester pubkey and contains `{version, request_event_id, status, payload?, error?}`.
- Backend handlers reject unauthorized requesters before decrypting/dispatching domain operations, publish encrypted terminal errors for decrypt/validation failures, and deduplicate by event id.

Browser signer support:

- NIP-07 is supported only when `window.nostr.nip44.encrypt/decrypt` are available.
- NIP-46 can participate only if the provider explicitly exposes `provider.nip44.encrypt/decrypt`; NIP-46's internal encrypted RPC channel does not by itself give the web app NIP-44 conversation-key operations. If absent, encrypted request/result route migration is blocked for that signer mode and the UI/tests should surface that exact blocker.

Encrypted operation catalog:

The following operation names are normative for the `5980`/`7980` encrypted request/result family. New encrypted browser-facing operations must be added here when introduced so the documented contract stays aligned with the implementation.

Notification encrypted operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `notifications.channels.list` | `{}` | `{channels}` | Channel configs are encrypted in transit; webhook `config.secret` is omitted from results. |
| `notifications.channels.get` | `{id}` | `{channel}` | Returns one sanitized channel or an encrypted terminal error. |
| `notifications.channels.create` | channel fields | `{channel}` | Webhook secrets are accepted only as encrypted write payloads. |
| `notifications.channels.update` | `{id, ...fields}` | `{channel}` | Omitted webhook secrets preserve the stored secret; returned channel is sanitized. |
| `notifications.channels.delete` | `{id}` | `{status,id}` | Deletes the channel over encrypted request/result events. |
| `notifications.channels.test` | `{id}` | `{status,id}` | Dispatches directly to the selected channel and returns terminal success/error. |
| `notifications.logs.list` | `{limit?,channel_id?}` | `{logs}` | Delivery logs and payloads are returned only in encrypted result content. |

Encrypted domain operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `payments.history` | `{worker,limit?}` | `PaymentRecord[]` | `worker` is required; `limit` defaults to 50 and is capped at 250. |
| `orgs.list` | `{}` | `({org fields..., role})[]` | Returns orgs visible to the requester pubkey with the caller's role attached to each row. |
| `orgs.detail` | `{id}` | `{org,members,invites,my_role}` | `id` may be an org UUID or org name; `invites` is populated only when the requester has admin access. |
| `orgs.create` | `{name,display_name?}` | `Organization` | Creates an organization for an authorized requester and returns the created org object directly. |
| `orgs.delete` | `{id}` | `{message}` | Deletes the organization when the requester is authorized. |
| `orgs.my_invites` | `{}` | `InviteWithOrg[]` | Returns invites for the requester pubkey enriched with org name/display name. |
| `orgs.accept_invite` | `{invite_id}` | `OrgMember` | Accepts an org invite for the requester pubkey and returns the created membership directly. |
| `orgs.create_invite` | `{org_id,pubkey,role?,expires_in?}` | `OrgInvite` | `role` defaults to `viewer`; `expires_in` is in hours and defaults to 72. |
| `orgs.revoke_invite` | `{org_id,invite_id}` | `{message}` | Revokes an existing invite. |
| `orgs.update_member_role` | `{org_id,pubkey,role}` | `{message}` | Updates member role state through encrypted transport. |
| `orgs.remove_member` | `{org_id,pubkey}` | `{message}` | Removes a member from the org. |

Encrypted route operations:

| Operation | Payload | Result payload | Notes |
|-----------|---------|----------------|-------|
| `services.secrets.list` | `{service_id}` | `{secrets,total}` | Returns secret refs only; plaintext/encrypted values are omitted. |
| `services.secrets.create` | `{service_id,name,value,environment_id?,encryption_method?}` | `{secret,status}` | Secret value is encrypted in the request and at rest; result contains metadata only. |
| `services.secrets.update` | `{service_id,secret_id,value,encryption_method?}` | `{secret,status}` | Re-encrypts the new value; result contains metadata only. |
| `services.secrets.delete` | `{service_id,secret_id}` | `{status,secret_id}` | Validates the secret belongs to the service before deletion. |
| `services.secrets.reveal` | `{service_id,secret_id}` | `{secret,value}` | Plaintext is returned only in the encrypted result for explicit reveal actions. |
| `deployments.run_logs.get` | `{run_id,tail?,stream?}` | `{logs,stream}` | Stored stdout/stderr snapshots are encrypted result content; public run projections carry metadata only. |
| `artifacts.signatures.verify` | `{artifact_id}` | `{found,stored,verified,discovered,rejected,errors,signatures}` | Verification is triggered by encrypted signed requests and stores discovered signature records. |

### Correlation Tags

Use tags for relay-side filtering and MCP follow-up subscriptions. Service flows use `service`, `environment`, `artifact`, `intent`, and `run`. LLM flows use `route`, `release`, `environment`, `intent`, and `run`. Status/result replies also include `e` with marker `reply`, `p` for the requester pubkey, plus `status` and `step` where applicable. Encrypted result replies use the same `e`/`p` pattern but keep payloads encrypted. MCP async LLM tools return the request event id and the relevant request/status/result/read-model kind ids so clients can subscribe directly rather than polling.

Clients should wait for EOSE on bootstrap queries, then keep subscriptions open for live updates. Deduplicate by event id; for replaceable events, latest `created_at` wins for `(kind, pubkey, d-tag)`. Deletions use tombstone content/tags (`deleted=true`), not Nostr delete events.

---

## Authorization

- **Signer-first browser identity**: web sessions are signer-first (NIP-07 or NIP-46), with signer pubkey as the primary user identity.
- **Nostr requests**: event signatures are verified and request kinds require authorized operator pubkeys.
- **Control-plane operator allowlist**: `nostr.authorized_pubkeys` is for control-plane/operator request authorization only.
- **Tenant bootstrap owner allowlist**: `auth.bootstrap_owner_pubkeys` governs who may create organizations over REST compatibility endpoints when configured.
- **REST and MCP HTTP**: use direct NIP-98 (`Authorization: Nostr <base64event>`) when auth is enabled. `Authorization: Bearer ...` is rejected with `401` rather than treated as a fallback.
- **REST role in architecture**: REST is a compatibility transport for narrowed CRUD/query surfaces that have not yet moved to Nostr-native flows.

---

## Deprecated / Quarantined Event Kinds

The legacy 311xx command bridge is deprecated and logs warnings when received. New integrations must use the canonical 596x request kinds.

| Deprecated | Replacement |
|------------|-------------|
| 31102 `KindCmdIntentCreate` | 5961 `KindDeployRequest` |
| 31103/31104 approval/rejection | 5966 `KindDeploymentApproval` |
| 31105 rollback | 5962 `KindRollbackRequest` |
