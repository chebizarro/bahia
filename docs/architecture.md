# Bahia Architecture

This is the overview. The detailed architecture documents live in
[`docs/architecture/`](architecture/README.md) and are linked from each
section below.

Bahia is a deployment and runtime control plane whose canonical state lives
on Nostr relays as signed events. It registers builds and artifacts, holds the
desired state of services across environments, executes deployments and
rollbacks on Loom workers or direct runtime targets, observes what is running,
detects drift, and records every change as relay-verifiable evidence.

## Invariants

1. **Relays are the source of truth.** Shared state is addressable events —
   `30900` control-plane state, `30315` status, `30078` app data and the
   relevant standard NIPs; history and attestations are regular `4903`
   events. Latest-wins by coordinate replaces row updates.
2. **Reads are REQ subscriptions.** Every client — web, CLI, MCP, sidecars
   and the daemon itself — reads by subscribing with narrow filters, treats
   `EOSE` as caught-up and keeps the subscription open. A local event store
   (IndexedDB in the browser, a bbolt store per Go process) holds what has
   been seen so rendering and restart never wait on a re-fetch.
3. **Writes are signed events verified by relay `OK`.** The author signs an
   intent and publishes it; `OK` is delivery, not business completion.
   Durable progress and terminal truth come from the canonical observables
   the daemon publishes.
4. **ContextVM is interactive RPC only.** `25910` carries assistant turns,
   secret reveal and run-log fetch — never a read of state.
5. **Identity and authorization are event state.** Entity ids are minted by
   the author (UUIDv7); membership, roles and operator allowlists are events
   relays serve and clients verify.
6. **PostgreSQL is an index.** It is optional for the daemon to start and
   is never the trigger for publishing.

The architecture ratchet (`make lint-arch`, also run by `go test ./...` and
`pnpm run test:unit`; see [ratchets](architecture/ratchets.md)) enforces
these: no legacy kind numbers outside
`internal/nostrmigration`, no direct library relay subscriptions outside the
relay pool and SoulFactory bus, no unannotated poll tickers in
`internal/service`/`internal/reconcile`, no test-only exports in `internal/`,
no `setInterval` or HTTP-client use in web stores, and no HTTP route a
DB-less daemon exposes without a dependency gate.

## Components

```text
 web (SvelteKit, store-first)   bahia CLI / pkg/client     MCP agents
        │ REQ / intents                │ REQ / intents          │ POST /mcp
        ▼                              ▼                        ▼
 ┌──────────────────────── relay sidecar (khatru, bbolt) ───────────────────────┐
 │  intents · ContextVM wraps · canonical state · status · audit · discovery   │
 └───────────────▲──────────────────────────────────────────────▲──────────────┘
                 │ publish (outbox)                              │ subscribe
            ┌────┴──────────────────── bahia daemon ─────────────┴────┐
            │ intent processor → domain handlers → registry/services   │
            │ projector · reactors · supervisors · schedulers          │
            │ local event store (bbolt) · publish outbox               │
            └──┬──────────┬──────────┬───────────┬──────────┬─────────┘
               │          │          │           │          │
           PostgreSQL   Blossom   OCI /v2    Loom workers  runtime targets
           (index)     (blobs)   (images)   (jobs)        (docker/compose/k8s/podman)
```

### Relay sidecar

`bahia-relay` (`cmd/relay`, `internal/relaysidecar`) is a khatru relay with
a bbolt event store. It verifies ids and signatures, admits writes by a
persisted NIP-86 policy plus the intent-author set the daemon pushes, serves
public topics anonymously and protected topics only to NIP-42-authenticated,
admitted readers, applies NIP-09 deletions, sweeps retention by kind class,
and supports NIP-45 COUNT and NIP-77 negentropy. Details:
[relay sidecar](relay-sidecar.md).

### Daemon

`bahia-server` (`cmd/server`, composed in `internal/app`) runs:

- the **intent subscriber and processor** (`internal/controlplane`): parses
  `30900` `t=bahia-intent` events (and gift-wrapped intents for sensitive
  domains), deduplicates by `intent_id`, authorizes the signer through the
  TrustSet, dispatches to the domain handler, and publishes the bounded
  `30315` status ([intents and authority](architecture/intents-and-authority.md));
- the **registry and domain services** (`internal/service`): services,
  environments, builds, artifacts, deployment intents and runs, policies,
  secrets, notifications, organizations, LLM routes and releases, ML models,
  packages, backups, DNS, workers, virtualization, security scanning, SBOM;
- the **relay-first registry**: a mutation publishes its canonical `30900`
  record and waits for `nostr.publish_quorum` relays before the index row is
  committed;
- the **projector** (`internal/adapters/nostr/projector.go`): discovery
  (`11316`–`11320`, `30002`, `10002`, `10050`), cp-state records, audit
  facts and tombstones, with per-coordinate deduplication so an unchanged
  state is not re-signed;
- **reactors and supervisors**: the reconciler (`reconcile.interval`,
  default 60s), drift detection, managed-instance supervision and route
  canaries (`supervision`), hygiene, continuity failover, Hive-CI dispatch
  and result processing, Loom job tracking, SoulFactory provisioning, the
  operator assistant;
- the **local event store and publish outbox** (`nostr.local_store`): inbound
  deduplication and per-(relay, filter) cursors ([event lifecycle](architecture/event-lifecycle.md));
  every signed outbound event is durable there before its first relay
  attempt and tracked per relay `OK` ([outbox delivery](architecture/outbox-delivery.md));
- the **HTTP server** (`server.host`/`server.port`, default
  `127.0.0.1:8080`): health, readiness, metrics, the OCI registry, MCP and a
  small set of `/api/v1` routes ([HTTP reference](api.md));
