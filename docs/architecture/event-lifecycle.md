# Event lifecycle resolution

Every consumer of relay events — the daemon (`internal/nostrutil/lifecycle.go`,
the one place Go code resolves event state), the local store, the CLI and the
web (`web/src/lib/nostr/ingestion.js`) — applies the same three rules, so the
same set of events always yields the same state regardless of arrival order:

1. **NIP-01 replaceable and addressable events.** Per `(kind, pubkey)` for
   replaceable kinds (`0`, `3`, `10000-19999`) and per `(kind, pubkey, d)` for
   addressable kinds (`30000-39999`), the latest `created_at` wins; on equal
   `created_at` the **lowest event id** wins. An older version is rejected on
   arrival. Producers that must publish several versions within one second
   (the assistant projection) stamp `created_at = max(now, last + 1)`.
2. **NIP-09 deletion requests (kind `5`).** An `e` reference deletes that id
   when the requester is its author; an `a` reference deletes every version of
   the requester's coordinate up to the request's `created_at`. A deleted event
   never comes back, whichever order the request and the target arrive in.
   Entity removal in Bahia is **not** NIP-09: the daemon publishes a `30900`
   tombstone (`deleted: true`) so readers learn the entity was removed
   deliberately (see [intents](intents-and-authority.md#12-level-triggered-desired-state)).
3. **NIP-40 expiration.** An event whose `expiration` tag is in the past is
   ignored on arrival, and a live event is dropped when its expiration passes
   (the web sweeps every five minutes; the sidecar sweeps stored events).
   Intent-status events (`30315`) expire after seven days.

Validation precedes all three: the event id must equal the NIP-01 serialized
hash, the Schnorr signature must verify for `pubkey`, and kind-specific
required tags must be present. Handlers are idempotent by event id.
