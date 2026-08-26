# Souls (AI Agents)

**Souls** in Bahia are AI agents provisioned through Soul Factory with cryptographic identities, personalities, and full infrastructure.

## Overview

Soul Factory provides:
- **LLM-driven generation** — Personalities from templates or custom prompts
- **Nostr-native identity** — NIP-46 bunker-backed keypairs
- **Full infrastructure** — Avatar, memory, workspace, deployment
- **Lifecycle management** — Suspend, resume, revoke, regenerate

## Key Concepts

### Soul

A **Soul** is a fully provisioned AI agent:

```yaml
agent_id: "scout"
name: "Scout"
pubkey: "npub1scout..."
status: "active"
tier: "standard"
personality:
  brief: "A research agent that investigates topics..."
  voice: "curious, thorough, analytical"
```

### Template

A **Template** provides a starting point:

```yaml
name: "research-agent"
brief: "Investigates topics and synthesizes findings"
tier: "standard"
permissions:
  - web_search
  - file_read
```

### Tiers

| Tier | Description | Use Case |
|------|-------------|----------|
| `lightweight` | Minimal resources | Simple tasks, monitoring |
| `standard` | Balanced | Most use cases |
| `heavy` | Maximum resources | Complex, multi-step work |

## Provisioning Souls

### Web UI

1. Navigate to **Souls** in the sidebar
2. Click **New Soul** (or use the Soul Designer)
3. Choose:
   - **Template**: Select a pre-made template, or
   - **Custom Brief**: Write your own personality brief
4. Configure:
   - **Agent ID**: Unique identifier
   - **Name**: Display name
   - **Tier**: Resource allocation
5. Click **Provision**
6. Monitor provisioning progress

### CLI

```bash
# From template
bahia souls provision scout \
  --template "31950:pubkey:research-agent" \
  --name "Scout" \
  --tier standard \
  --follow

# Custom brief
bahia souls provision codebot \
  --name "CodeBot" \
  --brief "A code review specialist focused on security and performance" \
  --tier heavy \
  --follow

# From brief file
bahia souls provision reviewer \
  --name "Code Reviewer" \
  --brief-file ./agent-brief.md \
  --follow
```

### Signer-first control plane

Bahia does not currently expose `soul_factory_*` MCP tools. Provisioning flows use the web UI, the `bahia souls` CLI, or signer-first Nostr control-plane operations.

## Provisioning Steps

Soul Factory executes 8 steps:

1. **Generate** — LLM creates SOUL.md, IDENTITY.md, permissions
2. **Signet** — Registers keypair with NIP-46 bunker
3. **Avatar** — Generates avatar image via FLUX/ComfyUI
4. **Profile** — Publishes Nostr profile (kind:0)
5. **Qdrant** — Creates vector memory collection
6. **Memory** — Seeds initial context
7. **Workspace** — Initializes git repository
8. **Deploy** — Registers with Bahia, publishes soul event

## Viewing Souls

### Web UI

The **Souls** page (Soul Gallery) shows:
- All provisioned souls
- Status indicators
- Quick actions

Click a soul to see:
- **Identity**: Name, pubkey, avatar
- **Personality**: Brief, voice, traits
- **Infrastructure**: Memory, workspace
- **Permissions**: What the agent can do
- **Status**: Active, suspended, revoked

### CLI

```bash
# List souls
bahia souls list
bahia souls list --status active

# Get soul details
bahia souls get scout
bahia souls get scout -o yaml
```

### CLI and web views

Use the Soul Gallery in the web UI or the `bahia souls list` / `bahia souls get` CLI commands to inspect souls. Bahia does not currently expose `soul_factory_list_souls` or `soul_factory_get_soul` MCP tools.

## Lifecycle Actions

### Suspend

Temporarily disable a soul:

```bash
bahia souls suspend scout --reason "Maintenance"
```

Suspend/resume/revoke/redeploy/regenerate operations currently use the web UI, the `bahia souls` CLI, or signer-first Nostr flows rather than `soul_factory_action` MCP tools.

