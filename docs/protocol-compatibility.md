# Bahia protocol compatibility

This matrix defines the protocols Bahia actively exchanges. The authoritative
Bahia wire shapes are in the [event specification](event-spec.md); kind and
topic constants are in `internal/kinds`.

## Bahia control plane

| Capability | Contract | Required behavior |
|---|---|---|
| Desired-state mutation | `30900`, `t=bahia-intent` | client-signed; sensitive domains use a `1059`/`21059` gift wrap |
| Intent disposition | `30315`, `t=intent-status` | requester-scoped accepted/rejected status; not durable state |
| Canonical state | `30900`, `schema=bahia.cp-state.v1` | addressable latest-wins record with a family `#t` topic |
| Operational status | `30315` | addressable NIP-38 state/progress |
| Audit | `4903` | immutable regular event without `d` |
| Interactive RPC | ContextVM `25910` | assistant, secret reveal and stored run-log fetch only |
| Discovery | `11316`–`11320`, `30002`, `10002`, `10050` | trust the configured service author; relay lists are purpose-specific |
| Documentation | `30023` | long-form pages published by `internal/docs` |

Clients must verify event IDs/signatures, apply NIP-01 replacement rules,
deduplicate by event ID, process stored events through `EOSE`, remain subscribed
for live events, handle `AUTH` and `CLOSED`, and check every publish `OK`.

## Supported NIPs

The Bahia relay sidecar advertises NIPs 1, 9, 11, 17, 40, 42, 44, 45, 51,
59, 65, 70 and 77.

| NIP | Bahia use |
|---|---|
| NIP-01 | REQ/EVENT/EOSE/OK/CLOSED and replacement semantics |
| NIP-09 | author-owned event and coordinate deletion |
| NIP-11 | relay metadata, limits and `auth_required` |
| NIP-17 / NIP-44 / NIP-59 | encrypted messages, seals and gift wraps |
| NIP-40 | expiration enforcement and sweeping |
| NIP-42 | protected relay reads and relay-specific authentication |
| NIP-45 | COUNT on the same indexed filter surface as REQ |
| NIP-51 | relay sets, membership lists and curation sets |
| NIP-65 | advisory service relay preferences |
| NIP-70 | protected-events behavior where used by the event library |
| NIP-77 | bounded negentropy reconciliation |
| NIP-78 | app data such as SBOM and config documents |
| NIP-98 | daemon HTTP/MCP and relay NIP-86 administration authentication |

Search filters (NIP-50) are rejected by the sidecar.

## Interoperability protocols

| System | Active events | Bahia behavior |
|---|---|---|
| Loom | `10100`, `5100`, `30100`, `5101`, `5102` | reads worker advertisements/status/results; publishes job requests/cancellations; may project job state to Bahia canonical records |
| Hive-CI | `5401`, `5402` | publishes/consumes workflow runs and consumes signed results; accepted release/artifact state is projected to `30900`/`4903` |
| NIP-34 | `30617`, `30618`, `1617`–`1619`, `1621`, `1630`–`1633`, `10317`, replies `1111` | repository discovery and collaboration events remain open interop traffic |
| Soul Factory | `31950`–`31953`, `5950`/`6950`/`7950`, `1950`, `30317`, `38384`/`38386` | templates, souls, drafts, lifecycle, capability and runtime-control exchange |
| Config Fabric | `30000` membership, `30078` desired document | sidecar applies trusted desired state and publishes `30900` `config-status` |
| Cascadia widgets | `30318` | web renders allowlisted publishers read-only |

Protocol-specific publishers keep their native event families. Bahia does not
wrap Loom, Hive-CI, NIP-34 or Soul Factory traffic in a request/response
abstraction; it projects Bahia-owned current state and audit evidence where
needed.

## Confidential state

Org-scoped records use an organization content key (OCK); fleet-scoped records
use the fleet OCK. Key envelopes are signed `org-key-envelope` records and the
ciphertext record binds its family, coordinate, topic and key version as AEAD
associated data. Public relay access to ciphertext does not grant plaintext
access.

An explicit `org/rekey` refounds the key and republishes every confidential
record at its existing coordinate. Organization `strict_revocation` defaults
to `false`; when true, member removal or role downgrade triggers the same
refounding. The `ock_rotation` readiness check remains non-passing while a
refounding cannot complete.

See [event specification: organization rekey](event-spec.md#organization-rekey)
and [confidential-state architecture](architecture/confidential-state.md).

## Client compatibility requirements

### Web

The web app requires runtime `PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and
`PUBLIC_BAHIA_SERVICE_PUBKEYS`, validates service-authored discovery, hydrates
its IndexedDB store from subscriptions, and signs with NIP-07 or NIP-46.
Feature pages are gated by the service announcement.

### CLI and `pkg/client`

Operator clients resolve relays from explicit flags/environment or trusted
service discovery, maintain a local outbox, authenticate protected reads with
the operator signer and follow `30315` intent status. Machine-readable output
remains on stdout while progress uses stderr.

### MCP

`POST /mcp` is JSON-RPC over HTTP and is dependency-gated by PostgreSQL.
Mutation tools publish the same signed intent contract in process; state-read
tools read the daemon's local signed-event store. Exact tools and arguments
are in the [MCP reference](user-guide/mcp-tools.md).

### Relay sidecar

Production deployments use `nostr.sidecar.read_auth_mode: enforce`. Public
`30900` families must be requested with a public `#t`; a broad `30900` filter,
a protected topic or a protected kind requires NIP-42 and an admitted reader.
See the [relay sidecar reference](relay-sidecar.md).
