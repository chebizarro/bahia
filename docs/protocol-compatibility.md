# Bahia Protocol Compatibility Matrix

This document describes which protocols and event kinds Bahia implements,
whether as a publisher (outbound), subscriber (inbound), or both.

## Quick Reference

| Protocol    | Purpose                          | Status      |
|-------------|----------------------------------|-------------|
| Loom        | Distributed job execution        | ✅ Complete |
| NIP-98      | HTTP authentication              | ✅ Complete (API), ⚠️ Stubbed (Blossom) |
| NIP-05      | Identity verification            | ✅ Complete |
| NIP-46      | Nostr Connect (remote signing)   | ⚠️ Stubbed  |
| Blossom     | Media/blob storage               | ✅ Complete (upload/download) |
| Cashu       | Ecash payments                   | ✅ Complete |

---

## Nostr Event Kinds

### Loom Protocol Events

Bahia integrates with [Loom](https://github.com/openagentsinc/loom), a Nostr-native
distributed compute protocol. Bahia acts as a **job requester** (client) that
submits deployment jobs to Loom workers.

| Kind  | Name                | Direction | Description                           |
|-------|---------------------|-----------|---------------------------------------|
| 10100 | Worker Advertisement | Inbound   | Replaceable event advertising worker capabilities |
| 5100  | Job Request         | Outbound  | Request to execute a deployment job   |
| 30100 | Job Status Update   | Inbound   | Parameterized replaceable progress updates |
| 5101  | Job Result          | Inbound   | Final result of a completed job       |
| 5102  | Job Cancellation    | Outbound  | Request to cancel a running job       |

#### Kind 10100 — Worker Advertisement (Inbound)

Workers publish replaceable events advertising their capabilities. Bahia's
\`WorkerDiscovery\` service subscribes to these events and maintains a catalog.

\`\`\`json
{
  "kind": 10100,
  "pubkey": "<worker-pubkey>",
  "content": "{\"name\":\"deploy-worker-1\",\"capabilities\":[\"docker\",\"compose\"],\"price_per_sec\":10,\"mint_url\":\"https://mint.example.com\"}",
  "tags": [
    ["d", "worker-ad"],
    ["relay", "wss://relay.example.com"]
  ]
}
\`\`\`

**Content fields:**
- \`name\`: Human-readable worker name
- \`capabilities\`: Array of supported deployment types
- \`price_per_sec\`: Price in satoshis per second of compute
- \`mint_url\`: Cashu mint URL for payments

#### Kind 5100 — Job Request (Outbound)

Bahia publishes job requests when a deployment run is created.

\`\`\`json
{
  "kind": 5100,
  "pubkey": "<bahia-pubkey>",
  "content": "{\"type\":\"docker-deploy\",\"image\":\"ghcr.io/org/app:v1.2.3\",\"env\":{\"PORT\":\"8080\"}}",
  "tags": [
    ["p", "<target-worker-pubkey>"],
    ["bid", "1000"],
    ["expiration", "1704067200"]
  ]
}
\`\`\`

**Tags:**
- \`p\`: Target worker pubkey
- \`bid\`: Maximum payment in satoshis
- \`expiration\`: Unix timestamp after which job should not be accepted

#### Kind 30100 — Job Status Update (Inbound)

Workers publish parameterized replaceable events with progress updates.

\`\`\`json
{
  "kind": 30100,
  "pubkey": "<worker-pubkey>",
  "content": "",
  "tags": [
    ["d", "<job-event-id>"],
    ["e", "<job-event-id>"],
    ["status", "running"],
    ["progress", "50"]
  ]
}
\`\`\`

**Status values:** \`accepted\`, \`running\`, \`completed\`, \`failed\`, \`cancelled\`

#### Kind 5101 — Job Result (Inbound)

Workers publish the final result when a job completes.

\`\`\`json
{
  "kind": 5101,
  "pubkey": "<worker-pubkey>",
  "content": "{\"exit_code\":0,\"stdout_ref\":\"https://blossom.example.com/abc123\",\"stderr_ref\":\"https://blossom.example.com/def456\"}",
  "tags": [
    ["e", "<job-event-id>"],
    ["status", "completed"],
    ["amount", "850"]
  ]
}
\`\`\`

**Content fields:**
- \`exit_code\`: Process exit code
- \`stdout_ref\`: Blossom URL for stdout logs
- \`stderr_ref\`: Blossom URL for stderr logs

#### Kind 5102 — Job Cancellation (Outbound)

Bahia can request job cancellation.

\`\`\`json
{
  "kind": 5102,
  "pubkey": "<bahia-pubkey>",
  "content": "",
  "tags": [
    ["e", "<job-event-id>"]
  ]
}
\`\`\`

---

### NIP-98 HTTP Authentication

Bahia supports [NIP-98](https://github.com/nostr-protocol/nips/blob/master/98.md)
HTTP Auth for authenticating API requests using Nostr keys.

| Kind  | Name      | Direction | Description                    |
|-------|-----------|-----------|--------------------------------|
| 27235 | HTTP Auth | Inbound   | Authentication event in header |

\`\`\`json
{
  "kind": 27235,
  "pubkey": "<user-pubkey>",
  "content": "",
  "tags": [
    ["u", "https://bahia.example.com/api/v1/services"],
    ["method", "GET"]
  ],
  "created_at": 1704067200,
  "sig": "<signature>"
}
\`\`\`

The event is base64-encoded and sent in the \`Authorization\` header:
\`\`\`
Authorization: Nostr <base64-encoded-event>
\`\`\`

**Validation:**
- Kind must be 27235
- URL tag must match request URL
- Method tag must match HTTP method
- Event must be recent (default: 60 second max skew)
- Signature must be valid
- Event ID must not be reused (replay protection)

---

### Bahia Command Events (Inbound)

Bahia can be operated entirely via Nostr events. See [nostr-commands.md](nostr-commands.md)
for full details.

| Kind  | Label                       | Description                          |
|-------|-----------------------------|--------------------------------------|
| 31100 | \`build.register\`            | Register a new build                 |
| 31101 | \`artifact.register\`         | Register a container artifact        |
| 31102 | \`deployment.intent.create\`  | Create deployment intent             |
| 31103 | \`deployment.intent.approve\` | Approve pending deployment           |
| 31104 | \`deployment.intent.reject\`  | Reject pending deployment            |
| 31105 | \`rollback.request\`          | Request rollback                     |

These are **parameterized replaceable** events (NIP-33) with:
- \`d\` tag for idempotency key
- \`t\` tag for human-readable label
- JSON content matching REST API DTOs

---

### Bahia Audit Events (Outbound)

Bahia publishes audit events for significant domain actions. These provide
a verifiable audit trail and enable event-driven integrations.

| Kind  | Label                   | Trigger                           |
|-------|-------------------------|-----------------------------------|
| 31000 | \`build.registered\`      | Build created via API/event       |
| 31001 | \`artifact.registered\`   | Artifact created                  |
| 31002 | \`deployment.created\`    | Deployment intent approved        |
| 31003 | \`deployment.completed\`  | Deployment run finished           |
| 31004 | \`drift.detected\`        | Observed state differs from desired |
| 31005 | \`runtime.observation\`   | Container health check result     |

Example audit event:
\`\`\`json
{
  "kind": 31002,
  "pubkey": "<bahia-pubkey>",
  "content": "{\"intent_id\":\"uuid\",\"service\":\"my-app\",\"environment\":\"production\",\"artifact\":\"sha256:abc...\"}",
  "tags": [
    ["d", "<intent-id>"],
    ["t", "deployment.created"],
    ["service", "<service-id>"],
    ["environment", "<environment-id>"]
  ]
}
\`\`\`

---

## Blossom Protocol

Bahia uses [Blossom](https://github.com/hzrd149/blossom) (BUD-01) for storing
deployment artifacts and logs. Blossom is a simple HTTP-based blob storage
protocol with content-addressable (SHA-256) URLs.

### Operations

| Method | Endpoint      | Direction | Description              |
|--------|---------------|-----------|--------------------------|
| PUT    | \`/upload\`     | Outbound  | Upload blob, get URL     |
| GET    | \`/<sha256>\`   | Both      | Download blob by hash    |
| HEAD   | \`/<sha256>\`   | Outbound  | Check blob existence     |

### URL Format

Blossom URLs are content-addressed:
\`\`\`
https://blossom.example.com/abc123def456...
\`\`\`

Where the path is the SHA-256 hash of the content. This enables:
- Integrity verification on download
- Multi-server redundancy (same hash = same content)
- Deduplication

### Usage in Bahia

| Data Type         | Storage                                |
|-------------------|----------------------------------------|
| Deployment logs   | stdout/stderr uploaded after job completion |
| SBOM files        | Software Bill of Materials (future)    |
| Build artifacts   | Container layers (future)              |

---

## Cashu Payments

Bahia uses [Cashu](https://cashu.space/) ecash for paying Loom workers.
Cashu provides privacy-preserving Bitcoin payments without requiring
on-chain transactions.

### Flow

1. **Job Request**: Bahia includes a \`bid\` tag with max payment
2. **Job Completion**: Worker returns \`amount\` tag with actual cost
3. **Payment**: Bahia creates Cashu token and sends to worker
4. **Redemption**: Worker redeems token at mint

### Implementation

| Component          | Description                              |
|--------------------|------------------------------------------|
| \`CashuWallet\`      | Manages mint connections and tokens      |
| \`PaymentService\`   | Orchestrates payments for deployment runs |
| \`PaymentRecord\`    | Audit trail of all payments              |

### Mint Configuration

\`\`\`yaml
cashu:
  default_mint: "https://mint.example.com"
  mints:
    - url: "https://mint.example.com"
      unit: "sat"
\`\`\`

---

## NIP-05 Identity

Bahia resolves [NIP-05](https://github.com/nostr-protocol/nips/blob/master/05.md)
identifiers to enrich user principals with human-readable identities.

| Operation      | Direction | Description                    |
|----------------|-----------|--------------------------------|
| Lookup         | Outbound  | Resolve pubkey → identifier    |
| Verification   | Outbound  | Verify identifier → pubkey     |

When a user authenticates via NIP-98, Bahia attempts to resolve their
pubkey to a NIP-05 identifier (e.g., \`alice@example.com\`) and includes
it in the Principal for display purposes.

### Caching

- Successful lookups: cached for 1 hour
- Failed lookups: negative cached for 5 minutes
- Background cleanup removes expired entries

---

## NIP-46 Nostr Connect

[NIP-46](https://github.com/nostr-protocol/nips/blob/master/46.md) enables
remote signing, allowing users to authenticate without exposing their
private keys to Bahia.

| Status | Description                              |
|--------|------------------------------------------|
| ✅     | Signet client implements full NIP-46 bunker protocol |
| ⚠️     | CLI `--nip46` flag for user auth not yet implemented |

**Signet client** (`internal/adapters/signet/client.go`):
- `Connect()` — Establishes NIP-46 session with bunker via go-nostr
- `Sign()` — Signs events through bunker's `sign_event` RPC
- `SignAs()` — Signs as provisioned agent via agent-specific bunker connection
- `ProvisionAgent()` — Calls Signet's custom `provision_agent` RPC
- `RevokeAgent()` — Calls Signet's custom `revoke_agent` RPC
- `GetPublicKey()` — Retrieves bunker's public key
- **Mock mode**: Falls back to local key generation when no bunker URI configured

**CLI auth**: The `bahia auth login --nip46` flag for user authentication is
separate from the Signet client and not yet implemented.

Connection string format:
```
bunker://<remote-signer-pubkey>?relay=wss://relay.example.com&secret=<secret>
```

---

## OCI Distribution Specification

Bahia implements the [OCI Distribution Spec v2](https://github.com/opencontainers/distribution-spec) as an internal container registry.

| Endpoint | Direction | Status | Description |
|----------|-----------|--------|-------------|
| `GET /v2/` | Inbound | ✅ | API version check |
| `GET /v2/{name}/manifests/{ref}` | Inbound | ✅ | Pull manifest |
| `HEAD /v2/{name}/manifests/{ref}` | Inbound | ✅ | Check manifest |
| `PUT /v2/{name}/manifests/{ref}` | Inbound | ✅ | Push manifest |
| `GET /v2/{name}/blobs/{digest}` | Inbound | ✅ | Pull blob |
| `HEAD /v2/{name}/blobs/{digest}` | Inbound | ✅ | Check blob |
| `POST /v2/{name}/blobs/uploads/` | Inbound | ✅ | Start upload |
| `PATCH /v2/{name}/blobs/uploads/{uuid}` | Inbound | ✅ | Chunk upload |
| `PUT /v2/{name}/blobs/uploads/{uuid}` | Inbound | ✅ | Complete upload |
| `GET /v2/{name}/tags/list` | Inbound | ✅ | List tags |
| `GET /v2/{name}/referrers/{digest}` | Inbound | ✅ | List referrers |

**Storage Backend:**
- Manifests: PostgreSQL (BYTEA for digest stability)
- Blobs: Blossom (content-addressed)
- Tags: PostgreSQL

**Authentication:**
- NIP-98 for push operations
- Basic auth service accounts
- Anonymous pull from configured CIDRs

---

## Hive-CI Protocol

Bahia subscribes to [Hive-CI](../hive-ci-protocol/SPECIFICATION.md) workflow events to auto-ingest CI results.

| Kind | Name | Direction | Status | Description |
|------|------|-----------|--------|-------------|
| 5401 | Workflow Run | Inbound | ✅ | CI workflow started |
| 5402 | Workflow Result | Inbound | ✅ | CI workflow completed |

### Kind 5401 — Workflow Run (Inbound)

Bahia subscribes to trusted CI dispatcher pubkeys and records workflow runs.

**Required tags parsed:**
- `a` — NIP-34 repository coordinate
- `commit` — Git commit hash
- `branch` — Git branch name
- `workflow` — Workflow file path
- `triggered-by` — User who triggered
- `publisher` — Ephemeral pubkey for result

### Kind 5402 — Workflow Result (Inbound)

Bahia validates ephemeral publisher key matches 5401, then processes:

**Required tags parsed:**
- `e` — Reference to 5401 event
- `status` — `success` or `failure`
- `exit_code` — Process exit code
- `duration` — Execution duration
- `log_url` — Blossom URL for logs
- `image_repo` — Container image repository (Bahia-specific)
- `image_tag` — Container image tag (Bahia-specific)
- `image_digest` — Container image digest (Bahia-specific)

**Processing flow:**
1. Validate publisher key relationship
2. Create Build record
3. Verify image in OCI registry
4. Create Artifact record
5. (Optional) Create staging DeploymentIntent

---

## Known Gaps

These features have interfaces defined but implementations are incomplete:

| Gap | File | Description |
|-----|------|-------------|
| Soul Lifecycle | `internal/soulfactory/provisioner.go` | Suspend/Resume/Revoke/Regenerate |
| Worker Stats | `internal/service/worker_catalog.go` | JobsCompleted, AvgDuration tracking |
| Reputation Scoring | `internal/service/worker_policy.go` | Worker reputation calculation |
| CLI NIP-46 Auth | `cmd/cli/main.go` | User auth via bunker (separate from Signet) |

---

## Implementation Status Summary

| Protocol/Kind | Direction | Status | File                           |
|---------------|-----------|--------|--------------------------------|
| Kind 10100    | Inbound   | ✅     | \`internal/adapters/loom/discovery.go\` |
| Kind 5100     | Outbound  | ✅     | \`internal/adapters/loom/client.go\` |
| Kind 30100    | Inbound   | ✅     | \`internal/adapters/loom/client.go\` |
| Kind 5101     | Inbound   | ✅     | \`internal/adapters/loom/client.go\` |
| Kind 5102     | Outbound  | ✅     | \`internal/adapters/loom/client.go\` |
| Kind 27235    | Inbound   | ✅     | \`internal/auth/nip98.go\` |
| Kind 31000-31005 | Outbound | ✅  | \`internal/events/publisher.go\` |
| Kind 31100-31105 | Inbound | ✅   | \`internal/adapters/nostr/processor.go\` |
| Blossom       | Both      | ✅     | \`internal/adapters/blossom/\` |
| Cashu         | Outbound  | ✅     | \`internal/adapters/cashu/\` |
| NIP-05        | Outbound  | ✅     | \`internal/auth/nip05.go\` |
| NIP-46        | Both      | ✅     | `internal/adapters/signet/client.go` |
| OCI Dist v2   | Inbound   | ✅     | `internal/api/handlers/registry.go` |
| Kind 5401     | Inbound   | ✅     | `internal/adapters/hiveci/subscriber.go` |
| Kind 5402     | Inbound   | ✅     | `internal/adapters/hiveci/subscriber.go` |

---

## Related Documentation

- [nostr-commands.md](nostr-commands.md) — Detailed Bahia command event specifications
- [event-spec.md](event-spec.md) — Internal event system documentation
- [api.md](api.md) — REST API reference
