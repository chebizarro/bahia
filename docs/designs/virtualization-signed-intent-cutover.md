# Virtualization signed-intent cutover

Normal daemon assembly keeps virtualization unavailable. It does not construct
its PostgreSQL journal projector or runtime, does not serve SQL-backed
virtualization reads, and reports `virtualization_canonical_recovery=warn`
when PostgreSQL or a virtualization provider is configured. Existing SQL
journal rows are preserved; they are not instructions to sign state or audit
events. Previously signed events in the local outbox remain subject to the
ordinary outbox recovery contract.

## Required mutation boundary

A virtualization mutation needs an operator-signed intent containing the
organization, stable resource and operation ids, expected generation,
idempotency key, request digest and approval reference. The transport validates
its signature and authorization before admission. The accepted intent is
retained in the local event store and on relays before any PostgreSQL index
write or provider effect. The intent processor deduplicates by event and
intent id, checks expected generation against the latest canonical coordinate,
and emits service-signed state and audit outcomes through the local outbox.
PostgreSQL can then index those outcomes; it cannot supply missing outcomes.

Provider observations and operation transitions need durable service-signed
facts with stable observation/operation keys. A provider effect is reattached
or retried by the accepted operation id and its canonical progress record,
not by a queued SQL row. A signed progress transition is admitted to the
outbox before the derived index advances. Multi-daemon effect execution needs
canonical fencing or an enforced single-writer topology; the current
PostgreSQL advisory lock is not sufficient when the database is absent.

Restart recovery replays validated relay/local intents, latest state and audit
facts, tombstones, and pending local outbox entries. It never scans
`virtualization_resource_changes` to decide what to publish. The replay must
be idempotent across: intent accepted before state signing, state queued before
relay acceptance, relay acceptance before SQL indexing, provider dispatch
before progress publication, and process loss during index rebuild. Equal-time
addressable winners and deletion/expiration follow the shared event lifecycle
contract.

## Existing journal disposition

An explicit operator-controlled cutover inventories SQL journal rows in
bounded keyset pages and compares their resource coordinates, generation and
content digests with validated relay/local state. SQL-only and divergent rows
are reported as conflicts. No daemon boot, reconnect, or database recovery
path imports or re-signs them. A governed resolution can create a new signed
operator intent referencing the legacy row digest, after review; the legacy
row alone cannot authorize publication. The cutover also inventories signed
pending local outbox entries separately so an accepted event is never
re-signed or discarded because its SQL index row is missing.

Acceptance requires PostgreSQL absent, unavailable, slow, empty, redundant and
divergent fixtures; zero SQL-attributable publication; bounded core readiness;
exact relay/local latest coordinates; and deterministic crash/restart tests at
each boundary above. Provider and approval flows must regain their existing
semantics before virtualization is enabled. The feature-level suspension is a
release blocker, not evidence that this cutover has been implemented.