### Resume

Reactivate a suspended soul:

```bash
bahia souls resume scout
```

### Revoke

Permanently disable (cannot be undone):

```bash
bahia souls revoke scout --reason "No longer needed"
```

### Redeploy

Redeploy the soul's infrastructure:

```bash
bahia souls redeploy scout
```

### Regenerate

Regenerate with a new brief:

```bash
bahia souls regenerate scout \
  --brief "Updated purpose and behavior..."
```

The current MCP server does not register a `soul_factory_regenerate` tool. Use the verified CLI command above or the signer-first Soul Factory lifecycle methods supported by the configured runtime.

## Templates

### Listing Templates

```bash
bahia souls templates list
```

### Built-in Templates

| Template | Description |
|----------|-------------|
| `research-agent` | Investigates topics, synthesizes findings |
| `code-reviewer` | Reviews code for quality and security |
| `monitor-agent` | Monitors systems, alerts on issues |
| `coordinator-agent` | Orchestrates other agents |
| `assistant-agent` | General-purpose assistant |
| `builder-agent` | Builds and deploys software |

### Getting Template Details

```bash
bahia souls templates get research-agent
```

## Nostr Event Kinds

| Kind | Name | Description |
|------|------|-------------|
| 31950 | SoulTemplate | Template definitions |
| 31951 | AgentSoul | Provisioned agent |
| 31952 | SoulDraft | Work-in-progress soul |
| 31953 | SoulFleetConfig | Trusted fleet-wide OpenClaw template (`d=soulfactory-fleet-config/v1`) |
| 5950 | ProvisioningRequest | DVM provisioning request |
| 6950 | ProvisioningStatus | Progress updates |
| 7950 | ProvisioningResult | Final result |
| 1950 | SoulAction | Lifecycle actions |
| 1951 | SoulAction legacy result | Backward-compatible lifecycle result alias |
| 30317 | RuntimeCapability | Runtime capability announcement |
| 38384 | RuntimeControlRequest | Runtime-directed control request |
| 38386 | RuntimeControlResult | Runtime-directed result |

## Fleet-wide OpenClaw configuration

Open **Settings → OpenClaw Fleet** at `/settings/fleet` to edit and publish the parameterized-replaceable kind `31953` document. The event content uses `soulfactory-fleet-config/v1` and contains an allowlisted OpenClaw `template` plus optional `defaults` for model, CLI bindings, and reproducible `plugin-id=install-source` requirements. Secret-shaped string fields must use `${VAR}` placeholders.

During provisioning, the reactor selects the newest valid event signed by a pubkey in `soul_factory.authorized_pubkeys`. The OpenClaw wrapper deep-merges fleet template → per-agent runtime/relay settings → wrapper-owned workspace, account binding, Nostr enablement, and file-backed secrets. The three `OPENCLAW_SOULFACTORY_DEFAULT_*` variables remain fallbacks when the fleet document omits their corresponding defaults.

Replacing kind `31953` also reconciles active OpenClaw souls automatically. The reactor skips souls whose kind `31951` read model already records the new fleet revision, applies the new template with bounded per-soul `soulfactory.config.reload` calls, publishes soul-scoped kind `6950` progress and kind `7950` terminal events, and rolls an individual runtime back to its recorded prior fleet revision on failure. Successful kind `31951` events carry a `fleet-revision` tag with the applied kind `31953` event ID.

## Per-agent runtime customization

The new-soul Runtime panel accepts an optional agent LLM model. Leave it blank to inherit the model from the fleet snapshot pinned for that provisioning request, then from the wrapper environment fallback. Entering a value writes `runtime.model` into the signed draft and passes that override to OpenClaw's `agents add --model` flow.

For dedicated OpenClaw agents, provision-time rendering deep-merges the draft's memory settings into `agents.defaults.memorySearch` and its voice mapping into the OpenClaw TTS section. Embedding provider/model, retrieval limits and threshold, auto-index/session behavior, and native reranking intent override overlapping fleet values. Retention days and the selected reranker model remain in Bahia's normalized runtime metadata when OpenClaw has no corresponding native key.