- the **MCP server** (`internal/mcp`): tools that sign intents in-process
  with the caller's identity and read state from the local event store
  ([CLI and MCP](architecture/cli-and-mcp.md)).

### Persistence

| Data | Where | Role |
|---|---|---|
| Canonical records, status, audit, discovery | relays (sidecar and `nostr.service_relays`) | source of truth |
| Everything the daemon has seen or published | `nostr.local_store` (bbolt) | cursors, deduplication, store-read tools, supervision inputs |
| Pending outbound events | `nostr.local_store.outbox_path` (bbolt) | durable publish with per-relay acceptance; `bahia outbox` inspects it |
| Registry index | PostgreSQL (`db.*`) | queryable index of services, environments, builds, artifacts, intents, runs, policies, secrets, notifications, orgs, DNS, workers; `nostr_events` archives events best effort ([PostgreSQL event store lifecycle](architecture/postgres-event-store-lifecycle.md)) |
| Payments, security targets/runs/schedules/findings, operator allowlists, Hive-CI execution state | relays only (`service.PaymentService`, `CanonicalSecurityRepository`) | PostgreSQL, when present, is a rebuildable index |
| Blobs, logs, SBOM payloads | Blossom (`blossom.*`) | content-addressed storage |
| Container images | the built-in OCI registry (`oci.*`, manifests in PostgreSQL, blobs in Blossom), Harbor, or any configured registry | image distribution |
| Relay history | sidecar `data_dir/events.bolt` | replay and retention |

The daemon starts without PostgreSQL (`postgres cache unavailable; continuing
with relay-first reduced tier`): it serves health and readiness, discovery,
the relay-only families above, MCP store reads and ContextVM RPC. Families
whose index is PostgreSQL are unavailable until it is reachable; HTTP routes
backed by those repositories answer `503`. A `database-recovery` runner
reconnects when PostgreSQL returns.

### Operating modes

`mode` (`full`, `degraded`, `emergency`; default `full`) selects the readiness
relay quorum (`nostr.relay_quorum.{full,degraded,emergency}_min_healthy`). The
relay-first write path is active whenever `nostr.publish_enabled` is true (the
default) or the mode is `full`.

## Flows

### Bootstrap

The web's store-first design is described in
[web store-first](architecture/web-store-first.md).

1. A client starts from its seed: the web from `PUBLIC_BAHIA_BOOTSTRAP_RELAYS`
   and `PUBLIC_BAHIA_SERVICE_PUBKEYS` injected at container start; the CLI
   from `--relay`/`BAHIA_NOSTR_RELAYS` or bootstrap-relay discovery with
   trusted service pubkeys.
2. It reads `11316` and the `30002` relay sets from the trusted service
   pubkey, learns the browser and ContextVM relays, feature flags and
   advertised capabilities.
3. It REQs the canonical families it needs with `authors` + `#t`, processes
   until `EOSE`, answers the NIP-42 challenge for protected topics, and
   keeps the subscriptions live.

### Write

1. The client mints the entity id, builds the full desired state, signs the
   intent (gift-wrapping sensitive domains) and publishes it.
2. The daemon validates, deduplicates, authorizes, dispatches and publishes
   `30315` `accepted` or `rejected` with any daemon-authored output.
3. The handler drives the service layer; the relay-first registry publishes
   the canonical record and only then commits the index row.
4. The client sees the record in its subscription and renders it.

### Deployment

1. Build and artifact records exist (Hive-CI bridge or `artifact/register`).
2. A `deployment/create` intent is policy-evaluated and produces an intent
   record with a desired-state snapshot; `deployment/approve` is required
   when policy says so.
3. An approved intent creates a run. Native runs execute through the runtime
   lifecycle service (`building_desired_state`, `locking_environment`,
   `rendering`, `applying`, `observing`, `projecting`); Loom-backed runs
   submit a `5100` job and follow `30100`/`5101`.
4. Observation updates `service-state`; a failed apply is recorded as failure.
   Drift is detected by the reconciler and published as state; route
   canaries and managed-instance supervision keep probing.

### Encrypted RPC

1. The client encrypts a `25910` request to the service pubkey inside a
   `1059` (or `21059` when oversized) wrap and publishes it to the ContextVM
   relay set.
2. The daemon unwraps, verifies the inner signer, authorizes, replies with a
   `notifications/progress` ack, executes, and publishes the reply in a wrap
   of the same lifetime correlated by `e=<outer request>,reply`.

## Key design decisions

- **Relays are canonical; databases index.** Losing PostgreSQL loses no
  control-plane truth.
- **Intent-based mutations.** Request and execution are separate, which gives
  approvals, replay protection by `intent_id` and revision checks by
  `expected_updated_at`.
- **Signer-first identity.** Users, operators, agents and the daemon act
  through Nostr keys; allowlists and memberships are events; entity ids are
  author-minted ([entity identity](architecture/entity-identity.md)).
- **Confidential state is encrypted, not hidden.** Org-scoped records use the
  org content key; fleet-scoped records the fleet OCK; relays hold
  ciphertext ([confidential state](architecture/confidential-state.md)).
- **Durable publication.** Service-authored events are persisted before relay
  delivery and retried until every write relay accepts or reaches a terminal
  state; an abandoned delivery is surfaced on readiness, never silently
  dropped.
- **The operator assistant runs as governed execution** with encrypted
  checkpoints ([assistant execution](architecture/assistant-execution.md)).
- **HTTP is for what HTTP is good at.** Probes, metrics, the OCI
  distribution API, browser blob downloads, log streaming and a handful of
  authenticated operator routes.
