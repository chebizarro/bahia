# Events

The **Events** route at `/events` is a live inspection view for the Nostr events that drive Bahia's control plane and read models.

## Loading and recovery

The browser loads:

- canonical Bahia read-model events, with the configured Bahia service author filter;
- Loom worker advertisements (kind `10100`);
- audit, status, and SBOM activity from the last seven days.

Read-model queries are capped at 1,000 events and the recent-activity query at 100. Long-lived subscriptions reconnect with a one-second overlap from the latest valid event timestamp and suppress duplicate event IDs. This makes short disconnects recoverable without presenting the overlap twice.

The relay indicator identifies the current connection and provenance. A connected relay is transport evidence, not authorization for an event: consumers still validate signatures, authors, schemas, and correlation rules.

## Filtering and inspection

Use the category filter to narrow the list to:

- Deployment
- Service
- LLM
- Policy
- SBOM
- Artifact

Choose 25, 50, or 100 rows per page. Select an event to open its complete JSON payload, including kind, author, tags, content, and timestamps.

## Operational use

Use Events to:

1. correlate a request with status, result, audit, or read-model events;
2. confirm whether a relay-delivered event has the expected author and tags;
3. inspect durable outcomes after a UI or MCP submission acknowledges only transport;
4. distinguish missing relay data from a projection or policy failure.

Do not treat the most recent arrival as canonical solely because it arrived last. Replaceable-event ordering, authorized authors, and the domain's correlation rules determine the accepted state.

## Related

- [Nostr Integration](../nostr-integration.md) — Event kinds, subscriptions, and replay
- [Deployments](deployments.md) — Deployment intent and result events
- [Artifacts](artifacts.md) — Artifact and SBOM events
