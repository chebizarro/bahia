# Bahia Nostr Event Specification

## Overview

Bahia publishes signed Nostr events to relay networks for traceability and automation. All events are published as parameterized replaceable events (kind 30000-39999 range) using NIP-33.

## Event Kinds

| Kind | Label | Description |
|------|-------|-------------|
| 31000 | `build.registered` | A new build has been registered |
| 31001 | `artifact.registered` | A new artifact has been registered |
| 31002 | `deployment.created` | A deployment intent has been created |
| 31003 | `deployment.completed` | A deployment run has completed |
| 31004 | `drift.detected` | Drift has been detected between desired and observed state |
| 31005 | `runtime.observation` | A runtime observation has been recorded |

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

## Protocol Compatibility Matrix

| Kind | Name | Direction | Bahia Role |
|------|------|-----------|------------|
| 10100 | Worker Advertisement | Subscribe (future) | Discover available Loom workers |
| 5100 | Job Request | **Publish** | Submit deployment / build jobs |
| 30100 | Job Status Update | Subscribe | Monitor running job status |
| 5101 | Job Result | Subscribe | Receive final result (exit code, logs) |
| 5102 | Job Cancellation | **Publish** | Cancel running / queued jobs |
| 31000–31005 | Bahia Audit Events | **Publish** | Emit build, deploy, drift events |

## Event Storage

All published and received Nostr events are stored in the `nostr_events` table for local audit trail, indexed by kind and entity reference.
