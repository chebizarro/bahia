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

```json
{
  "tool": "soul_factory_regenerate",
  "arguments": {
    "agent_id": "scout",
    "new_brief": "Updated purpose and behavior..."
  }
}
```

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
| 5950 | ProvisioningRequest | DVM provisioning request |
| 6950 | ProvisioningStatus | Progress updates |
| 7950 | ProvisioningResult | Final result |
| 1950 | SoulAction | Lifecycle actions |
| 1951 | SoulAction legacy result | Backward-compatible lifecycle result alias |
| 30317 | RuntimeCapability | Runtime capability announcement |
| 38384 | RuntimeControlRequest | Runtime-directed control request |
| 38386 | RuntimeControlResult | Runtime-directed result |

## Nostr-First Provisioning Flow

Soul creation is signer-first and event-driven:

1. The browser/operator signs a `31952` Soul draft containing the desired identity, runtime target, relay policy, permissions, workspace, assets, and `spec_hash`.
2. The browser/operator signs a ContextVM `25910` request using `soul-factory/provision`; params use the existing `soulfactory-provisioning/v1` schema. Bahia preserves the request event id for correlation and adapts the request into the staged SoulFactory reactor. Existing `5950` publishers remain supported as lifecycle interop during contraction.
3. Bahia SoulFactory publishes `6950` progress, sends scoped runtime control kind `38384` events to OpenClaw, validates correlated `38386` runtime results, publishes final `31951`, and then publishes terminal `7950`.
4. Clients subscribe to the correlated Nostr events for durable truth. A ContextVM or MCP acknowledgment is not completion.

REST provisioning and lifecycle routes are intentionally not part of SoulFactory. Do not integrate against a REST create/provision/suspend/resume path; use signed Nostr events and scoped subscriptions.

The Bahia sidecar relay accepts the SoulFactory event family above as open interop data. Browser routes such as `/souls/new` can query `31950`, `31951`, `31952`, and `30317` through the sidecar without CLOSED rejections, and operators/runtimes can publish correlated SoulFactory request, progress, and result events through the same relay boundary when their signatures and event IDs are valid.

## Agent Self-Provisioning

Agents can provision other agents through the signer-first Soul Factory flow. Use the same web, CLI, or Nostr provisioning surfaces described above; Bahia does not currently expose a `soul_factory_provision` MCP tool.

## Configuration

The app-level Soul Factory reactor is disabled by default. Enable it only with explicit Nostr relays, a real Signet bunker URI, authorized requester pubkeys, and LLM settings:

```yaml
soul_factory:
  enabled: true
  relays:
    - wss://relay.example.com
  additional_relays:
    - wss://private-relay.example.com
  nip29_groups: # optional; controller-signed membership assignments for new souls
    - relay: wss://groups.example.com
      id: fleet-ops
    - relay: wss://groups.example.com
      id: fleet-dev
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

When enabled, Bahia starts a Nostr-native Soul Factory reactor and OpenClaw runtime adapter. Provisioning and lifecycle work remains event-driven through Nostr; Bahia does not add REST provisioning or lifecycle routes for Soul Factory.

When `nip29_groups` is configured, provisioning uses the Signet-custodied Soul Factory controller to authenticate with each group relay and publish a NIP-29 `put-user` event after the new identity is minted. Every relay must acknowledge the assignment or provisioning fails closed. The new soul never receives or handles raw signing-key material.

For OpenClaw command-driver deployments, the packaged local wrapper currently supports `soulfactory.provision`, `soulfactory.update`, `soulfactory.persona.update`, and `soulfactory.revoke`. Full updates require optimistic spec-hash checks and accept either a canonical replacement spec or a merge patch over the persisted prior resolved spec. The sidecar advertises that conservative method set by default; operators can override it with `-methods` or `OPENCLAW_SOULFACTORY_METHODS` only when the configured command really implements additional runtime-control methods. The wrapper supports dry-run verification and non-dry-run targeting of an existing containerized OpenClaw runtime through `OPENCLAW_SOULFACTORY_RUNTIME_MODE=existing-container` plus `OPENCLAW_SOULFACTORY_CONTAINER`; it does not expose REST lifecycle control or launch persistent bare-metal OpenClaw runtimes.

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

Check bunker connectivity and the configured bunker URI/private-key inputs used by your deployment. The current CLI does not register `bahia auth login`.

### Avatar Generation Fails

Avatar is optional — a placeholder is used if generation fails. Check ComfyUI/Lemmy availability.

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
