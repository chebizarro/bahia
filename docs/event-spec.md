# Bahia Nostr Event Specification

## Overview

Bahia publishes signed Nostr events to relay networks for traceability, automation, and control-plane state. Audit/activity events use the 31000-series, replaceable read models use NIP-33-style d-tagged 3196x events, and request/status/result flows use the canonical 596x/696x/796x series.

## Event Kinds

| Kind | Label | Description |
|------|-------|-------------|
| 31000 | `build.registered` | A new build has been registered |
| 31001 | `artifact.registered` | A new artifact has been registered |
| 31002 | `deployment.created` | A deployment intent has been created |
| 31003 | `deployment.completed` | A deployment run has completed |
| 31004 | `drift.detected` | Drift has been detected between desired and observed state |
| 31005 | `runtime.observation` | A runtime observation has been recorded |
| 31014 | `llm_route.projection` | LLM route registry projection emitted |
| 31015 | `llm_release.registered` | LLM release registration emitted |
| 31016 | `llm_deployment.*` | LLM deployment intent/approval/rejection emitted |
| 31017 | `llm_deployment_run.*` | LLM deployment run lifecycle emitted |
| 31018 | `llm_route_state.*` | LLM route state/observation/drift emitted |
| 31019 | `llm_gateway_route.synced` | LLM gateway route synchronization emitted |

## Internal Operational Event Types

Bahia also emits typed in-process audit events used by Nostr read-model projectors, automation subscribers, and local observability wiring. Adoption and direct-runtime events are deliberately structured around IDs and counts; they must not contain secret values, raw environment values, Docker TLS material, or bearer/NIP-98 credentials.

| Type | Description | Key fields |
|------|-------------|------------|
| `adoption.scan_completed` | Adoption dry-run scan completed | `target_count`, `candidate_count`, `target_error_count`, `redacted_env_key_count`, `redacted_label_key_count`, `duration_ms` |
| `adoption.imported` | One adoption candidate was persisted | `service_id`, `environment_id`, `artifact_id`, `target_name`, `container_id`, `container_name`, `status` |
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

## Event Structure

All events follow this structure:

```json
{
  "kind": 31000,
  "content": "<JSON-encoded event data>",
  "tags": [
    ["t", "build.registered"],
    ["d", "<entity-id>"]
  ],
  "created_at": 1234567890,
  "pubkey": "<bahia-service-pubkey>",
  "sig": "<schnorr-signature>"
}
```

## Loom Integration Events

Bahia uses the [Loom Protocol](../loom-protocol/SPECIFICATION.md) for decentralised compute.

### Job Request (Kind 5100)
Published when Bahia submits a deployment job to Loom workers:

```json
{
  "kind": 5100,
  "content": "",
  "tags": [
    ["cmd", "bash"],
    ["args", "-c", "docker pull image@sha256:abc... && docker run -d --name api image@sha256:abc..."],
    ["p", "<worker_pubkey>"],
    ["payment", "<cashu_token>"],
    ["env", "BAHIA_DEPLOY_SERVICE", "api"],
    ["env", "BAHIA_DEPLOY_ENVIRONMENT", "staging"],
    ["env", "BAHIA_DEPLOY_IMAGE", "harbor.example.com/app/api:v1.2.3"],
    ["env", "BAHIA_DEPLOY_DIGEST", "sha256:abc123..."],
    ["env", "BAHIA_DEPLOY_TYPE", "deploy"]
  ]
}
```

### Job Status Update (Kind 30100)
Received from Loom workers during job execution:

```json
{
  "kind": 30100,
  "content": "Executing command...\nPulling image...",
  "tags": [
    ["d", "<job-request-event-id>"],
    ["e", "<job-request-event-id>"],
    ["p", "<client-pubkey>"],
    ["status", "running"]
  ]
}
```

### Job Result (Kind 5101)
Received from Loom workers upon job completion:

```json
{
  "kind": 5101,
  "content": "",
  "tags": [
    ["e", "<job-request-event-id>"],
    ["p", "<client-pubkey>"],
    ["success", "true"],
    ["exit_code", "0"],
    ["duration", "56"],
    ["stdout", "https://blossom.server/job-stdout.log"],
    ["stderr", "https://blossom.server/job-stderr.log"],
    ["change", "<cashu_change_token>"]
  ]
}
```

### Job Cancellation (Kind 5102)
Published by Bahia to cancel a running or queued job:

```json
{
  "kind": 5102,
  "content": "",
  "tags": [
    ["e", "<job-request-event-id>"],
    ["p", "<worker_pubkey>"]
  ]
}
```

## LLM Control-Plane Events

Bahia's LLM control plane uses the canonical Nostr contract in `internal/controlplane/reactor.go` and publishes database-backed projections through `internal/adapters/nostr/projector.go`.

### LLM Requests (Kinds 5971–5975)

Operators and MCP tools publish signed request events for route creation, release registration, deployment, approval/rejection, and rollback:

