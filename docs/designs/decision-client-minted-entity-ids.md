# Decision: client-minted UUIDv7 entity ids

- Status: accepted (2026-09-30)
- Issue: bahia-irsry.35 (blocks Phase 3, bahia-irsry.11)
- Findings: C-40, RC-1, B-6, B-25 in `docs/investigations/nostr-first-architecture-audit-2026-09-29.md`
- Charter: invariant 6 in `docs/architecture.md` ("`d` tags are minted by the author")
- Normative text: `docs/event-spec.md` "Entity identity and coordinates"; `docs/nostr-event-implementation-guide.md` "Entity identity for create paths"

## Context

Addressable coordinates carried Postgres UUIDs: rows were created first, and the id came from `uuid.New()` in a handler or repository, or from `DEFAULT gen_random_uuid()`. Only the daemon with a database could therefore create an entity. Clients had to mutate over RPC and treat the reply as truth, which blocks moving authority to signed desired-state events (Phase 3). The relay-first registry path also published its canonical event *before* the repository minted the id. A create without a pre-set id went out on `d=00000000-0000-0000-0000-000000000000`.

## Options considered

1. **Client-generated, time-sortable id fixed at creation: UUIDv7 (chosen), or ULID.**
   - Any signer can create an entity offline.
   - A retry reuses the id, so creates are idempotent.
   - Renames never move the coordinate.
   - UUIDv7 is still a UUID. Every existing `uuid.UUID` field, Postgres `uuid` column, the `<prefix>:<uuid>` grammar and every consumer keep working, and existing v4 coordinates stay valid with no re-keying.
   - ULID would need a new text encoding, new column types and dual-format decoders for no functional gain.
2. **Natural key (`<domain>:<org>:<slug>`).**
   - Deterministic, and uniqueness looks inherent.
   - Renames change identity, so every reference and tombstone must move.
   - Deleting and recreating a name reuses a coordinate that already carries a tombstone.
   - Two signers choosing the same slug still need an arbiter.
   - Org slugs and names leak into public coordinates.
   - It fits only domains whose key is globally unique by nature and never renamed (DNS zones already use `zone:<name>`).
3. **Deterministic hash of the natural key (UUIDv5).** This has the same rename and arbitration problems as (2). The id is also predictable, so a third party can pre-claim it.

## Decision

- One rule for every domain whose identity is not inherent: **the author mints an RFC 9562 UUID and fixes it at creation**. New ids are UUIDv7; v4 is accepted in create intents; everything else is rejected.
- Natural-key coordinates stay only where the key *is* the identity: DNS zones/backends, content-addressed SBOM/security references, and request-event-id correlated state.
- Natural keys like `(org, name)` are uniqueness constraints enforced by the authoritative applier. The first applied create wins, ordered by `(created_at, event id)` in Phase 3.
- A create resolves by id, then content:
  - no entity: create;
  - same content: idempotent replay, with no write and no republish;
  - different content: conflict (`domain.ErrEntityIDConflict`, JSON-RPC `-32010`).
- The stored org is part of the compared content. An id therefore cannot reach into another org. Relays already scope addressable events by author pubkey, so no signer can overwrite another's coordinate.

## Consequences

- Services and environments implement the rule end to end:
  - web dialogs mint once per attempt;
  - ContextVM `service/create` / `environment/create` accept `id`;
  - `RegistryService` / `RelayFirstRegistry` resolve replays and conflicts before publishing;
  - repositories store the id verbatim and classify primary-key hits.
- The relay-first nil-UUID publish bug is fixed, because the id is minted before publication.
- No schema migration is needed. The `id uuid` primary keys already accept client values, and their `DEFAULT gen_random_uuid()` becomes unused rather than removed.
- The projector's coordinate builders are already input-agnostic (`canonicalStateDTag` is the identity on the id, `serviceStateDTag` composes two ids). They need no change.
- Remaining domains adopt the same pattern with their Phase 3 slices. The event spec lists them.
