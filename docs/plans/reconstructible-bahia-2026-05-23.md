# Reconstructible Bahia: Relay-Canonical Architecture Plan

## Goal

Evolve Bahia from a DB-coupled management server into a reconstructible, relay-canonical orchestration fabric where Nostr relays hold canonical state, Postgres becomes a disposable cache, and fresh Bahia instances can cold-start by replaying the event graph — making Bahia itself restartable, replaceable infrastructure rather than a sacred cluster brain.

## Background

Startup hard-fails without Postgres (`app.go:88-91`). All 17+ repositories are Pg-backed (`app.go:99-134`). No degraded/in-memory startup path exists. The reactor subscribes from `nostr.Now()` (`reactor.go:418-430`), missing events while Bahia is down. Health endpoints always return 200 with no real checks (`router.go:117-124`). No application "mode" concept exists in config. The continuity definition store, heartbeat monitor, continuity graph, and failover trigger engine are implemented but not wired in `app.go`. Bahia has no self-identity events.

However, key infrastructure for reconstructibility already exists:
- **Repository interface pattern** — cleanly separated from Pg implementations (`interfaces.go`), swap-ready
- **Cursor-based replay** — `subscriber.subscriptionSince()` (`subscriber.go:416-440`) with persistence, dedup, 1-second overlap
- **Snapshot rebuild** — `projector.RepublishSnapshot()` (`projector.go:364-388`) rebuilds from source state at startup and periodically
- **Relay pool EOSE** — `SubscribeAllWithEOSE()` with merged relay EOSE handling (`relay_pool.go:441-573`)
- **Continuity stores** — idempotent in-memory stores with timestamp ordering (`continuity_definition_store.go`, `heartbeat_monitor.go`)
- **BackgroundManager** — runner orchestration with conditional startup gates (`background.go`)
- **Relay health primitives** — existing `relay_health.go` with health stats, not yet surfaced to readiness

For full evidence with file:line refs, see `prompt-exports/oracle-plan-2026-05-23-140723-relay-canonical-plan-dffe.md`.

## Approach

### A. Mode Model, Tier Model, and Subsystem Gating

Implement as a **mode overlay** on existing `Enabled` flags — don't replace them.

**Config** (`internal/config/config.go`): Add top-level `Mode` field (`full|degraded|emergency`), default `full`. Validate enum and minimum relay availability. Don't reject incompatible feature flags — suppress at runtime and report.

**Mode policy** (new: `internal/app/mode_policy.go`): Derives requested tier from configured mode, computes active tier from available dependencies at boot.

**Tier model** — four closed tiers:

| Tier | Contains | Required in |
|------|----------|-------------|
| `tier0` | Relay connectivity, bootstrap/replay, self-status, liveness/readiness HTTP | All modes |
| `tier1` | Continuity, worker liveness/catalog, heartbeats, failover, DNS continuity, minimal backup restore metadata | Emergency+ |
| `tier2` | Core registry/control-plane cache (services, envs, builds, artifacts, intents, runs, policies, state) | Degraded+ |
| `tier3` | Extended subsystems (LLM, ML, packages, OCI, HiveCI, assistant, tool provisioning) | Full only |

Mode → tier mapping: `full` → tier3, `degraded` → tier2, `emergency` → tier1.

If active tier < requested tier: `/ready` returns 503, self-readiness event reports degraded, routes above active tier return 503, runners above active tier don't start.

### B. Shared Replay Catalog, Cold Bootstrap, and Warm Cursor Replay

This is the load-bearing change.

**Kind catalog** (new: `internal/adapters/nostr/catalog.go`): Single source of truth for replay groups, snapshot kinds, live kinds, and catalog version. Replaces scattered kind lists in reactor, subscriber, and projector. Each `ReplayGroup` has a name, kinds, tier, snapshot/live flag, and required flag.

**New self-identity kinds** — use Bahia's existing Nostr keypair:
- `31410` — Bahia identity definition (version, catalog version, mode, relay policy)
- `31411` — Bahia replay checkpoint (per-group cursors, catalog version)
- `30360` — Bahia readiness status (phase, tier, checks)

