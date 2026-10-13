# Deleting leaked status events (NIP-09, `e` tags only)

Bahia status events — kinds `30360` (readiness), `31410` (identity) and
`31411` (replay checkpoint) — published by builds older than `36d6d2d7`
carry the service **private key** as their `d` tag (bahia-h9n7f). Every such
event is signed by the leaked identity. `bahia-leak-delete` finds them on
each relay, asks the relays to delete them with NIP-09 requests that name
them **by event id only**, and removes Bahia's own local copies.

Never delete these events by coordinate. An `a` tag is
`<kind>:<pubkey>:<d>`, and `d` is the leaked key: an `a`-tag deletion request
publishes the key again, to every relay it is sent to. The tool cannot build
one and refuses to publish one.

Deletion is hygiene, not containment. The key was readable on public relays
(`relay.damus.io` was in the publish set), so treat it as scraped. Rotation
to a new service identity (bahia-h9n7f.1) is the fix; run this procedure
during the rotation, **after** the new identity is active and no reader
trusts the old pubkey, and **before** the old key is discarded: the
deletions must be signed by the old key.

During the canonical-state re-assertion under the new service identity
(bahia-h9n7f.1), republish every secret-registry record with the current
publisher. It retains each secret's opaque UUID coordinate but places name,
service ID and environment ID only in OCK ciphertext. Current readers accept
both historical tagged records and new tagless records, so no relay-side
migration tool is needed for this format change. Re-assertion does not erase
plaintext tags from old relay copies; assess historical exposure separately
from the status-event deletion procedure below.

## Guarantees

- **Signer pinned to the leaked identity.** The tool signs only through the
  service signer seam (`servicesigner.Open`): a local key file, any NIP-46
  bunker, or a NIP-55L signer. Startup fails before any relay or file is
  touched unless the signer reports `--old-pubkey`. No key is accepted on
  argv; a local key is read only from `--key-file` (and refused if
  `BAHIA_LEAK_DELETE_PRIVATE_KEY` is set).
- **`e` and `k` tags only.** Every request is kind 5 by the leaked identity,
  with empty content, one `e` tag per event id the scans found and one `k`
  tag per kind of those events. A guard checks every request before it is
  signed, after it is signed (id and signature too) and again before every
  publish, including requests read back from the report. It refuses any `a`
  tag (any case or padding), any other tag, an id no scan found, and any tag
  value equal to a scanned `d` value or containing `nsec1`.
- **The key is never written.** Scans keep event ids, kinds and `created_at`
  only. `d` values are kept as SHA-256 fingerprints, which the guard compares
  against and which redact relay-supplied `OK`/`CLOSED` reasons that echo
  one. The nostr library's own logger (which prints a whole event when a
  relay sends one that does not match the REQ) is silenced.
- **Protocol.** Reads are REQ pages `{kinds:[30360,31410,31411],
  authors:[old], limit, until}` through Bahia's relay pool; a page is complete
  only at the relay's EOSE, NIP-42 AUTH is answered as the leaked identity,
  and a `CLOSED` or timeout leaves that relay's scan `incomplete`, never
  empty. Publishes record every relay's `OK` flag and message.
- **Rate limits.** Publishes pass Bahia's outbound admission controller as one
  bulk operation: paced by the bulk lane (5 requests per minute by default)
  and waiting out the shared rate-limit breaker. `rate-limited:`, `error:`
  and transport failures are retried in the next round after an exponential
  backoff. The kill switch (`BAHIA_NOSTR_OUTBOUND_KILL_SWITCH_FILE`) stops
  the run.
- **Resumable.** The report records each signed request before its first
  publish and every relay's answer after each publish. Re-running `delete
  --execute` republishes the same signed requests, only to relays that have
  not accepted them; new events from a later scan get new requests.

## Before you start

- Build the tool: `make build-bahia-leak-delete` (or
  `go build -o bin/bahia-leak-delete ./cmd/bahia-leak-delete`).
- Have the **old** key reachable through a signer, and its pubkey
  (`--old-pubkey`, hex or npub).
- List every relay that ever received Bahia status events: every
  `nostr.relays`, `nostr.service_relays` and sidecar URL the deployment ever
  used, and public relays such as `wss://relay.damus.io`. A relay missing
  from the list keeps its copies.
- Pick a report path outside any repository. It holds no key, but it lists
  the leaked event ids and the relays' answers; keep it for the record.

Signer flags (all subcommands that sign or AUTH):