The creation wizard is explicit about controls that do not have an interactive runtime path:

- Browser-local avatar files cannot be attached because Bahia has no durable web Blossom-upload endpoint. Use a durable `blossom:` or HTTPS asset reference; the UI never saves `blob:` URLs.
- Generate Avatar and Play Sample are disabled unless the page has a runtime dispatcher. Their configuration is still saved to the draft and applied during provisioning.
- Reindex is available only on a deployed soul and completes only when runtime progress/result events confirm it.
- Tool grants and approval policy are signed control-plane intent. The owned OpenClaw wrapper does not currently translate them into OpenClaw tools, MCP, or plugin enforcement.

## Nostr-First Provisioning Flow

Soul creation is signer-first and event-driven:

1. The browser/operator signs a `31952` Soul draft containing the desired identity, runtime target, relay policy, permissions, workspace, assets, and `spec_hash`.
2. The browser/operator signs a ContextVM `25910` request using `soul-factory/provision`; params use the existing `soulfactory-provisioning/v1` schema. Bahia preserves the request event id for correlation and adapts the request into the staged SoulFactory reactor. Existing `5950` publishers remain supported as lifecycle interop during contraction.
3. Bahia SoulFactory publishes `6950` progress, sends scoped runtime control kind `38384` events to OpenClaw, validates correlated `38386` runtime results, publishes final `31951`, and then publishes terminal `7950`.
4. Clients subscribe to the correlated Nostr events for durable truth. A ContextVM or MCP acknowledgment is not completion.

REST provisioning and lifecycle routes are intentionally not part of SoulFactory. Do not integrate against a REST create/provision/suspend/resume path; use signed Nostr events and scoped subscriptions.

The Bahia sidecar relay accepts every valid Nostr event kind without a numerical allowlist. Browser routes such as `/souls/new` can query `31950`, `31951`, `31952`, and `30317` through the sidecar, and operators/runtimes can publish correlated SoulFactory request, progress, and result events through the same relay boundary when their signatures and event IDs are valid.

## Provisioning recovery

On restart, the Soul Factory reactor backfills one globally newest provisioning request and one globally newest lifecycle action, not every workflow missed during downtime, then keeps its subscription open for new events. Terminal results are checked idempotently so those replays do not duplicate completed work.

The same startup subscription backfills up to 100 runtime-control results and continues following new results. A late successful result can complete the public projection from a persisted checkpoint without repeating Signet identity creation, avatar generation, memory/workspace setup, or runtime provisioning. Recovery checkpoints do not expose the Signet bunker URI or raw signing material. Operators should not treat the one-request/one-action backfill as exhaustive recovery for multiple concurrent missed workflows.

## Agent Self-Provisioning

Agents can provision other agents through the signer-first Soul Factory flow. Use the same web, CLI, or Nostr provisioning surfaces described above; Bahia does not currently expose a `soul_factory_provision` MCP tool.

## Configuration

The app-level Soul Factory reactor is disabled by default. Enable it only with explicit Nostr relays, a real Signet bunker URI, authorized requester pubkeys, and LLM settings:

