# Bahia Protocol Compatibility Matrix

This document summarizes the protocols and event families Bahia currently uses and how they fit the **current** product shape.

## Important scope note

Bahia's product contract is no longer well-described by a REST-only or registry-only model.

Current reality:
- Bahia is a **deployment/runtime control plane**
- the **public control plane is Nostr-native**
- the **relay sidecar is the primary realtime/public boundary**
- browser/operator identity is **signer-first**
- sensitive browser domains may use **encrypted Nostr request/result** flows
- REST and MCP remain important, but they are narrowed compatibility/query/tooling surfaces

For the canonical control-plane contract, prefer:
- `docs/control-planes.md`
- `docs/nostr-commands.md`
- `docs/event-spec.md`

---

## Quick reference

| Protocol / surface | Purpose | Current status |
|---|---|---|
| Nostr public control plane | canonical requests, status/results, read models, activity | ✅ primary |
| Relay sidecar | primary browser/backend public relay boundary | ✅ primary |
| Encrypted Nostr request/result | sensitive browser-facing operations | ✅ implemented |
| NIP-98 | Bahia HTTP authentication and OCI push auth | ✅ implemented |
| NIP-05 | identity enrichment / verification | ✅ implemented |
| NIP-46 | signer / bunker support | ✅ implemented in Signet + browser signer flows; some CLI-specific auth UX remains incomplete |
| Loom | distributed job execution | ✅ implemented |
| Hive-CI | workflow event ingestion | ✅ implemented |
| OCI Distribution API | registry push/pull | ✅ implemented |
| Blossom | blob/log storage backend | ✅ implemented |
| Cashu | worker payment surface | ✅ implemented |
| REST API | narrowed CRUD/query/log compatibility surface | ✅ implemented |
| MCP JSON-RPC | tooling surface with async correlation metadata | ✅ implemented |

### Hive CI Protocol Events

Bahia's CI/deployment bridge is aligned to the **Hive CI protocol** and its Loom execution hand-off:

| Kind | Name | Direction | Description |
|------|------|-----------|-------------|
| 5401 | Workflow Run | Inbound | Trusted CI trigger / workflow-run fact |
| 5402 | Workflow Result | Inbound | Trusted build outcome fact |
| 5100 | Loom Job Request | Outbound | Actual compute dispatch for build/deploy work |

> **Boundary note:** `kind:5900` is **not** part of Bahia's Hive CI integration. `5900` belongs to the older upstream NIP-90 `dvm-cicd-runner` path. Bahia's subscriber and pipeline bridge are designed around `5401` / `5402`, with `5100` as the execution transport.

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
>>>>>>> 99d1c51 (docs: clarify Hive CI protocol boundaries)

---

## Control-plane transport hierarchy

### 1. Public Nostr control plane (primary)
This is the canonical public event contract for Bahia.

Main families:
- **service requests**: `5961-5968`
- **LLM requests**: `5971-5975`
- **tool/adoption workflow**: `5976`, `5977`, `6976`, `7976`, `7977`, `5978-5979`
- **service/environment/policy/artifact public writes**: `5981-5989`
- **service status**: `6961-6963`
- **LLM status**: `6973`
- **adoption status**: `6978`
- **service results**: `7961-7966`
- **LLM results**: `7971-7973`
- **adoption results**: `7978-7979`
- **replaceable read models**: `31961-31970`
- **audit/activity events**: `31000-31099`

### 2. Encrypted Nostr request/result plane
Sensitive browser-facing domains use:
- request kind `5980`
- result kind `7980`

These flows are intentionally separate from the public relay sidecar and use encrypted-request relay URLs where configured.

### 3. MCP and REST
- **MCP** (`/mcp`, `/api/v1/mcp`) is a first-class tooling surface.
- **REST** remains for narrowed CRUD/query/log/registry compatibility.
- For async Nostr-native workflows, HTTP should not be treated as the sole source of completion truth.

---

## Canonical Nostr event families

### Public request kinds

| Range / Kind | Purpose |
|---|---|
| `5961` | deploy request |
| `5962` | rollback request |
| `5963` | service action / direct-runtime action |
| `5964` | service create |
| `5965` | environment create |
| `5966` | deployment approval |
| `5967` | observation submit |
| `5968` | drift remediation |
| `5971-5975` | LLM route/release/deploy/approval/rollback |
| `5976` | tool provisioning request (agent → Bahia) |
| `5977` | tool approval handoff request (Bahia → operator) |
| `5978-5979` | adoption scan / adoption import |
| `5981-5989` | service/environment update/delete, artifact register, policy create/update/delete/evaluate |

### Status kinds

| Range / Kind | Purpose |
|---|---|
| `6961-6963` | deployment/service/action progress |
| `6973` | LLM deployment progress |
| `6976` | tool provisioning progress |
| `6978` | adoption progress |

### Result kinds