**Replay cursor planner** (new: `internal/adapters/nostr/replay_cursor.go`): Extract `subscriber.subscriptionSince()` into a reusable planner. Computes `Since` from: in-memory lastSeen → local `NostrEventRepository` cursor → relay-authored `31411` checkpoint. Takes the newest, subtracts 1 second.

**Bootstrapper** (new: `internal/adapters/nostr/bootstrapper.go`): Cold-start coordinator. Bootstrap algorithm:
1. Compute `bootstrapStartedAt`, publish `31410` identity
2. Open snapshot subscriptions for groups at/below requested tier
3. Decode events → apply to in-memory stores + optional PG cache via `RelayProjectionCache`
4. Wait for EOSE on required snapshot groups (with per-group timeout — if EOSE never arrives due to relay behavior, fall back to timeout-based completion)
5. Open live catch-up subscriptions using shared cursor planner
6. When required live groups reach EOSE (or timeout), mark tier ready
7. Publish `31411` checkpoint + `30360` readiness
8. Start dependent background runners

**Failure semantics**: If required snapshot groups for the requested tier fail (timeout, relay disconnect), the bootstrapper attempts to promote a lower tier. If no tier can be established, it enters `BootstrapPhaseFailed` — HTTP remains up, `/ready` returns 503, and the bootstrapper retries periodically. Partial EOSE (some groups complete, others don't) can promote to the highest tier whose required groups are satisfied.

Duplicates suppressed by `NostrEventRepository.Record` + in-memory dedup. Out-of-order replaceables dropped by projection meta guard. Disconnects handled by replay overlap + checkpoint recovery.

### C. Disposable Postgres Projection Cache

Postgres remains useful but only as a **rebuildable local cache**:
- Boot with DB unavailable must be possible up to tier1
- Boot with empty DB must be recoverable from relays
- DB tables updated from canonical relay events during bootstrap and steady state
- DB loss does not lose control-plane truth

**In-memory NostrEventRepository** (new: `internal/repository/in_memory_nostr_event.go`): Implements existing `repository.NostrEventRepository` with mutex-protected map. Used when DB unavailable and for tests. No interface change needed.

**Projection ordering metadata** (new: `internal/repository/pg_relay_projection_meta.go` + migration): Generic `RelayProjectionMetaRepository` with stream/entityKey/updatedAt/sourceEventID/tombstoned. Avoids adding source_event_id columns to every cache table.

**Projection cache applier** (new: `internal/service/relay_projection_cache.go`): Idempotent cache mutation from decoded relay read models into existing repositories. Derive stream+entityKey from kind+d-tag, compare timestamps, apply create/update/delete using existing repos, persist meta after successful apply. Reused for bootstrap and live replay.

**`DecodedProjectionEvent` design** — this is the load-bearing type at the boundary between bootstrapper, subscriber, and all cache writes. It should be a tagged-union/discriminated type with per-family variant structs. Each family (service, environment, worker, continuity, etc.) registers a decode function in the kind catalog and a corresponding cache applier method. The design must accommodate both single-entity replaceables (kind+d-tag → one entity) and batch replaceables. Getting this type shape right is critical — Items 10, 11, and 15 all depend on it.

**Cache family sequencing**: tier1 first (workers, continuity defs/status, heartbeats), then tier2 core (service/env/build/artifact/intent/run/state/policy), then tier3 extended (backup, DNS, LLM, ML, packages).

### D. Write-Path Inversion: Relays Become Authoritative

Bootstrap alone isn't enough — if mutations remain DB-first, relays stay secondary.

**Canonical write rule** (per family, migrated incrementally):
1. Validate command
2. Produce canonical replaceable/result event payload
3. Publish to relays successfully
4. Apply locally to in-memory/PG caches from same canonical payload
5. Report success

If relay publish fails, the mutation fails. This preserves relay authority.

**Sequencing**: continuity kinds first (already largely relay-native) → core registry read models → backup/DNS → LLM/ML/packages → flip projector source from repo-backed to canonical stores.

**Extension points**: Extract shared encode/decode from projector-private helpers into reusable codec functions under `internal/adapters/nostr/`.

### E. Bahia Self-Identity, Checkpoint, and Readiness Events

_Logically part of Section B — the self-identity publisher is the bootstrapper's output channel._

**Bahia status projector** (new: `internal/service/bahia_status_projector.go`): Similar role to `continuity_status_projector.go` but system-scoped. Owns dedup fingerprints, only republishes on state change. Publishes `31410` (identity), `31411` (checkpoints), `30360` (readiness).

**Serialization**: Add encode/decode for 31410/31411/30360 to `internal/adapters/nostr/continuity_serialization.go`.

### F. Real Health/Readiness and Background Runner State

**Background status tracking** (`internal/app/background.go`): Extend `BackgroundManager` with per-runner `RunnerStatus` (name, running, required, tier, timestamps, last error). Don't change `BackgroundRunner` interface.

**Health manager** (new: `internal/app/health.go`): `HealthProvider` with `Liveness()` / `Readiness()` returning `HealthSnapshot` (status, mode, tiers, checks, bootstrap summary, relay summary, cache summary, runner status).

**Check set**: `relay_quorum`, `bootstrap_snapshot`, `live_catchup`, `continuity_runtime`, `worker_catalog`, `core_projection_cache`, `extended_projection_cache`, `background_runners`, `postgres_cache` (informational).

**Readiness semantics**: `/health` stays 200 unless shutting down (degraded status in body). `/ready` returns 200 only when required bootstrap groups caught up, relay quorum satisfied, required runners running, required cache families available.

**Relay health integration**: Embed `RelayHealthTracker` in `RelayPool`, expose `HealthSnapshot()`, `ConnectedCount()`, `HealthyCount()`. Relay quorum thresholds should be configurable, not hardcoded — provide sensible defaults (recommend 3 relays, require 2 for full, 1 for emergency) but let operators tune them.

### G. App Composition and Startup Lifecycle

**Startup reorder** (`internal/app/app.go`):
1. Logger
2. Derive `ModePolicy` from config
3. Connect relay pools
4. Create in-memory tier0/tier1 stores
5. Try DB connect/migrate — success: create PG repos; failure: tier1-only, cache unavailable
6. Create `NostrEventRepository` — PG-backed if available, in-memory otherwise
7. Create bootstrapper + health provider + Bahia status projector
8. Start HTTP server immediately (visible during bootstrap)
9. Start bootstrapper
10. Wait for bootstrap readiness for highest viable tier
11. Start background runners allowed by active tier
12. Publish readiness change

**Wire existing continuity**: definition store, heartbeat monitor, continuity graph, failover trigger engine, recipe executor. Pass `ContinuityGraph` to router deps (fixes current nil wiring).

**Reactor/subscriber refactor**: Reactor owns control-plane request ingestion + request event audit recording. Subscriber narrows to non-reactor operational streams (worker ads, Loom, assistant). Both consume the shared kind catalog instead of local kind lists. Removes duplicate subscriptions.

### H. Router and HTTP Surface

**Health endpoints**: Replace static 200 responses with `HealthProvider` calls. Response shape: `{status, version, mode, requested_tier, active_tier, ready, checks}`.

**Route gating by tier**: Mount all routes but guard disabled groups with mode/tier middleware returning 503 with explanatory body — don't make them 404.
- tier0: `/health`, `/ready`, `/metrics`
- tier1: `/api/continuity/*`, worker reads, minimal backup/DNS status
- tier2: core `/api/v1` service/environment/build/artifact/deployment/state/policy
- tier3: LLM, ML, packages, OCI, assistant, HiveCI, tool provisioning

## Work Items

### Item 1 — Config mode and app mode policy
**Goal:** Add the `Mode` config field and the `ModePolicy` runtime abstraction that gates subsystems by tier.
**Done when:** Config accepts `mode: full|degraded|emergency`, validates it, and `ModePolicy` correctly maps modes to tiers with `AllowsTier`/`RouteEnabled`/`RunnerEnabled` helpers. Unit tests cover validation and tier derivation.
**Key files:** `internal/config/config.go`, `internal/app/mode_policy.go` (new)
**Dependencies:** None — lands first.
**Size:** Small

### Item 2 — Health infrastructure and background runner status
**Goal:** Add `HealthProvider`, `HealthSnapshot`, and per-runner status tracking so real readiness can be computed.
**Done when:** `BackgroundManager` tracks per-runner status snapshots. `HealthProvider` assembles liveness/readiness from relay pool, bootstrap state, runner status, and cache availability. Unit tests cover check evaluation.
**Key files:** `internal/app/health.go` (new), `internal/app/background.go`
**Dependencies:** Item 1 (mode policy types)
**Size:** Medium

### Item 3 — Real health/readiness endpoints
**Goal:** Replace static 200 `/health` and `/ready` responses with `HealthProvider`-driven responses.
**Done when:** `/health` returns rich JSON with mode/tier/checks. `/ready` returns 503 when required checks fail. Router/handler tests verify both paths.
**Key files:** `internal/api/router/router.go`, `internal/api/dto/health.go`
**Dependencies:** Item 2
**Size:** Small

### Item 4 — Kind catalog
**Goal:** Single source of truth for replay groups, kind lists, catalog versioning — unblocking all downstream replay work.
**Done when:** `KindCatalog` defines all snapshot/live groups with tier assignments. `DecodedProjectionEvent` tagged-union type is defined with per-family decode registration. Unit tests verify catalog coverage.
**Key files:** `internal/adapters/nostr/catalog.go` (new)
**Dependencies:** None — pure data definition, lands early.
**Size:** Small

### Item 5 — In-memory NostrEventRepository and shared replay cursor planner
**Goal:** Enable DB-free event audit/cursor storage and extract the reusable replay cursor computation from `subscriber.go`.
**Done when:** `InMemoryNostrEventRepository` implements `NostrEventRepository` with mutex-protected map. `ReplayCursorPlanner` computes `Since` timestamps from in-memory, DB, and checkpoint sources. Subscriber refactored to use planner. Unit tests cover cursor computation and in-memory repo.
**Key files:** `internal/repository/in_memory_nostr_event.go` (new), `internal/adapters/nostr/replay_cursor.go` (new), `internal/adapters/nostr/subscriber.go`
**Dependencies:** None (parallel with Items 1-4)
**Size:** Medium

### Item 6 — Reactor warm cursor replay
**Goal:** Replace reactor's fixed `nostr.Now()` startup cursor with shared cursor planner, eliminating the disconnect gap.
**Done when:** Reactor computes filters from persisted/in-memory cursors using catalog groups. Reconnects use latest cursor state instead of original startup timestamp. In-memory per-group lastSeen tracking works. Integration tests verify reconnect gap replay.
**Key files:** `internal/controlplane/reactor.go`
**Dependencies:** Items 4-5 (catalog, replay cursor planner)
**Size:** Medium

### Item 7 — Relay health tracking
**Goal:** Surface relay connectivity state for readiness evaluation.
**Done when:** `RelayPool` exposes `HealthSnapshot()`, `ConnectedCount()`, `HealthyCount()`. Connection/publish/subscribe paths maintain tracker state. Health provider reads relay stats. Unit tests cover quorum evaluation.
**Key files:** `internal/adapters/nostr/relay_pool.go`, `internal/adapters/nostr/relay_health.go`
**Dependencies:** Item 2 (health provider consumes relay stats)
**Size:** Small

### Item 8 — Wire existing continuity runtime ⚠️ CRITICAL PATH
**Goal:** Connect the already-implemented continuity components in `app.go` so the continuity fabric is fully operational. This is a gate for DB-optional startup (Item 12) since tier1 requires working continuity.
**Done when:** `app.go` creates continuity definition store, heartbeat monitor, continuity graph, failover trigger engine. `ContinuityGraph` passed to router deps. `/api/continuity/topology` and `/api/continuity/simulate` return real data. Integration tests verify.
**Key files:** `internal/app/app.go`
**Dependencies:** None — start early, as it gates Item 12.
**Size:** Small

### Item 9 — Self-identity, checkpoint, and readiness event codecs + publisher
**Goal:** Bahia publishes self-describing events to relays for observability and checkpoint-based replay recovery.
**Done when:** Encode/decode for kinds 31410, 31411, 30360 exists. `BahiaStatusProjector` publishes identity at startup, checkpoints after EOSE/periodically, readiness on state change. Round-trip serialization tests pass.
**Key files:** `internal/adapters/nostr/continuity_serialization.go`, `internal/service/bahia_status_projector.go` (new)
**Dependencies:** Items 2, 4 (health provider state, catalog version)
**Size:** Medium

### Item 10 — Projection meta persistence and generic cache applier
**Goal:** Enable idempotent relay-event-to-DB-cache mutation for bootstrap and steady-state replay.
**Done when:** `RelayProjectionMetaRepository` with PG implementation exists. Migration adds `relay_projection_meta` table. `RelayProjectionCache` applies decoded events (via `DecodedProjectionEvent` from Item 4) to existing repos with last-write-wins ordering. Out-of-order/tombstone tests pass. Tier1+tier2 core families covered.
**Key files:** `internal/repository/interfaces.go`, `internal/repository/pg_relay_projection_meta.go` (new), `internal/db/migrations/` (new), `internal/service/relay_projection_cache.go` (new)
**Dependencies:** Item 4 (kind catalog defines `DecodedProjectionEvent`) + existing repo interfaces
**Size:** Large

### Item 11 — Bootstrapper: cold-start relay replay
**Goal:** Fresh Bahia instances can reconstruct operational state by replaying the event graph from relays.
**Done when:** Bootstrapper runs snapshot + live catch-up phases. Decoded events applied to in-memory stores and cache applier. EOSE gates tier readiness (with timeout fallback). HTTP is live during bootstrap. Phase/progress observable via self-status events and `/health`. Bootstrap failure enters retry mode, partial completion promotes lower tier. Cold-start integration tests with mock relay streams pass.
**Key files:** `internal/adapters/nostr/bootstrapper.go` (new), `internal/app/app.go`
**Dependencies:** Items 5, 9, 10 (cursor planner, self-status publisher, cache applier)
**Size:** Large

### Item 12 — DB-optional app startup
**Goal:** Bahia starts relay-first; Postgres is optional. Emergency/degraded modes work without DB.
**Done when:** `app.go` reorders startup to relay-first. DB failure doesn't abort startup — active tier drops to tier1. In-memory `NostrEventRepository` used as fallback. Runners gated by active tier. Integration tests verify startup with DB absent in emergency and degraded modes.
**Key files:** `internal/app/app.go`
**Dependencies:** Items 1-3, 5, 8, 11 (mode policy, health, health endpoints, in-memory repos, continuity wiring, bootstrapper)
**Size:** Large

### Item 13 — Route gating by active tier
**Goal:** Routes above active tier return 503 with explanatory body instead of silently failing or 404-ing.
**Done when:** Router mounts all route groups with tier-aware middleware. Disabled groups return `{error, mode, active_tier, required_tier}`. Route gating tests verify per mode/tier.
**Key files:** `internal/api/router/router.go`
**Dependencies:** Item 12 (active tier available)
**Size:** Small

### Item 14 — Shrink subscriber scope; move command audit to reactor
**Goal:** Eliminate duplicate control-plane subscriptions and give the reactor direct ownership of its replay cursor.
**Done when:** Subscriber only handles non-reactor streams (worker ads, Loom, assistant). Reactor records accepted events to `NostrEventRepository`. No duplicate kind subscriptions. Integration tests verify audit and handling.
**Key files:** `internal/adapters/nostr/subscriber.go`, `internal/controlplane/reactor.go`
**Dependencies:** Items 4, 6 (catalog, reactor cursor replay)
**Size:** Medium

### Item 15 — Canonical payload audit and full tier2 cache bootstrap
**Goal:** Audit event payload round-trip fidelity, extend lossy payloads, then complete cache appliers so `full`/`degraded` modes can rebuild from relays.
**Done when:** Every core registry kind validated for `repo model → encode → decode → compare` round-trip fidelity. Lossy payloads extended before building appliers. Service/env/build/artifact/intent/run/state/policy cache appliers complete.
**Key files:** `internal/service/relay_projection_cache.go`, `internal/adapters/nostr/projector.go` (codec extraction)
**Dependencies:** Item 10
**Size:** Large — ⚠️ **sizing risk**: if many kinds have lossy payloads, this item expands to include canonical payload redesign. A quick fidelity audit early (can be done in parallel with Items 1-8) would de-risk this.

### Item 16 — Core write-path inversion
**Goal:** Core registry mutations become relay-first — relay publish success required, DB writes are secondary cache application. **This is the biggest architectural blocker**: if a DB write succeeds and relay publish fails, the DB remains "truth" — which breaks relay-canonical semantics.
**Done when:** `RegistryService` mutation methods publish to relay before local cache. Relay publish failure = command failure. End-to-end mutation→relay→cache reconstruction tests pass.
**Key files:** Files implementing `service.NewRegistryService` — **validation required**: locate exact filenames by searching for constructor names referenced from `internal/app/app.go`.
**Dependencies:** Item 15
**Size:** Large

### Item 17 — Extended family migration (tier3)
**Goal:** LLM, ML, packages, backup, DNS families migrated to relay-canonical pattern.
**Done when:** Each family independently shippable. Cache appliers + write-path inversion complete per family.
**Key files:** LLM/ML/package/backup/DNS service files — **validation required**: search for `NewLLMRegistryService`, backup/package mutation constructors in `app.go`.
**Dependencies:** Item 16
**Size:** Large

### Item 18 — Flip projector authority direction
**Goal:** Projector reads from canonical stores/caches instead of authoritative DB state.
**Done when:** `RepublishSnapshot` sources from canonical in-memory/cache stores. Snapshot repair works after DB loss.
**Key files:** `internal/adapters/nostr/projector.go`
**Dependencies:** Item 17
**Size:** Medium

### Item 19 — Final readiness hardening
**Goal:** `/ready` depends only on relay-canonical state and requested tier, not DB authority. Comprehensive regression coverage.
**Done when:** PG is treated as cache health only. Regression tests pass for: cold-start with empty DB, warm restart with checkpoint only, emergency boot with DB absent, reconnect gap replay, route gating across modes.
**Key files:** `internal/app/health.go`, `internal/api/router/router.go`, integration test files
**Dependencies:** Items 12-18
**Size:** Medium

## Risks and Migration

### Mixed-Version Event-Kind Risk
Continue accepting legacy worker read-model kinds (31974, 31991-31993) during mixed-version window. Don't publish new legacy kinds. Disambiguate 31974 by tag/content shape. Prefer canonical 32000-32003 in all new code.

### Boot Sequencing Risk
Don't roll out "DB optional" startup until: bootstrapper exists, tier1 continuity/worker replay is wired, health/readiness reflect active tier correctly.

### Write-Path Inversion Risk
Unavoidable intermediate dual-authority period. Keep it short and explicit: phase 1 = relay-rebuildable reads, phase 2 = canonical-first writes for core families, phase 3 = projector source flip.

### Rollback Behavior
- `mode` config is additive; old binaries ignore unknown fields
- Self kinds are additive; old binaries ignore them
- `relay_projection_meta` table is additive and safe to leave behind
- Rolling back after write-path inversion restores DB-first semantics; don't do that after relays become sole authority for a migrated family without a planned downgrade window

## Open Questions

1. ~~Should bootstrap be a separate binary or integrated startup?~~ **Resolved: integrated startup path.** HTTP starts immediately, bootstrap runs in background, readiness gated by tier completion.
2. ~~Which Nostr keypair for self-identity?~~ **Resolved: reuse existing Bahia Nostr keypair** (`cfg.Nostr.PrivateKey`). Avoids second trust root.
3. ~~Relay topology specifics?~~ **Resolved at architecture level:** configurable quorum thresholds with sensible defaults. Concrete placement is operator decision.
4. **Canonical payload fidelity audit needed.** For the write-path inversion (Items 15-17), exact files implementing `service.NewRegistryService`, `service.NewLLMRegistryService`, and backup/package mutation services need to be located. More critically, a `repo model → encode → decode → compare` round-trip audit per kind family should be done early to assess how many payloads are lossy — this directly sizes Items 15-17.
5. **Mock relay test harness.** Items 6, 11, 12, and 15 all require relay-level integration tests. Verify whether a mock relay infrastructure exists; if not, building one is a prerequisite.
6. **EOSE reliability.** Some relay implementations omit EOSE for empty filter results. The bootstrapper needs timeout-based fallback; verify behavior of Bahia's configured relay set.

## References

- `docs/designs/continuity-fabric.md` — prior continuity fabric design (mentions state replication as gap, Phase 3 roadmap)
- `internal/adapters/nostr/subscriber.go` — existing cursor-based replay pattern to extract
- `internal/adapters/nostr/projector.go` — existing snapshot rebuild pattern (authority direction inverts)
- `internal/repository/interfaces.go` — swappable repository interface surface
- `internal/adapters/nostr/relay_health.go` — existing relay health primitives to surface