```sh
--signer local  --key-file /secure/old-service.key        # 64 hex, mode 0600
--signer nip46  --bunker-uri-file /secure/old-bunker.uri --client-key-file /secure/nip46-client.key
--signer nip55l [--nip55l-bus ADDR] [--nip55l-app-id bahia]
--signer-timeout 30s
```

## 1. Scan

```sh
bahia-leak-delete scan --old-pubkey <old-pubkey> \
  --relay wss://relay.example --relay wss://relay.damus.io \
  --report leak-report.json --signer local --key-file /secure/old-service.key
```

Per relay it prints `complete` or `incomplete` (with the redacted reason),
the number of pages and events per kind, then one line per event:
`<id> kind=<kind> created_at=<RFC3339>`. Without signer flags, relays that
require NIP-42 AUTH to read are reported incomplete. `--page-limit`
(default 100), `--page-timeout` (default 30s) and `--concurrency` (relays at
once, default 4) tune the reads. A `truncated` relay held more events in one
second than a page returned; re-run with a larger `--page-limit`.

Exit status: `0` every relay complete, `2` some relay incomplete or
truncated (re-run scan for those relays; each scan replaces that relay's
previous result), `1` error.

## 2. Review the plan

```sh
bahia-leak-delete delete --old-pubkey <old-pubkey> --report leak-report.json
```

Prints every request's tags (`["e", "<id>"]`, `["k", "31410"]`, …) and each
target relay's state. Nothing is signed, published or written. Targets are
every scanned relay plus any `--relay` given here (for relays that refused
reads but accept writes). `--batch-size` (default 50, at most 500) sets the
events per request.

## 3. Publish the deletions

```sh
bahia-leak-delete delete --old-pubkey <old-pubkey> --report leak-report.json \
  --execute --signer local --key-file /secure/old-service.key
```

Each line is `relay <url> deletion <id>: <status> (<reason>)`, then a tally
per relay. Statuses:

| Status | Meaning | Next run |
|---|---|---|
| `accepted` | `OK true` | done |
| `duplicate` | the relay already has the request | done |
| `rejected` | `OK false` with `blocked:`, `invalid:`, `restricted:`, `pow:` or another policy reason | retried |
| `auth-required` | the relay still demanded NIP-42 after AUTH as the old identity | retried |
| `rate-limited` | `rate-limited:` after `--max-attempts` rounds (default 5) | retried |
| `error` | `error:`, a transport failure or an admission refusal | retried |

Exit status `0` means every request is accepted on every target relay; `2`
means some are not; re-run the same command (after fixing the relay side,
for example allowing the old pubkey to write) until it exits `0`. A relay
that answers `rejected` for good keeps the events: record it with the bead.

## 4. Verify

Re-run step 1. A relay that honours NIP-09 no longer returns the events; one
that accepted the deletion but still serves an event is flagged
`(deletion accepted by this relay, still served)`.

## 5. Purge Bahia's local copies

Stop `bahia-server` first: the event store and outbox are bbolt files with an
exclusive lock. Without `--execute` the command only lists; the outbox is
then opened read-only.

```sh
bahia-leak-delete purge-local --old-pubkey <old-pubkey> \
  --event-store /var/lib/bahia/nostr-cache/daemon.bolt \
  --outbox /var/lib/bahia/nostr-cache/outbox.bolt \
  --db-config /etc/bahia/config.yaml            # add --execute to remove
```

- **Event store** (`nostr.local_store.path`): removes the leaked identity's
  status events and the undelivered markers on their coordinates (the marker
  key embeds the `d` value), then compacts the file.
- **Outbox** (`nostr.local_store.outbox_path`): removes those entries in every
  state, then compacts the file. Entries pinned by a backup-run admission or
  holding a delivery proof are refused (status events never are).
- **PostgreSQL** (`--db-config`: a Bahia config whose `db` section names the
  database): deletes the rows of `nostr_events` in one transaction; rows that
  an archive batch claimed are listed with the batch id.

bbolt and PostgreSQL keep deleted bytes in free space: the tool compacts the
bbolt files; for PostgreSQL run `VACUUM FULL nostr_events` in a maintenance
window. A store file set aside as `<path>.corrupt-*` is reported and must be
deleted by hand.

## Not covered

- Archive artifacts of `nostr_event_archive_batches` (exported files and
  protected object versions) and PostgreSQL backups or WAL archives that
  predate the purge.
- Relays that ignore NIP-09, and copies anyone already fetched.
- The relay sidecar's own store: include the sidecar's URL in the relay list
  and it applies the `e`-tag deletions like any relay.
