# DNS Agent Rollout Runbook — Nostr-First Migration

This document covers the capability-negotiated upgrade path for the DNS
subsystem's migration from ContextVM RPC to Nostr event subscriptions
(Phase 3, D1).

## Architecture Overview

The DNS subsystem has two communication paths between daemon and agent:

| Path | Direction | Mechanism |
|------|-----------|-----------|
| **Zone sync (RPC)** | daemon → agent | ContextVM `SyncZone` RPC push |
| **Zone sync (events)** | daemon → relay → agent | `EventPublishDNSBackend` publishes kind 30900 zone-sync events; agent's `ZoneSubscriber` receives them |
| **Health (RPC)** | daemon → agent | ContextVM `Health()` RPC call |
| **Health (events)** | agent → relay → daemon | Agent publishes NIP-38 kind 30315 status events; daemon's `AgentHealthReader` subscribes |

## Capability Negotiation Protocol

1. **Agent advertises capabilities** — The agent's `HealthPublisher` includes
   a capability tag in its NIP-38 health event:
   ```
   ["capability", "zone-subscribe", "1"]
   ```
   This tells the daemon: "I can receive zone state via Nostr subscriptions;
   you don't need to push zones over RPC."

2. **Daemon reads capabilities** — The daemon subscribes to agent health
   events (kind 30315, d-tag `dns-agent`) via `AgentHealthReader`. It caches
   each agent's capabilities and health status.

3. **Daemon routes per-agent** — `CapabilityAwareDNSBackend` wraps both the
   RPC backend (`DnsmasqAgentBackend`) and the event backend
   (`EventPublishDNSBackend`). For each operation:
   - If the agent advertises `zone-subscribe` capability → use event backend
   - Otherwise → fall back to RPC backend

4. **No double-apply** — Both the RPC `SyncHandler` and the event
   `ZoneSubscriber.ApplyZoneSync` share the agent's `state.ZoneSerials` map
   under the same mutex. A zone serial is applied at most once regardless of
   which path delivers it first.

## Compatibility Matrix

| Daemon | Agent | Zone Sync | Health Check | Notes |
|--------|-------|-----------|--------------|-------|
| **New** | **New** | Events (Nostr) | Events (NIP-38) | Target state. RPC unused. |
| **New** | **Old** | RPC (ContextVM) | RPC (ContextVM) | Old agent has no `zone-subscribe` capability → daemon falls back to RPC for both sync and health. |
| **Old** | **New** | RPC (ContextVM) | RPC (ContextVM) | Old daemon doesn't know about events. Agent keeps its ContextVM `SyncHandler` and `Health` handlers, so RPC still works. Agent also publishes NIP-38 health and subscribes to zone-sync events, but nobody reads/writes them. |
| **Old** | **Old** | RPC (ContextVM) | RPC (ContextVM) | Status quo, no change. |

All four cells work correctly. No coordination is required between daemon
and agent upgrade order.

## Recommended Upgrade Order

1. **Deploy new agents first.** New agents are fully backward-compatible:
   they keep their ContextVM RPC handlers alongside the new Nostr
   subscription path. An old daemon continues pushing zones and checking
   health via RPC as before.

2. **Deploy new daemon second.** Once the daemon is upgraded, it begins
   subscribing to NIP-38 health events and checks each agent's capabilities.
   For newly upgraded agents that advertise `zone-subscribe`, the daemon
   switches to publishing zone-sync events. For any remaining old agents, it
   continues using RPC.

3. **Verify.** After both are upgraded:
   - Agent health events (kind 30315) should appear in the relay with
     `d=dns-agent` and the `capability` tag.
   - Zone-sync events (kind 30900, d-tag `zone-sync:<zone>`) should appear.
   - The agent log should show zone applies coming from the subscription
     path, not (or in addition to) RPC.
   - DNS resolution should be unaffected throughout.

4. **Monitor the dual-path window.** During the transition period where both
   paths may deliver the same zone serial, the no-double-apply guard ensures
   idempotent application. Monitor for:
   - `dns_zone_apply_total` — should stay at 1 per serial per zone
   - Agent logs: "zone sync already applied" (skipped duplicate)

## Cleanup: When to Remove the RPC Fallback

Once **all** agents in the fleet have been upgraded to the new version
(advertising `zone-subscribe` capability) and have been running stably:

1. **Remove from agent:**
   - ContextVM `SyncHandler` (RPC zone push handler)
   - ContextVM `Health` handler
   - The ContextVM transport startup in `cmd/bahia-dns-agent/main.go`

2. **Remove from daemon:**
   - `DnsmasqAgentBackend` (RPC backend)
   - `CapabilityAwareDNSBackend` wrapper (no longer needed; use
     `EventPublishDNSBackend` directly)
   - RPC transport/client code for DNS agent

3. **Remove shared code:**
   - ContextVM DNS agent service definition and protobuf/codec
   - The `CapabilityAwareDNSBackend` and `AgentHealthReader` types can be
     simplified once capability checking is no longer needed

**Follow-up issue:** The deletion of the RPC fallback code should be tracked
as a separate issue once fleet-wide adoption of the event path is confirmed.

## Troubleshooting

### Agent not receiving zone-sync events
- Check relay connectivity from the agent
- Verify the agent's subscription filter matches: kind 30900, d-tag prefix
  `zone-sync:`, topic `dns-zone-sync`
- Confirm the daemon's `EventPublishDNSBackend` is publishing (check daemon
  logs for "canonical DNS zone sync published")

### Daemon not detecting agent capabilities
- Verify the agent is publishing NIP-38 events (kind 30315, d-tag
  `dns-agent`)
- Check the NIP-40 expiration: events expire after 5 minutes; the agent
  must re-publish before expiration
- Confirm the daemon's health subscriber is running (check for
  "agent health subscriber" in daemon background runners)

### Duplicate zone applies during transition
- Expected and harmless: the no-double-apply guard ensures only one apply
  per serial. The duplicate is logged and skipped.
- If both paths are consistently delivering the same serial, the fleet is
  in the mixed-version window. Once old daemons are upgraded, RPC pushes
  will stop for capable agents.
