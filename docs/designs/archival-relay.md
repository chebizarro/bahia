# Design Note: Dedicated Archival Relay (C-19)

**Date:** 2026-10-02
**Status:** Proposal
**Finding:** C-19, C-22, C-39

## Problem

The sidecar relay's retention policy deletes regular events after a configurable
period (previously 7 days, now durable by default). Replaceable and addressable
events are never age-swept—latest-wins bounds them. However, this means the
sidecar is not a durable archive: it keeps only the most recent version of
addressable state, and regular audit facts can be aged out.

Today, Postgres `nostr_events` (~19 GB, ~12M rows) and `bahia-event-archive`
serve as the de-facto event log. This inverts the Nostr-first goal: relays
should be the durable record, with Postgres as a disposable cache.

## Proposal

Deploy a dedicated **archival relay** alongside the edge sidecar. The archival
relay is durable, append-only for its accepted kinds, and negentropy-enabled.

### What gets archived

| Kind class | Examples | Archive? | Rationale |
|---|---|---|---|
| Audit facts (regular 4903) | Security scans, compliance checks | **Yes** | Durable audit trail; the sidecar may age-sweep regular events |
| Control-plane state (addressable 30900) | Fleet state, worker state, DNS state | **Yes** (all versions) | History of state transitions; the sidecar keeps only the latest |
| Config-status (addressable 30900) | Applied/rejected config | **Yes** (all versions) | Configuration audit trail |
| Config-fabric (30051/30078) | Desired config lists/policies | **Yes** | Policy history |
| NIP-38 status (30315) | Operational status | **Yes** | Operational history |
| Assistant transcripts | Agent conversations | **Yes** | Session replay, debugging |
| Request/transport (25910, 1059, 21059) | ContextVM messages, gift wraps | **No** | Ephemeral RPC; short retention on the sidecar is correct |
| Ephemeral (20000-29999) | Typing indicators, presence | **No** | By definition not stored |
| Profiles, relay lists, follows | NIP-01 standard kinds | **No** | The sidecar already keeps these as latest-wins; public relays are the canonical source |

### Retention

- **Audit facts (4903):** Retained indefinitely. These are the regulatory and
  compliance record.
- **Addressable state (30900, etc.):** All historical versions retained. The
  sidecar keeps only the latest, but the archival relay stores every version
  that was ever published. NIP-01 replacement is disabled or the store is
  configured to keep superseded versions.
- **Config-status:** 90-day retention. Config-status events now carry NIP-40
  expiration (C-22 fix), so the archival relay sweeps them automatically.
- **Everything else:** Not accepted; the archival relay rejects kinds it does
  not archive at the `OnEvent` policy level.

### Architecture

```
                        ┌──────────────┐
                        │ Edge Sidecar │  (latest-wins, short request retention)
   clients ◄──────────►│  :3334       │
                        └──────┬───────┘
                               │ NIP-77 negentropy sync
                               ▼
                        ┌──────────────┐
                        │  Archival    │  (append-only for audit/state kinds)
                        │  Relay :3335 │
                        └──────────────┘
```

- The archival relay is a separate Khatru instance backed by an LMDB event
  store (`fiatjaf.com/nostr/eventstore/lmdb`), which provides real tag indexes,
  NIP-09 semantics, and efficient storage.
- Synchronization: the edge sidecar and archival relay reconcile via NIP-77
  negentropy. The archival relay subscribes to the sidecar's audit and state
  kinds and stores every version. This is a pull model: the archival relay runs
  a periodic negentropy sync against the sidecar.
- The archival relay does **not** accept writes from external clients. It is
  read-only except for negentropy sync from the sidecar.
- NIP-42 read auth applies the same policy as the sidecar (C-21).

### How clients query history

1. **REQ with `since`/`until`:** Clients query the archival relay with
   time-range filters. The archival relay returns all versions of addressable
   events within the range, not just the latest.
2. **NIP-77 negentropy:** Clients with local event stores can reconcile against
   the archival relay to fill gaps.
3. **Discovery:** The sidecar's NIP-11 info document lists the archival relay
   URL in a custom `archival_relay` field. Clients that need history query it
   directly.

### Deployment

- The archival relay runs as a separate process (or container) on the same host
  as the sidecar, or on dedicated storage.
- Its data directory is backed up independently. Since all data originates from
  signed Nostr events, the backup is a simple LMDB snapshot.
- No Postgres dependency: the archival relay replaces the `nostr_events` table
  and `bahia-event-archive` script (C-39). Once the archival relay is proven
  stable, those Postgres tables can be dropped.

### Migration path

1. Deploy the archival relay alongside the sidecar.
2. Run initial negentropy sync to seed it from the sidecar's current store.
3. One-shot migration: export relevant events from Postgres `nostr_events` as
   NDJSON, re-publish them to the archival relay. This catches events the
   sidecar has already swept.
4. Validate: query the archival relay for known audit facts and state history.
5. Once validated, remove the `bahia-event-archive` cron job and plan the
   `nostr_events` table deprecation.

### Open questions

- **Multi-relay archival:** Should there be more than one archival relay for
  redundancy? If so, they sync with each other via negentropy.
- **Retention policy for superseded addressable events:** Keep all versions
  forever, or bound by count (e.g., keep the last 100 versions per coordinate)?
- **Access control:** Should the archival relay have a more restrictive read
  policy than the sidecar (e.g., audit facts visible only to operators)?