```yaml
soul_factory:
  enabled: true
  agent_runtimes:
    - openclaw
  runtime_pubkeys:
    openclaw:
      - "<64-char OpenClaw runtime pubkey>"
  relays:
    - wss://relay.example.com
  additional_relays:
    - wss://private-relay.example.com
  nip29_groups: # optional; controller-signed membership assignments for new souls
    - relay: wss://groups.example.com
      id: fleet-ops
    - relay: wss://groups.example.com
      id: fleet-dev
  communikeys_communities: # optional; controller-owned kind-30000 section ACLs
    - pubkey: "<64-char Signet/controller community pubkey>"
      sections: [General, Apps, Chat]
  concord_communities: # optional; CORD-05 Direct Invites for encrypted backup communities
    - community_id: "<64-char self-certifying Concord community id>"
      invite_bundle_env: "FLEET_CONCORD_INVITE" # or invite_bundle_file / invite_bundle_sealed_file (absolute paths)
  authorized_pubkeys:
    - "<64-char requester pubkey>"
  soul_factory_pubkey: "<64-char Signet/controller pubkey>"
  signet_bunker_uri: "bunker://..."
  startup_timeout: 15s
  llm_base_url: "https://llm.example.com" # API origin; Bahia appends /v1/messages
  llm_model: "soul-model"
  llm_api_key: "${SOUL_FACTORY_LLM_API_KEY}"
  llm_timeout: 120s
  workspace_gitea_url: "https://git.example.com" # optional; enables workspace repo generation
  workspace_private_key_ref: "secret://souls/openclaw/nostr-private-key"
  workspace_agent_memory_mcp_url_ref: "config://souls/agent-memory-mcp-url"
  workspace_gateway_port: 18780
```

When enabled, Bahia starts a Nostr-native Soul Factory reactor and one generic adapter for every `agent_runtimes` entry. `runtime_pubkeys` can pin each target to exact capability/result signing identities; production multi-runtime enablement pins every enabled runtime so an unknown signed `30317` cannot become eligible. Provisioning and lifecycle work remains event-driven through Nostr; Bahia does not add REST provisioning or lifecycle routes for Soul Factory. See the [Metiq runtime enablement runbook](../../runbooks/metiq-runtime-enablement.md) for the protected config, Signet enrollment, live validation, evidence, and rollback procedure.

When `nip29_groups` is configured, provisioning uses the Signet-custodied Soul Factory controller to authenticate with each group relay and publish a NIP-29 `put-user` event after the new identity is minted. Every relay must acknowledge the assignment or provisioning fails closed. The new soul never receives or handles raw signing-key material.

When `communikeys_communities` is configured, the Signet/controller key must own the named community pubkey and its existing kind-`30000` section profile lists must be reachable through the SoulFactory relays. During Signet provisioning, Bahia reads each exact admin-authored list through EOSE, preserves its tags and content, adds the new soul's `p` tag, republishes the replacement with the controller, and requires relay `OK`. Provisioning fails closed on missing lists, invalid ownership, AUTH failure, or rejection. Badges remain engagement-only and never grant Communikeys write access.

When `concord_communities` is configured, each entry references CORD-05 CommunityInvite JSON through either an environment-variable name or an absolute mounted-secret file path; Bahia does not accept inline `community_root` or channel keys. Bahia verifies the bundle's self-certifying `community_id`, current control/channel material, size and relay bounds, then uses the Signet-held staff identity to NIP-44-encrypt a kind-`3313` rumor and sign its kind-`13` seal. A single-use key signs the outer kind-`1059` wrap with `p=<new soul>` and `k=3313`. Every bundle relay must also be configured as a SoulFactory relay; Bahia authenticates and publishes to that declared set, and every relay must return an accepted `OK` or provisioning aborts. Bahia additionally resolves the recipient's giftwrap inbox per CORD-05 §6 — their kind-`10050` DM relay list when one exists, their NIP-65 read relays otherwise — and publishes there too, requiring at least one inbox relay to accept. A freshly provisioned agent has published neither list yet, so its invite rides the community relays alone. The agent's Concord client must watch at least one of those relays. A delivered Direct Invite cannot be revoked; removing accidental access requires CORD-06 key rotation/refounding.