```json
{
  "kind": 5973,
  "content": "{\"route_id\":\"...\",\"environment_id\":\"...\",\"release_id\":\"...\"}",
  "tags": [
    ["route", "<route_id>"],
    ["environment", "<environment_id>"],
    ["release", "<release_id>"]
  ]
}
```

### LLM Status and Results (Kinds 6973, 7971–7973)

Bahia publishes threaded replies to the original request. Deploy, approval, and rollback share kind `7973`; route create and release register use `7971` and `7972` respectively.

```json
{
  "kind": 6973,
  "content": "{\"status\":\"processing\",\"step\":\"provisioning\",\"message\":\"deploying LLM route\"}",
  "tags": [
    ["e", "<request_event_id>", "", "reply"],
    ["p", "<requester_pubkey>"],
    ["route", "<route_id>"],
    ["environment", "<environment_id>"],
    ["intent", "<intent_id>"],
    ["run", "<run_id>"],
    ["status", "processing"],
    ["step", "provisioning"]
  ]
}
```

### LLM Read Models (Kinds 31964–31965)

LLM route registry and route-state projections are Bahia-signed replaceable events. Clients query them, wait for EOSE, and then keep the subscription open for live updates.

```json
{
  "kind": 31965,
  "content": "{\"route_id\":\"...\",\"environment_id\":\"...\",\"desired_release_id\":\"...\",\"gateway_status\":\"synced\"}",
  "tags": [
    ["d", "<route_id>:<environment_id>"],
    ["route", "<route_id>"],
    ["environment", "<environment_id>"],
    ["release", "<desired_release_id>"],
    ["intent", "<desired_intent_id>"],
    ["run", "<active_run_id>"],
    ["gateway", "synced"],
    ["backend", "vllm"]
  ]
}
```

**Bahia processing:**
1. Validates request event ID/signature/timestamp and authorized pubkey.
2. Mutates LLM registry/deployment state through `LLMRegistryService`.
3. Publishes status/result replies with `route`, `release`, `environment`, `intent`, and `run` tags.
4. Projects `31964`/`31965` read models and `31014–31019` audit/activity events.

## Hive-CI Integration Events

Bahia subscribes to [Hive-CI](../hive-ci-protocol/SPECIFICATION.md) events to auto-ingest CI workflow results.

### Workflow Run (Kind 5401)
Received from Hive-CI dispatchers when a workflow starts:

```json
{
  "kind": 5401,
  "pubkey": "<trusted-dispatcher-pubkey>",
  "content": "",
  "tags": [
    ["a", "30618:abc123...:my-project"],
    ["workflow", ".github/workflows/build.yml"],
    ["commit", "abc123def456"],
    ["branch", "main"],
    ["trigger", "push"],
    ["triggered-by", "<user-pubkey>"],
    ["publisher", "<ephemeral-pubkey>"]
  ]
}
```

### Workflow Result (Kind 5402)
Received from the ephemeral key declared in the 5401 event:

```json
{
  "kind": 5402,
  "pubkey": "<ephemeral-pubkey>",
  "content": "",
  "tags": [
    ["e", "<workflow-run-event-id>"],
    ["log_url", "https://blossom.server/workflow.log"],
    ["status", "success"],
    ["exit_code", "0"],
    ["duration", "234"],
    ["image_repo", "registry.sharegap.net/cascadia/myapp"],
    ["image_tag", "v1.2.3"],
    ["image_digest", "sha256:abc123..."]
  ]
}
```

**Bahia processing:**
1. Validates `5402.pubkey == 5401.publisher` (ephemeral key relationship)
2. Creates Build record from CI result
3. Verifies image exists in OCI registry
4. Creates Artifact linked to Build
5. (If configured) Creates staging DeploymentIntent

## Protocol Compatibility Matrix

| Kind | Name | Direction | Bahia Role |
|------|------|-----------|------------|
| 10100 | Worker Advertisement | Subscribe (future) | Discover available Loom workers |
| 5100 | Job Request | **Publish** | Submit deployment / build jobs |
| 30100 | Job Status Update | Subscribe | Monitor running job status |
| 5101 | Job Result | Subscribe | Receive final result (exit code, logs) |
| 5102 | Job Cancellation | **Publish** | Cancel running / queued jobs |
| 5401 | Workflow Run | Subscribe | Receive CI workflow start (Hive-CI) |
| 5402 | Workflow Result | Subscribe | Receive CI workflow result (Hive-CI) |
| 31000–31019 | Bahia Audit Events | **Publish** | Emit build, deploy, drift, and LLM lifecycle events |
| 31964 | LLM Route Registry | **Publish** | Replaceable LLM route registry read model |
| 31965 | LLM Route State | **Publish** | Replaceable LLM route/environment state read model |
| 5971–5975 | LLM Requests | Subscribe | Consume authorized LLM control-plane commands |
| 6973 | LLM Deployment Status | **Publish** | Emit LLM deployment/rollback progress |
| 7971–7973 | LLM Results | **Publish** | Emit LLM route/release/deployment terminal results |

## Event Storage

All published and received Nostr events are stored in the `nostr_events` table for local audit trail, indexed by kind and entity reference.
