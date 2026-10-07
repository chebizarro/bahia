# Bahia Nostr Event Implementation Guide

This guide is for engineers adding or changing Nostr events in Bahia. The
wire contract itself — kinds, tags, coordinates, topics, encryption and
ordering — is in the [event specification](event-spec.md); this document only
tells you which mechanism to use and what a change must include.

## Core rule

Do not allocate a Bahia-specific event kind for a new semantic. Every
semantic Bahia has maps onto one of these, in this order of preference:

1. A **client-signed intent**: `30900` with `t=bahia-intent`
   ([event spec §3](event-spec.md#3-intents-30900-tbahia-intent)). Use it for
   every mutation, desired-state upsert and operator request. Add an
   operation to an existing domain before adding a domain.
2. **Canonical state**: a `30900` family with its own `#t` topic
   ([§5](event-spec.md#5-control-plane-state-30900-schemabahiacp-statev1)).
   Use it for anything a client asks "what is the current X" about.
3. **NIP-38 status** `30315` for progress and liveness
   ([§6](event-spec.md#6-operational-status-30315)).
4. **Audit** `4903` for an immutable fact ([§7](event-spec.md#7-audit-4903)).
5. **NIP-78 app data** `30078` or a **NIP-51 set** (`30002`, `30004`,
   `30000`) for documents, references and collections
   ([§8](event-spec.md#8-app-data-and-collections)).
6. **ContextVM** `25910` only for an interaction that is inherently
   request/response and not state. The registered methods are listed in
   [§4](event-spec.md#4-interactive-rpc-contextvm-25910); adding one needs a
   written justification that no state representation exists.
7. An **existing NIP** or interop protocol (Loom, Hive-CI, NIP-34,
   SoulFactory).

A new kind number requires a written justification that its relay behaviour
(replaceability, retention, indexing) differs from every mechanism above.

## Decision tree

| Question | Mechanism |
|---|---|
| Is a client asking Bahia to change or do something? | Intent `30900` + `t=bahia-intent`; result in the requester-scoped `30315` status |
| Is it the current value of an entity? | `30900` family record with `#t`; tombstone with `deleted=true` on the same coordinate |
| Is it progress, health or liveness? | `30315` |
| Is it something that happened and must never change? | `4903`, correlated by `state`, `t`, `e`, `fact` |
| Is it a document, reference or curated inventory? | `30078` / `30004` / `30000` |
| Is it bootstrap or routing information? | `11316`–`11320` content or a `30002` relay set; never a new kind |
| Is it a secret reveal, a log fetch or an assistant turn? | ContextVM `25910` inside a gift wrap |
| Is it a delete? | NIP-09 `5` for relay-level deletion; a `deleted=true` tombstone for durable domain state |

## What every change must include

1. **Constants, not literals.** Kinds and topics come from `internal/kinds`
   (and `cascadia-go` for Cascadia kinds). The architecture ratchet
   (`make lint-arch`) rejects numeric legacy kinds outside
   `internal/nostrmigration`.
2. **A `#t` topic** on every new `30900` family, live records and tombstones
   alike, and a public/protected classification in
   `internal/relaysidecar/read_auth.go`. Relays index single-letter tags
   only; `domain`, `schema` and `entity` are checked locally.
3. **An author-minted coordinate.** Entity ids are UUIDv7 chosen by the
   author ([§10](event-spec.md#10-entity-identity)); natural keys stay
   natural. Never derive `d` from a database sequence.
4. **Encryption where the content is confidential.** Org-scoped records use
   the org content key; fleet-scoped records use the fleet OCK. Public tags
   of an encrypted record carry no identifying material (no `p`, no pubkey
   in `d`).
5. **Sanitized content.** No secret plaintext, generated env files, raw Docker
   transport material, TLS material, bearer or NIP-98 credentials, or
   free-form error text in relay content.
6. **Publication through the outbox.** Service-authored events go through
   the signed publisher so they are durable before the first relay attempt
   and tracked per relay `OK`.
7. **Reads by subscription.** Consumers REQ with `authors` + `#t` (or `#d`),
   treat `EOSE` as caught-up, keep the subscription open and deduplicate by
   event id. No poll tickers without a `//nostr:allow-poll <reason>`
   annotation; no HTTP or MCP polling for anything with a canonical
   observable.
8. **A fixture.** Intent shapes are generated from Go into
   `web/tests/fixtures/*intent*.json`; the web and `pkg/client` parse them.
   Update the generator and the consumers together.
9. **Ordering tests that respect NIP-01.** A later publication within the
   same second is not a newer revision; assign distinct `created_at` seconds
   when a test asserts a succession of states and test genuine same-second
   conflicts separately (lowest id wins).
10. **Documentation.** Add the family to the topic table in the
    [event specification](event-spec.md#family-topics) and, for an
    operator-facing surface, to the [user guide](user-guide/index.md).

## Interop exceptions

- **Hive-CI.** `build/request` is an intent, but the CI bus is Hive-CI's own
  durable `5401`/`5402`; Bahia's self-dispatch publishes a tag-only `5401`
  with a per-run ephemeral `publisher` key and targets a Loom `5100` job
  (`cmd=loom-ci`) at a trusted worker advertising `loom-ci`, carrying only
  NIP-44-encrypted `secret` tags for that worker and no payment tag.
- **SoulFactory.** Templates, drafts, souls, provisioning and runtime
  control use the SoulFactory kinds exchanged directly with its reactors and
  runtimes; Bahia projects provisioning progress into `30900`/`4903`.
- **Loom.** Job requests, status and results use Loom kinds; Bahia projects
  run health into `30315` and, when `loom.canonical_projection.enabled`,
  job state into `30900`/`4903`.
