# Relay configuration

How Bahia decides which relays carry its control plane, how to change them,
and what the daemon refuses. Contracts: [outbox delivery](../architecture/outbox-delivery.md),
[control planes](../control-planes.md), [deployment](../deployment.md#relay-topology).

## One effective relay policy

The **canonical relay settings** govern: the service-signed `30900` record at
`d=relay-settings:operator` (schema `bahia.relay-settings.v1`), written by a
fleet operator's signed `relay`/`policy-set` intent (web relay settings). The
static `nostr.service_relays` (falling back to `nostr.relays`),
`nostr.contextvm_relays` and `nostr.browser_relays` are **bootstrap only**:
they are in force until the canonical settings are received.

1. **Startup.** The daemon connects with the static lists, then restores the
   last canonical settings it accepted (PostgreSQL projection, or a durable
   local control record without PostgreSQL) and applies them before any
   intent or publication runs.
2. **Catch-up.** The relay settings hydrator REQs the canonical coordinate on
   every configured and policy-named relay. Events are validated, checked
   against the relay guard below, and promoted latest-wins (`created_at`,
   then lowest id); a refused event is never promoted, stored or applied, so
   the newest event on the relays can be refused while the last accepted
   settings stay in force across restarts. After EOSE from every relay it
   marks catch-up, and only then are the relay-set events advertised.
3. **Runtime change.** A new canonical record is applied as soon as the
   hydrator receives it: the relay pools are reconfigured in place (open
   subscriptions are re-issued on the new relays; reconnects use the pool's
   exponential backoff), the service publisher's write set changes on the
   next delivery round, and the relay-set events are republished. Nothing
   polls.

The policy record, audit facts, and service DM relay lists enter the daemon's
outbox before relay I/O. Each needs the configured service write-relay quorum;
an incomplete round remains queued. If the new policy changes the write set
while its own event is pending, subsequent rounds use the new effective set.
Relay-policy projection backups contain the complete signed policy event;
restore rejects the old v1 backup format and verifies the event ID and Schnorr
signature against the configured service author. A projection predating signed
event retention must be received from a relay again before it can be exported.

Each canonical list governs only when it is non-empty; an empty list leaves
the bootstrap list in force. The same effective lists drive:

| Consumer | Derived from |
|---|---|
| Service pool and service publisher write set (`nostr.publish_quorum` accounting) | service relays (`ServiceWriteRelays`: the sidecar once, by `backend_url`; Loom relays share the pool but are never write relays) |
| Control-plane and ContextVM pools | sidecar, ContextVM relays (falling back to browser relays) |
| `30002` `bahia-browser-v1` / `bahia-contextvm-v1` / `bahia-service-v1` / `bahia-nip34-v1`, `10002` | the same lists, published by the projector after catch-up |

## Controlled relays and the guard

`nostr.controlled_relays` lists the relays Bahia operates; the sidecar's
`backend_url` and `public_url` are always controlled. Unset means the sidecar
alone. It is **required** — startup fails and names the fix — when the daemon
publishes (`publish_enabled`) with a service identity and any service,
ContextVM or browser relay is not the sidecar; `relay_topology` warns while it
is unset.

- **Startup fails** when a service, ContextVM or browser relay is not
  controlled, or when any control-plane list or `controlled_relays` names a
  well-known public relay (damus.io, nos.lol, primal.net, nostr.wine,
  nostr.band, snort.social, ... — `internal/relayguard`).
- **Canonical settings are refused** at runtime under the same rules: the
  topology in force is kept, the refusal is logged at error level and
  `relay_topology` health turns `warn`. The relay-policy intent is rejected
  before anything is signed. Canonical settings can only choose among
  controlled relays, so a stolen service key cannot move the control plane to
  another relay through relay settings; to add a relay, list it in
  `controlled_relays` and restart first.
- **Every daemon relay pool refuses** to write a service-signed control-plane
  event (`30900` cp-state including OCK and NIP-44 payloads, `4903`,
  `30315`, `31410`/`31411`/`30360`, relay and discovery lists, plaintext
  ContextVM `25910`, NIP-09 deletions) to a relay that is not controlled. The
  control-plane, ContextVM and service pools also keep NIP-59 gift wraps
  (ephemeral signer) on controlled relays; the service pool holds the Loom
  relays too. The relay gets a `blocked:` result, which the outbox treats as
  that relay's permanent rejection. Each refused (relay, kind) is logged at
  error level once; `bahia_nostr_control_plane_guard_refusals_total` counts
  them all.
- `bahia-migrate nostr` and `bahia-migrate f74a-import` sign with the service
  key: their target relays (`--relays` or config) must pass the same rules,
  and their pools carry the same guard.
- Public relays are allowed only in non-control-plane lists (`loom.relays`,
  `nostr.nip34_relays`, `nostr.dm_relay_lists`, `soul_factory.*`,
  `dns.backends.*.agent_relays`, `fips.relay_urls`, and the canonical NIP-34
  and DM relay lists) and only when `nostr.allowed_public_relays` names them.

## Publish quorum

`nostr.publish_quorum` (default `1`; `-1` = every write relay) counts accepted
`OK`s among the service write relays only. A quorum above 1 needs at least
that many service write relays: configuration with fewer fails validation and
canonical settings with fewer are refused. With two Bahia-controlled service
relays, set `publish_quorum: 2` so a publish is durable only when both hold it.
The control-plane read-model publisher (sidecar or ContextVM relays) caps the
quorum at its own relay count.

## Health

`GET /ready` → `relay_topology`:

| Detail | Meaning |
|---|---|
| `source` | `bootstrap` (static lists) or `canonical` |
| `caught_up` | canonical settings caught up (EOSE); relay sets are advertised. Still `false` after 5 minutes: `warn` |
| `service_relays`, `contextvm_relays`, `browser_relays` | effective lists |
| `service_write_relays`, `publish_quorum`, `required_acceptances` | what a publish must reach |
| `controlled_relays` | the guard's set |
| `static_service_relays`, `drift` | static lists that differ from the canonical ones (`warn`) |
| `refused` | why the latest canonical settings were refused (`warn`); the last accepted settings stay in force |

`relay_policy_projection` reports the canonical record's event id and hash.
Its `confirmation=relay_confirmed` detail means the accepted head arrived as
an EVENT from a configured relay in the current subscription, before or after
EOSE. A PostgreSQL or local projection read alone reports `cached`.

## Migration: 2026-10-11 production relay set

Production ran with `nostr.relays: [wss://relay.sharegap.net, wss://relay.damus.io]`,
no `service_relays` and quorum 1, so the control plane reached a public relay
and one `OK` from damus counted as durable. This release refuses that
configuration at startup. With the service key rotation:

1. Set the static lists to the target and remove `relay.damus.io` everywhere:

   ```yaml
   nostr:
     service_relays:
       - wss://bahia.sharegap.net/relay
       - wss://relay.sharegap.net
     controlled_relays:
       - wss://bahia.sharegap.net/relay
       - wss://relay.sharegap.net
     publish_quorum: 2
   ```

   Drop `nostr.relays` (it is only the fallback for `service_relays`) and any
   `BAHIA_NOSTR_RELAYS` override. Keep `browser_relays`/`contextvm_relays` on
   Bahia-controlled relays and list them in `controlled_relays`.

   Startup also now refuses well-known public relays in the non-control-plane
   lists unless `nostr.allowed_public_relays` names them. Check
   `loom.relays`, `nostr.nip34_relays`, `nostr.dm_relay_lists`,
   `soul_factory.relays`/`additional_relays`/`nip05_relays` (purplepag.es is
   a common NIP-05 relay), `dns.backends.*.agent_relays` and `fips.relay_urls`:
   remove each public relay or list it in `allowed_public_relays`. The error
   names the key and the relay.
2. Start the daemon. `relay_topology` shows `source=bootstrap`, then
   `canonical` once the existing relay settings are received.
3. Apply the relay policy again from the web relay settings with the same
   service relays. Check `relay_topology`: `pass`, no `drift`,
   `required_acceptances=2`, and the `bahia-service-v1` relay set lists the
   two relays.
4. Confirm publishes record `OK` from both relays: MCP `bahia_outbox_status`
   with `include_details` shows no pending entries short of the quorum, and
   `canonical_delivery` stays `pass`.
5. Kind-5 deletion requests are not control-plane kinds, so the guard does
   not stop the one-off NIP-09 deletions of the leaked events on damus.io
   (bahia-h9n7f.2).