| Range / Kind | Purpose |
|---|---|
| `7961-7966` | deployment/action/create/observation/remediation terminal results |
| `7971-7973` | LLM terminal results |
| `7976` | tool provisioning terminal result (Bahia → agent) |
| `7977` | tool approval response (operator → Bahia) |
| `7978-7979` | adoption terminal results |
| `7980` | encrypted terminal result |

### Replaceable read models

| Range / Kind | Purpose |
|---|---|
| `31961-31963` | service state + service/environment registries |
| `31964-31965` | LLM route registry + route state |
| `31966-31970` | artifact, deployment intent/run, build, and policy registries |

### Audit / activity events

| Range / Kind | Purpose |
|---|---|
| `31000-31019` | build/artifact/deployment/drift/runtime/LLM lifecycle audit activity |

---

## Legacy 311xx bridge

The `31100-31105` command bridge exists only as **deprecated compatibility behavior**.

- It is **not** the supported control-plane contract.
- New integrations must publish the canonical 596x/597x/598x request kinds instead.
- Current docs should not treat 311xx as the normal way to integrate with Bahia.

If you need command details, use `docs/nostr-commands.md` rather than assuming the old 311xx bridge is authoritative.

---

## NIP-98 HTTP authentication

Bahia supports NIP-98 direct HTTP auth for protected HTTP surfaces.

### Current usage
- protected REST routes
- protected MCP routes
- OCI push auth

### Behavior
- `Authorization: Nostr <base64event>` is the supported auth header when auth is enabled
- `Authorization: Bearer ...` should be rejected by protected Bahia Nostr event contracts
- browser-compatible direct NIP-98 capability is advertised via `Nostr discovery events (kind 31974 + NIP-51 kind 30002)`

---

## NIP-46 Nostr Connect

NIP-46 support is **implemented**, but some surfaces are more complete than others.

### Implemented
- Signet client bunker support
- browser signer/session flows that use NIP-46 where available
- signing support for control-plane actions

### Still incomplete / narrower
- some CLI-specific auth UX remains separate from Signet support
- encrypted browser flows additionally require exposed NIP-44 encrypt/decrypt capability from the provider

This means “NIP-46 is stubbed” is no longer an accurate summary of the current repo state.

---

## Loom protocol

Bahia integrates with Loom as a deployment/job execution path.

| Kind | Name | Direction | Purpose |
|------|------|-----------|---------|
| `10100` | Worker Advertisement | inbound | discover worker capabilities |
| `5100` | Job Request | outbound | submit deployment/build job |
| `30100` | Job Status Update | inbound | observe progress |
| `5101` | Job Result | inbound | receive final result |
| `5102` | Job Cancellation | outbound | cancel job |

This remains an important execution protocol, but it now sits inside a broader Nostr-native control-plane story rather than defining the whole product architecture.

---

## Hive-CI protocol

Bahia subscribes to Hive-CI workflow events and converts them into build/artifact/deployment state.

| Kind | Name | Direction | Purpose |
|------|------|-----------|---------|
| `5401` | Workflow Run | inbound | workflow started |
| `5402` | Workflow Result | inbound | workflow completed |

Current processing expectations:
1. validate trusted dispatcher / publisher relationship
2. create build state
3. verify image in registry
4. create artifact state
5. optionally create deployment intent

---

## OCI Distribution API

Bahia implements the OCI Distribution API at `/v2/*`.

Main capabilities:
- push/pull manifests
- push/pull blobs
- tag listing
- referrer listing

Storage model:
- manifests/tags in PostgreSQL
- blobs/logs via Blossom-backed storage

Authentication model:
- NIP-98 for push operations
- service-account basic auth where configured
- anonymous pull from allowed CIDRs where configured

---

## Blossom

Bahia uses Blossom as a blob/log storage backend.

Typical uses:
- OCI blob storage backend
- workflow logs
- deployment stdout/stderr references
- other content-addressed binary payloads where configured

---

## Cashu

Bahia includes a Cashu-backed payment surface for worker/job cost accounting and payment workflows.

Current role:
- worker payment coordination
- payment history / cost reporting surfaces
- integration with deployment/job execution economics

---

## Known gaps / caution areas

These are the main gaps that still matter for readers of this doc:

| Gap | Notes |
|-----|------|
| Soul lifecycle completeness | some lifecycle paths still need careful verification before being described as fully complete everywhere |
| Worker stats / reputation depth | supporting metrics/scoring remain less mature than core deployment flow |
| CLI-specific NIP-46 auth UX | narrower/incomplete compared with Signet + browser signer support |
| Documentation drift | older REST-first / JWT-first descriptions still exist in some files and must not be treated as authoritative |

---

## Recommended reading order

1. `docs/control-planes.md`
2. `docs/nostr-commands.md`
3. `docs/event-spec.md`
4. `docs/api.md`
5. `docs/relay-sidecar.md`
