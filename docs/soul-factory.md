# Soul Factory

Soul Factory is a Nostr-native agent provisioning system integrated into bahia. It creates AI agents with cryptographic identities, generated personalities, and full infrastructure (memory, workspace, deployment).

## Overview

Soul Factory provides:
- **LLM-driven soul generation** from templates or custom prompts
- **Nostr-native API** using events for all operations
- **Cryptographic identity** via NIP-46 bunker (Signet)
- **Full infrastructure** setup (avatar, memory, workspace, deployment)
- **Lifecycle management** (suspend, resume, revoke, regenerate)

## Event Kinds

| Kind | Name | Description |
|------|------|-------------|
| 31950 | SoulTemplate | Prepared templates for generating agents |
| 31951 | AgentSoul | Fully provisioned agent identity |
| 31952 | SoulDraft | Work-in-progress soul before provisioning |
| 5950 | ProvisioningRequest | DVM request to provision a soul |
| 6950 | ProvisioningStatus | Progress updates during provisioning |
| 7950 | ProvisioningResult | Final result (success or failure) |
| 1950 | SoulAction | Lifecycle actions (suspend, resume, etc.) |

## CLI Commands

### List Souls

```bash
bahia souls list
bahia souls list --status active
bahia souls list -o json
```

### Get Soul Details

```bash
bahia souls get scout
bahia souls get scout -o yaml
```

### Provision a Soul

```bash
# From a template
bahia souls provision my-agent --template 31950:pubkey:research-agent --follow

# Custom brief
bahia souls provision my-agent --brief "An agent that helps with research" --tier standard

# From brief file
bahia souls provision my-agent --brief-file ./agent-brief.md --follow
```

### Lifecycle Actions

```bash
# Suspend
bahia souls suspend my-agent --reason "Maintenance"

# Resume
bahia souls resume my-agent

# Revoke (permanent)
bahia souls revoke my-agent --reason "No longer needed"

# Redeploy
bahia souls redeploy my-agent

# Regenerate with new brief
bahia souls regenerate my-agent --brief "Updated purpose..."
```

### Templates

```bash
bahia souls templates list
bahia souls templates get research-agent
```

## MCP Tools

Soul Factory exposes MCP tools for agent interactions:

### soul_factory_list_souls

List all provisioned agent souls.

```json
{
  "status": "active",
  "limit": 50
}
```

### soul_factory_get_soul

Get details for a specific agent.

```json
{
  "agent_id": "scout"
}
```

### soul_factory_provision

Provision a new agent soul.

```json
{
  "agent_id": "my-agent",
  "name": "My Agent",
  "brief": "An agent that helps with...",
  "tier": "standard",
  "template": "31950:pubkey:research-agent"
}
```

### soul_factory_action

Execute lifecycle action.

```json
{
  "agent_id": "my-agent",
  "action": "suspend",
  "reason": "Maintenance"
}
```

### soul_factory_regenerate

Regenerate soul with new brief.

```json
{
  "agent_id": "my-agent",
  "new_brief": "Updated purpose and behavior..."
}
```

## Provisioning Workflow

When a soul is provisioned, Soul Factory executes 8 steps:

1. **Generate** - LLM generates soul content (SOUL.md, IDENTITY.md, permissions)
2. **Signet** - Registers keypair with NIP-46 bunker
3. **Avatar** - Generates avatar image via FLUX
4. **Profile** - Publishes Nostr profile (kind:0)
5. **Qdrant** - Creates vector memory collection
6. **Memory** - Seeds initial context in agent-memory
7. **Workspace** - Initializes git repo with soul files
8. **Deploy** - Registers with bahia, publishes soul event

## Resource Tiers

| Tier | Description | Use Case |
|------|-------------|----------|
| lightweight | Fast, minimal resources | Simple tasks, monitoring |
| standard | Balanced capabilities | Most use cases |
| heavy | Maximum resources | Complex workloads, multi-step |

## Templates

Templates provide pre-configured starting points:

- **research-agent** - Investigates topics and synthesizes findings
- **code-reviewer** - Reviews code for quality and security
- **monitor-agent** - Monitors systems and alerts on issues
- **coordinator-agent** - Orchestrates other agents
- **assistant-agent** - General-purpose assistant
- **builder-agent** - Builds and deploys software

## Web UI

Access the Soul Gallery at `/souls`:

- **Gallery View** - Browse all souls with filtering
- **Soul Designer** - Create new souls with wizard flow
- **Soul Detail** - View identity, permissions, infrastructure
- **Lifecycle Actions** - Suspend, resume, redeploy buttons

## Integration with bahia

Provisioned souls are automatically:
- Registered as bahia Services
- Given initial DeploymentIntents
- Status synced between bahia and Nostr events
- Lifecycle actions flow through to bahia deployments

## Authorization

Provisioning requires authorization. Add pubkeys to the authorized list:

```go
AuthorizedProvisioners = []string{
    "cdee943cbb19c51ab847a66d5d774373aa9f63d287246bb59b0827fa5e637400",
    // Add more authorized pubkeys
}
```

## Configuration

Soul Factory is configured through the bahia config:

```yaml
soul_factory:
  relays:
    - wss://relay.sharegap.net
    - wss://armada.sharegap.net
  private_relays:
    - wss://private.relay.example.com
  signet_bunker_uri: bunker://...
  blossom_url: https://blossom.example.com
  qdrant_url: http://localhost:6333
  agent_memory_url: http://localhost:3000
  lemmy_url: http://localhost:8188
```

## Examples

### Provision a Research Agent

```bash
bahia souls provision scout \
  --name "Scout" \
  --template 31950:pubkey:research-agent \
  --tier standard \
  --follow
```

### Provision Custom Agent

```bash
cat > agent-brief.md << 'EOF'
You are a code review specialist focused on:
- Security vulnerabilities
- Performance issues
- Code style consistency

You respond thoughtfully and provide specific, actionable feedback.
EOF

bahia souls provision code-reviewer \
  --name "CodeBot" \
  --brief-file agent-brief.md \
  --tier heavy \
  --follow
```

### Agent Self-Provisioning

Agents can provision other agents using the MCP tools:

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

## Troubleshooting

### Provisioning Fails at Signet Step

Check Signet bunker connectivity:
```bash
bahia auth login --nip46 "bunker://..."
```

### Avatar Generation Fails

Avatar generation is optional. If it fails, a placeholder is used. Check Lemmy/ComfyUI availability.

### Soul Not Appearing

Souls are published to relays. Check relay connectivity:
```bash
# Verify relays
curl -X GET wss://relay.sharegap.net
```

### Status Not Syncing

Ensure bahia integration is configured with the agent environment ID.
