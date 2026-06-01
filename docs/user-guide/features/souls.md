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

### MCP Tool

```json
{
  "tool": "soul_factory_provision",
  "arguments": {
    "agent_id": "scout",
    "name": "Scout",
    "template": "31950:pubkey:research-agent",
    "tier": "standard"
  }
}
```

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

### MCP Tool

```json
{
  "tool": "soul_factory_list_souls",
  "arguments": {
    "status": "active"
  }
}
```

```json
{
  "tool": "soul_factory_get_soul",
  "arguments": {
    "agent_id": "scout"
  }
}
```

## Lifecycle Actions

### Suspend

Temporarily disable a soul:

```bash
bahia souls suspend scout --reason "Maintenance"
```

```json
{
  "tool": "soul_factory_action",
  "arguments": {
    "agent_id": "scout",
    "action": "suspend",
    "reason": "Maintenance"
  }
}
```

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

## Agent Self-Provisioning

Agents can provision other agents:

```json
{
  "tool": "soul_factory_provision",
  "arguments": {
    "agent_id": "sub-agent",
    "name": "Sub Agent",
    "brief": "A specialized agent for specific tasks",
    "tier": "lightweight"
  }
}
```

## Configuration

Soul Factory requires configuration:

```yaml
soul_factory:
  relays:
    - wss://relay.example.com
  signet_bunker_uri: "bunker://..."
  blossom_url: "https://blossom.example.com"
  qdrant_url: "http://localhost:6333"
  agent_memory_url: "http://localhost:3000"
  lemmy_url: "http://localhost:8188"  # ComfyUI for avatars
```

## Authorization

Provisioning requires authorization:

```go
AuthorizedProvisioners = []string{
    "npub1admin...",
}
```

## Best Practices

1. **Start with templates** — Customize from working examples
2. **Be specific in briefs** — Clear purpose leads to better behavior
3. **Use appropriate tiers** — Don't over-provision
4. **Monitor active souls** — Check for issues
5. **Suspend before revoke** — Allow for recovery

## Troubleshooting

### Provisioning Fails at Signet

Check bunker connectivity:
```bash
bahia auth login --nip46 "bunker://..."
```

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
