# Bahia Nostr Command Events

Bahia can be operated entirely via Nostr events, without the REST API.
Inbound command events are ingested by the Nostr subscriber, persisted to the
`nostr_events` audit table, and processed by the event processor.

## Event Kind Overview

| Kind  | Label                       | REST Equivalent              |
|-------|-----------------------------|------------------------------|
| 31100 | `build.register`            | `POST /api/v1/builds`        |
| 31101 | `artifact.register`         | `POST /api/v1/artifacts`     |
| 31102 | `deployment.intent.create`  | `POST /api/v1/intents`       |
| 31103 | `deployment.intent.approve` | `POST /api/v1/intents/:id/approve` |
| 31104 | `deployment.intent.reject`  | `POST /api/v1/intents/:id/reject`  |
| 31105 | `rollback.request`          | `POST /api/v1/rollback`      |

## Common Structure

All command events are **parameterized replaceable** (kind 31000–31999) and
MUST be signed by a key authorized to perform the corresponding action.

```json
{
  "kind": 31100,
  "pubkey": "<author-pubkey-hex>",
  "content": "<JSON-encoded command payload>",
  "tags": [
    ["d", "<idempotency-key>"],
    ["t", "build.register"]
  ],
  "created_at": 1234567890,
  "sig": "<schnorr-signature>"
}
```

- The `d` tag is the idempotency key — re-publishing the same `d` value
  replaces the previous event (NIP-33).
- The `t` tag contains the human-readable label.
- The `content` field is a JSON object matching the corresponding REST request DTO.

## Kind 31100 — `build.register`

Register a new build.

**Content** (JSON):
```json
{
  "service_id": "uuid",
  "git_sha": "abc123",
  "git_ref": "refs/heads/main",
  "ci_system": "hive-ci",
  "ci_run_id": "run-42",
  "status": "succeeded",
  "metadata": {}
}
```

**Tags**:
- `["d", "<service_id>-<git_sha>"]`
- `["t", "build.register"]`
- `["service", "<service_id>"]` — enables filtering by service

## Kind 31101 — `artifact.register`

Register a new container artifact.

**Content** (JSON):
```json
{
  "build_id": "uuid",
  "service_id": "uuid",
  "image_repo": "ghcr.io/org/app",
  "image_tag": "v1.2.3",
  "image_digest": "sha256:abc...",
  "manifest_media_type": "application/vnd.oci.image.manifest.v1+json",
  "size_bytes": 52428800,
  "metadata": {}
}
```

**Tags**:
- `["d", "<image_digest>"]`
- `["t", "artifact.register"]`
- `["service", "<service_id>"]`

## Kind 31102 — `deployment.intent.create`

Create a deployment intent (request to deploy an artifact to an environment).

**Content** (JSON):
```json
{
  "service_id": "uuid",
  "environment_id": "uuid",
  "artifact_id": "uuid",
  "requested_by": "npub...",
  "source_kind": "nostr",
  "metadata": {}
}
```

**Tags**:
- `["d", "<service_id>-<environment_id>-<artifact_id>"]`
- `["t", "deployment.intent.create"]`
- `["service", "<service_id>"]`
- `["environment", "<environment_id>"]`

**Note**: When auth is enabled, `requested_by` is overridden with the event
author's pubkey (same as the REST API `resolveActor` behaviour).

## Kind 31103 — `deployment.intent.approve`

Approve a pending deployment intent.

**Content** (JSON):
```json
{
  "intent_id": "uuid"
}
```

**Tags**:
- `["d", "<intent_id>-approve"]`
- `["t", "deployment.intent.approve"]`
- `["e", "<intent_id>"]` — references the intent

## Kind 31104 — `deployment.intent.reject`

Reject a pending deployment intent.

**Content** (JSON):
```json
{
  "intent_id": "uuid",
  "reason": "optional reason string"
}
```

**Tags**:
- `["d", "<intent_id>-reject"]`
- `["t", "deployment.intent.reject"]`
- `["e", "<intent_id>"]`

## Kind 31105 — `rollback.request`

Request a rollback to the previous successful deployment.

**Content** (JSON):
```json
{
  "service_id": "uuid",
  "environment_id": "uuid",
  "requested_by": "npub..."
}
```

**Tags**:
- `["d", "<service_id>-<environment_id>-rollback-<timestamp>"]`
- `["t", "rollback.request"]`
- `["service", "<service_id>"]`
- `["environment", "<environment_id>"]`

## Relationship to Outbound Audit Events

Outbound events (published *by* Bahia) use kinds 31000–31005:

| Kind  | Label                   | Direction |
|-------|-------------------------|-----------|
| 31000 | `build.registered`      | Outbound  |
| 31001 | `artifact.registered`   | Outbound  |
| 31002 | `deployment.created`    | Outbound  |
| 31003 | `deployment.completed`  | Outbound  |
| 31004 | `drift.detected`        | Outbound  |
| 31005 | `runtime.observation`   | Outbound  |

Inbound commands (31100–31105) trigger the same domain logic as REST calls,
which in turn publishes the corresponding outbound audit events (31000–31005).
