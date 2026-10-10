# Architecture contracts

Present-tense descriptions of how Bahia works, written for engineers and
coding agents. The charter — relays canonical, databases derived — is in
[`docs/architecture.md`](../architecture.md); the event catalogue is
[`docs/event-spec.md`](../event-spec.md) and
[`docs/nostr-event-implementation-guide.md`](../nostr-event-implementation-guide.md).
Code wins over any page here; if they disagree, fix the page.

| Page | Covers |
|---|---|
| [intents-and-authority.md](intents-and-authority.md) | Signed `30900` intents, trust set, the daemon's processing pipeline, `30315` status, canonical state, readiness, adding a domain handler |
| [outbox-delivery.md](outbox-delivery.md) | Local outbox durability and the abandonment contract (undelivered markers, `canonical_delivery`, operator retry) |
| [confidential-state.md](confidential-state.md) | Org content keys (NIP-CAS-0011): envelopes, key distribution, rotation and refounding, operator allowlists, what readers do |
| [service-signer.md](service-signer.md) | The service identity's signer (local, any NIP-46 bunker, NIP-55L): identity pinning, standard NIP-46 contract, binary NIP-44 capability |
| [entity-identity.md](entity-identity.md) | Author-minted UUIDv7 ids, natural keys, replay vs conflict |
| [event-lifecycle.md](event-lifecycle.md) | NIP-01 latest-wins, NIP-09 deletion and NIP-40 expiration as every consumer applies them |
| [web-store-first.md](web-store-first.md) | The web app's event store, boot sequence, derived views, pending intents and what stays on ContextVM |
| [cli-and-mcp.md](cli-and-mcp.md) | `NostrClient` reads, `IntentPublisher` writes and exit codes, config fabric, in-daemon MCP, the daemon's HTTP surface |
| [assistant-execution.md](assistant-execution.md) | The assistant's durable executor: workflows, approval hashing, checkpoints, uncertainty and recovery |
| [postgres-event-store-lifecycle.md](postgres-event-store-lifecycle.md) | Retention classes and the two-phase archive protocol for `nostr_events` |
| [ratchets.md](ratchets.md) | The architecture gates, how to extend them, baseline regeneration, `CPStateFamily` allocation |