`invite_bundle_sealed_file` keeps the material in Signet-backed custody: the file holds only a NIP-44 payload sealed to the staff key, opened through the bunker at use time, and it is the only source a CORD-06 rotation can write fresh keys back to. `RotateConcordCommunity` performs the rekey (a named Private Channel) or refounding (the `community_root`, plus a fresh `control_root` and Public Channels), then publishes CORD-06 §1 kind-`3303` Rekey Blobs to the rekey address so any survivor holding the prior key converges on the new epoch, and redistributes the new bundle to the survivors reachable by Direct Invite — anyone omitted from `Recipients` is severed. See [Soul Factory](../../soul-factory.md#cord-06-rekeys-and-refoundings).

For OpenClaw command-driver deployments, the packaged local wrapper currently supports `soulfactory.provision`, `soulfactory.update`, `soulfactory.persona.update`, `soulfactory.config.reload`, `soulfactory.memory.reindex`, and `soulfactory.revoke`. Full updates require optimistic spec-hash checks and accept either a canonical replacement spec or a merge patch over the persisted prior resolved spec. The sidecar advertises that conservative method set by default; operators can override it with `-methods` or `OPENCLAW_SOULFACTORY_METHODS` only when the configured command really implements additional runtime-control methods. Non-dry-run provisioning uses `OPENCLAW_SOULFACTORY_RUNTIME_MODE=per-agent-compose` (the default) to reconcile a dedicated, ownership-labelled gateway with an immutable image digest, pinned source commit, resource limits, health check, persistent config/workspace mounts, file-backed secrets, explicit Nostr plugin allowlist, and exact account binding. Shared `existing-container` provisioning is rejected so incumbent gateways are never mutated.

The OpenClaw sidecar stores its trusted controller set in a mounted policy file beside its idempotency state. Deployment configuration may seed the file once, but subsequent controller authorization changes use signed ContextVM `soulfactory.controller.grant` and `soulfactory.controller.revoke` events or a SIGHUP reload of persisted state; no restart or environment edit is required. The sidecar signing key must be supplied through `OPENCLAW_SOULFACTORY_PRIVATE_KEY_FILE`, never as a value-bearing environment variable.

If `workspace_gitea_url` is set, generated OpenClaw workspace config uses the configured SoulFactory/OpenClaw runtime-control relays, Signet/controller pubkey, LLM model, and secret/config references above. Workspace repository publication is a separate NIP-34/ngit operation: ngit publication relays are required independently from OpenClaw runtime-control relays and are not treated as generic control-plane substitutes. Bahia fails configuration or workspace generation explicitly when required values are missing or pubkeys are not 64-character hex strings; it does not write placeholder relays, controllers, inline private keys, fake MCP URLs, or silently substitute OpenClaw control relays for missing ngit publication relays in production workspace files.

## Authorization

Provisioning requires configured requester pubkeys in `soul_factory.authorized_pubkeys`. Use 64-character hex Nostr pubkeys, not npub strings, in server config.

## Best Practices

1. **Start with templates** — Customize from working examples
2. **Be specific in briefs** — Clear purpose leads to better behavior
3. **Use appropriate tiers** — Don't over-provision
4. **Monitor active souls** — Check for issues
5. **Suspend before revoke** — Allow for recovery

## Troubleshooting

### Provisioning Fails at Signet

Check `GET /ready` and find `signet-soulfactory`. Its `state`, `last_error`, `last_attempt`, and `last_success` fields distinguish an unattempted connection, an active outage, and recovery. Bahia remains live and retries automatically; signing-required operations fail explicitly while disconnected. After the bunker or NIP-46 relay recovers, readiness returns from degraded to healthy without a restart and the reactor replays queued provisioning history. Also verify the configured bunker URI/private-key inputs used by your deployment. The current CLI does not register `bahia auth login`.

### Avatar Generation Fails

Interactive generation requires a configured runtime avatar provider; check ComfyUI/Lemmy availability and the runtime's advertised methods. Browser file upload is intentionally disabled until a durable web Blossom upload endpoint is configured, so use a durable `blossom:` or HTTPS avatar reference instead.

### Soul Not Appearing

Souls are published to relays. Check:
- Relay connectivity
- Soul status (may still be provisioning)

### Status Not Syncing

Ensure Bahia integration is configured correctly.

## Related

- [Workers](workers.md) — Soul execution hosts
- [Services](services.md) — Soul deployments
- [Nostr Integration](../nostr-integration.md) — Event model
