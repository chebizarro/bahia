# Desired-State Runtime Architecture: Plan

## Goal
Move Bahia from environment-level, monolithic Compose operations toward Nostr-native structured service desired state that can render to Compose and Docker Engine targets first, while keeping Kubernetes and Podman as later renderer/adapter targets.

## Background
- User decision: extend Bahia’s existing Nostr event/read-model families rather than introduce a disconnected desired-state model. The plan should use current request/status/result and parameterized replaceable read-model conventions as the migration path.
- User decision: plan both Compose phases: first synthesize a full Compose project compatible with the existing `/srv/data/bahia-managed/docker-compose.yml` operational shape, then support per-service fragments where safe.
- User decision: include Compose and Docker Engine in the first architecture slice so the renderer/adapter seam is proven early, not deferred until after Compose-only work.
- Compose is currently modeled around a project directory containing `docker-compose.yml`; the adapter invokes Docker Compose with `--project-directory` and uses that directory as the command working directory (`internal/adapters/runtime/compose.go:22-31`, `internal/adapters/runtime/compose.go:232-252`).
- Runtime creation already requires explicit target configuration: Compose targets require `compose_dir`, Docker targets use endpoint/Docker host config, and worker targets are converted through the same factory path (`internal/adapters/runtime/factory.go:48-58`, `internal/adapters/runtime/factory.go:76-137`).
- Runtime resolution overlays global flat config, `runtime.default`, `runtime.environments[env.Name]`, environment `runtime_config`, and service runtime type checks before constructing/caching an adapter (`internal/adapters/runtime/resolver.go:83-205`).
- The shared runtime deploy contract is `runtime.DeployOptions`, carrying environment variables, labels, ports, volumes, restart policy, command, entrypoint, working directory, network mode, and pull behavior (`internal/adapters/runtime/docker.go:57`).
- Docker already implements direct Engine-style deploy/observe/restart/stop/log operations; `Deploy` builds container create payloads, removes existing containers, optionally pulls images, creates and starts containers (`internal/adapters/runtime/docker.go:294-404`).
- Compose deploy is currently service-image substitution plus `compose pull` and `compose up -d --force-recreate --no-deps <service>` (`internal/adapters/runtime/compose.go:122-156`), which is the pragmatic bootstrap behavior this plan replaces with rendered desired state.
- Direct runtime lifecycle actions flow through `RuntimeLifecycleService`: resolve service/env/runtime, build deploy options from adopted service runtime config, merge secrets, add Bahia labels, call the runtime adapter, observe, record observation, and publish runtime events (`internal/service/runtime_lifecycle.go:80-343`).
- Adopted workload runtime configuration already captures many desired-state fields: target/container/source runtime/host alias/endpoint ref/environment/ports/volumes/labels/Compose metadata (`internal/domain/models.go:133-158`).
- Environment service state already expresses desired-vs-observed deployment state with desired artifact/intent/run, runtime observation, drift status, and reconciliation timestamp (`internal/domain/models.go:233-303`; `internal/domain/models.go:90-97`).
- Persistence exists for services, environments, deployment intents, runtime observations, and environment service state (`internal/db/migrations/000001_init.up.sql:6-143`), with service runtime config stored as JSONB (`internal/db/migrations/000017_service_runtime_config.up.sql:1-3`).
- Control-plane event conventions are established: deploy request/status/result kinds (`5961`, `6961`, `7961`), replaceable service state (`31961`), deployment intent registry (`31967`), and deployment run registry (`31968`) (`internal/adapters/nostr/catalog.go:33-38`, `internal/controlplane/reactor.go:31-132`).
- The control-plane reactor validates inbound events, dedupes by event ID, audits, handles AUTH/CLOSED/EOSE/reconnect, and dispatches by kind (`internal/controlplane/reactor.go:443-607`). Any desired-state command flow must preserve those Nostr-first semantics.
- `KindServiceState` is published as a parameterized replaceable event with `d=<service_id>:<environment_id>` and tags for service, environment, artifact, intent, run, deletion, and drift status (`internal/adapters/nostr/projector.go:2793-2840`; decoder at `internal/adapters/nostr/catalog.go:543-575`).
- Deployment requests are published by `ServiceCommandPublisher.PublishDeployRequest` with service/environment/artifact tags and correlation metadata for status/result kinds (`internal/controlplane/service_command_publisher.go:44-101`).
- Worker/Loom execution is already event-driven: job requests are Nostr events, job status/result subscriptions handle EOSE, CLOSED, AUTH retry, validation, dedupe, and terminal result handling (`internal/adapters/loom/client.go:150-379`; `internal/workflow/coordinator.go:102-389`).
- Runtime endpoints are intentionally server-managed aliases (`runtime.endpoints.<ref>` / `endpoint_ref`); docs and DTOs discourage exposing raw Docker host material in public adoption/control-plane paths (`docs/control-planes.md:419-465`; `docs/adoption-production-rollout.md:77-118`; `internal/api/dto/requests.go:275-289`).
- Prior art: runtime-aware provisioning plans avoided REST as the external control plane and required event-native runtime-control adapters (`docs/plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md:121-136`; current adapter contract at `internal/soulfactory/runtime_adapter.go:39-56`).
- Prior art: resource-pressure planning kept Loom worker advertisements and Bahia-derived pressure/service state rather than a platform rewrite, and noted fragmentation across placement/reconciliation paths (`docs/plans/resource-pressure-orchestration-2026-05-24.md:14-66`).
- External reference: Docker Compose `config`/`convert` can render a canonical Compose model after merging files, variables, and short syntax; project naming precedence and canonical Compose labels matter for generated output. See Docker docs: https://docs.docker.com/reference/cli/docker/compose/config/ and https://docs.docker.com/reference/compose-file/version-and-name/.
- External reference: Docker Engine API is REST over the daemon transport; current docs show API v1.54 for Engine 29.5.x with backward compatibility and version negotiation concerns. See https://docs.docker.com/reference/api/engine/.

## Approach

Bahia should adopt an environment-scoped, JSONB-backed desired runtime state model that feeds both full-project Docker Compose rendering and direct Docker Engine apply through the existing runtime resolver, factory, and lifecycle seams. The architecture should preserve the current Nostr request/status/result/read-model families (`5961`/`5963`, `6961`/`6963`, `7961`/`7962`, `31961`/`31967`/`31968`) and keep runtime endpoint aliases server-managed.

This is a targeted runtime-subsystem refactor, not a platform rewrite. Keep the reactor, authorization, event validation, deployment intent/run/state ownership, endpoint aliasing, and replaceable read models. Replace the current container-centric deploy assembly with a typed desired-state builder, persisted snapshots and hashes, environment-level apply locking, full Compose project synthesis in phase 1, Docker Engine desired-state apply in phase 1, and fragment optimization only after full-project correctness is proven.

The current system has four blockers this plan resolves:

1. `runtime.DeployOptions` is container-centric; it is useful for direct create/start operations but not enough for environment revisioning, full-project Compose synthesis, or fragment eligibility.
2. Compose currently mutates an existing project with image env substitution and service-scoped `up`, which cannot be Bahia’s source of truth for a rendered desired project.
3. Bahia persists desired/observed state concepts, but not a canonical desired runtime snapshot/hash that can be rendered, projected, compared, and hydrated for sibling services.
4. Full-project Compose requires one writer per environment; current service-scoped operations do not prove environment-level serialization.

### Current-state constraints

- External protocol: no new public control-plane family. Existing Nostr request/status/result events remain the command surface; progress and terminal results continue through correlated events, and projected truth remains in replaceable read models.
- Runtime resolution: the current config overlay path remains the runtime targeting authority, including `runtime.default`, environment overrides, service runtime type checks, and `endpoint_ref` alias resolution.
- Compose: phase 1 keeps the environment-level project shape and synthesizes the full project for a Bahia-owned `compose_dir`. It does not merge arbitrary operator-authored Compose YAML.
- Docker Engine: phase 1 proves the same desired-state contract against direct Engine operations so the renderer/adapter seam is real from the beginning.
- Kubernetes/Podman: Podman should reuse the Docker-compatible path where possible; Kubernetes remains compatible through the existing adapter path until a later slice.

### Desired-state domain and persistence

Introduce typed desired-state structs under the domain/runtime layer. The contract should carry enough information to render service desired state for Compose and Docker: schema version, environment/runtime target metadata, target service, revision hash, service/artifact IDs, stable service key, image reference, command/entrypoint/working directory, env, ports, volumes, labels, healthcheck, dependency policy, network policy, restart/pull policy, desired hash, and renderer-specific extension data. Let the implementation choose exact Go field names, but keep the seam typed rather than unstructured maps at call sites.

Use `Service.RuntimeTargetName()` as the initial stable runtime service key source (`internal/domain/models.go:134-141`), because it already prefers adopted target names and falls back to `Service.Name`. The builder must normalize that value for Compose service names, Docker names, and filesystem paths; when normalization collides or is lossy, suffix a short service ID fragment and persist the resolved key in desired state.

Define canonical serialization before any hash-dependent work lands. Hash inputs should be deterministic JSON with sorted object keys, stable list ordering, explicit null/empty handling, schema version included, and volatile fields excluded. The same serializer should produce service desired hashes, environment revision hashes, and normalized observation hashes; tests should lock golden hash fixtures so drift, no-op apply, and fragment eligibility cannot diverge by renderer.

Renderer extension payloads should be typed contracts even if stored as JSONB. Compose extensions should cover Compose-only dependency rendering, service-level healthcheck rendering, generated env-file metadata, and project/network/volume declarations needed for full-project synthesis. Docker extensions should cover Engine-specific host config, networking config, volume options, and healthcheck fields that are not portable. Kubernetes/Podman data should be reserved only as extension namespaces, not implemented behavior, in this slice.

Persist desired-state data JSONB-first with explicit hash columns. This resolves the normalization question in favor of flexibility for phase 1 while keeping cheap comparison/indexing available through hashes. Additive persistence should cover:

- deployment-intent desired-state snapshot and hash
- deployment-run runtime apply metadata
- environment-service-state latest desired runtime state and hash
- runtime-observation normalized observed state and hash

Exact column names belong to the implementation, but the persistence owners are known: deployment intent/run state is in `internal/repository/pg_deployment.go`, service state in `internal/repository/pg_state.go`, and runtime observations in `internal/repository/pg_observation.go`.

Do not persist resolved secret plaintext. Persist literal env values only when non-secret; secret-backed env entries should retain secret refs and redaction/source metadata. Resolved secret values may exist only in memory during apply, in runtime API payloads, or in generated Compose env files where required.

### Orchestration and locking

Keep `RuntimeLifecycleService` as the runtime apply owner. Deploy execution should converge on one internal helper used by deployment requests, direct runtime `action=deploy`, and rollback-to-artifact once an artifact is selected. `restart` and `stop` remain operational lifecycle actions, not desired-state mutations.

Full-project Compose rendering requires environment-level serialization. Add a DB-backed advisory lock keyed by `environment_id`, injected into `RuntimeLifecycleService`. Do not hold a DB transaction across CLI/API runtime calls. Use short transactions for persistence, hold the advisory lock across build/render/apply/observe, then persist the outcome and publish correlated Nostr status/result events after commit.

Failure behavior should be explicit: release locks with `defer`, persist failed runs, attempt best-effort observation, keep the requested desired snapshot available for drift comparison, and publish terminal failure through the existing result kind.

### Desired-state builder and legacy hydration

Add a builder that maps service + environment + artifact + runtime config + secrets into a canonical desired service spec. It should inject stable Bahia labels (`bahia.managed`, service/environment/artifact/intent/run IDs, desired hash), split literal env from secret refs, and carry renderer-specific details in typed extension payloads.

Compose needs all managed services in an environment. Add environment-plan assembly that loads active environment-service-state rows, replaces the target service with the freshly built spec, hydrates sibling specs from stored desired state when present, reconstructs missing sibling specs from current persisted registry/runtime/artifact state, drops deleted/tombstoned services, sorts deterministically, and computes the environment revision hash. Opportunistically persist hydrated legacy sibling specs so the first full-project render does not omit existing managed services.

### Runtime adapter capability

Add an additive desired-state capability seam, leaving the existing runtime interface in place during migration. Compose and Docker must implement the new capability in phase 1; Podman can reuse the Docker-compatible path where feasible; Kubernetes stays on the legacy deploy path until a later slice.

The new apply result should report renderer, revision hash, target resource IDs/names, observation hints, and non-fatal warnings. `RuntimeLifecycleService` should require this capability only for desired-state-managed Compose/Docker/Podman deploys and use the legacy path only where explicitly out of scope.

### Compose phase 1: synthesized full project

Split Compose rendering from CLI execution. The renderer should deterministically write a Bahia-owned project shape:

- `<compose_dir>/docker-compose.yml`
- `<compose_dir>/.bahia/env/<service-key>.env`
- `<compose_dir>/.bahia/render-state.json`

Use an explicit Compose project `name:` derived from the normalized environment name with an ID suffix when needed. Do not rely on directory-basename project naming.

Apply flow:

1. assemble the full environment plan
2. render into a staging location under `.bahia/`
3. validate staged output with `docker compose config -q`
4. atomically replace live generated files
5. pull when policy requires
6. run `docker compose --project-directory <compose_dir> up -d --remove-orphans`
7. observe resulting services and persist normalized observations

Phase 1 should drop service-image env substitution, service-scoped `up <service>`, and unconditional `--force-recreate` in the desired-state path. Compose should compute the minimal changes from the rendered project.

### Docker Engine and Podman phase 1

Add a Docker renderer/helper that maps desired service specs into Engine container config, host config, networking config, and prerequisite volume/network operations. Use stable Bahia-managed container naming and prefer Bahia labels over names for lookup/correlation.

Apply flow:

1. resolve endpoint through existing resolver/factory rules
2. inspect the existing Bahia-managed container for service/environment
3. compare `bahia.desired_hash`
4. no-op and observe when hashes match and pull policy does not force work
5. otherwise pull as needed, ensure volumes/networks, stop/remove existing managed container, create replacement, attach networks, and start
6. persist observation and apply metadata

Podman should not get a separate desired-state design in this plan; reuse the Docker-compatible seam where current adapter behavior allows.

### Observation normalization and drift

Normalize observations for Compose and Docker into a comparable subset: image ref/digest, command, entrypoint, working directory, non-secret env values, secret env key presence, ports, volumes, restart policy, network attachments, and Bahia labels. Exclude container IDs, timestamps, ephemeral IPs, Compose-generated non-semantic labels, and secret plaintext.

For desired-state-managed Compose/Docker services, drift should be:

- `in_sync` when normalized observed hash equals desired hash and runtime health is acceptable
- `drifted` when hashes differ
- `unknown` when observation cannot be produced

### Nostr projection and event payloads

Do not add new kinds or d-tag coordinates. Add fields and tags only where older readers can ignore them.

Status events should expose step progression inside existing status kinds: `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, and `projecting`. Item 2 owns emitting those steps from the lifecycle path; Item 8 owns additive projection/result payload compatibility. Result and read-model payloads should add only the minimum stable metadata needed for operators and downstream agents: renderer, desired-state hash, environment revision, runtime target, apply metadata summary, and observation ID when available. Exact field names belong in the event-spec implementation PR.

Projector/catalog changes must preserve tolerant decoding and replaceable semantics.

### Compose phase 2: deferred fragment optimization

Per-service fragments and service-scoped Compose apply are deferred until full-project synthesis is stable. Phase 1 has no fragment dependency: the operational Compose output remains the full project generated by the final renderer shape (`docker-compose.yml`, `.bahia/env/<service-key>.env`, and `.bahia/render-state.json`) for the environment or selected deployment unit, and apply remains full-project `docker compose --project-directory <compose_dir> up -d --remove-orphans`.

The fragment implementation slice is tracked in Beads rather than this plan. That slice must start from the final `ComposeRenderer` / `ComposeDesiredStateApplier` shape and decide:

- fragment layout and merge order under the Bahia-owned `.bahia/` tree without making fragment files the phase-1 source of truth;
- dependency eligibility, including `depends_on` health conditions, cross-service ordering, and sibling hydration guarantees;
- network and volume eligibility, including when declarations are project-wide and cannot be safely mutated by a service-scoped apply;
- explicit Compose project naming behavior so fragments cannot accidentally rely on directory-basename naming or create a second project;
- operator visibility rules for generated fragments, render metadata, secret-bearing env files, and any diagnostics exposed through Nostr/read-model metadata.

Until those rules are accepted and implemented, unsafe or ambiguous Compose changes must use the phase-1 full-project apply path.

## Work Items

### Item 1 — Runtime desired-state domain contract and schema

**Goal:** Introduce the typed desired-state model and persistable hashes/snapshots without changing external control-plane behavior.

**Done when:**
- `internal/domain/runtime_desired_state.go` exists with environment-scoped and service-scoped desired-state types.
- `internal/domain/models.go` carries desired-state, desired hash, normalized observation, and apply metadata fields.
- A migration adds desired-state and hash fields for deployment intents, deployment runs, environment service state, and runtime observations.
- Repository persistence loads and saves the new fields.
- Tests prove canonical hash stability and secret-redaction behavior.

**Key files:** `internal/domain/runtime_desired_state.go` (new), `internal/domain/models.go`, `internal/db/migrations/`, concrete persistence files under `internal/repository/` or the current repository package.

**Dependencies:** None.

**Size:** M.

### Item 2 — Runtime lifecycle refactor and environment apply locking

**Goal:** Make desired-state apply the canonical deploy path for Compose/Docker while serializing environment mutations safely.

**Done when:**
- `RuntimeLifecycleService` has a shared internal deploy helper used by deployment execution and direct runtime `action=deploy`.
- Environment-scoped advisory locking exists and is injected from `internal/app/app.go`.
- Deploy execution uses short DB transactions around persistence only, never around runtime I/O.
- Status publication includes the new step progression.
- Tests prove concurrent same-environment deploys serialize deterministically without sleep-based waiting.

**Key files:** `internal/service/runtime_lifecycle.go`, `internal/service/runtime_apply_lock.go` (new), `internal/app/app.go`, `internal/controlplane/operator_actions.go`, deploy-intent execution path to verify.

**Dependencies:** Item 1.

**Size:** L.

### Item 3 — Desired-state builder and legacy environment-plan hydration

**Goal:** Build canonical service desired state and assemble a full environment runtime plan that can render old environments safely.

**Done when:**
- Builder resolves service/environment/artifact/runtime config into a canonical desired service spec.
- Builder injects Bahia labels and separates literal env values from secret refs.
- Environment-plan assembly loads sibling services, hydrates missing snapshots from current persisted state, and computes deterministic revision hashes.
- Hydrated legacy sibling specs are persisted opportunistically.
- Tests prove first full-project render includes legacy siblings with no stored desired snapshot.

**Key files:** `internal/service/runtime_desired_state_builder.go` (new), `internal/service/runtime_environment_plan.go` (new), `internal/adapters/runtime/resolver.go`, service/state/artifact repository files.

**Dependencies:** Items 1–2.

**Size:** L.

### Item 4 — Add desired-state runtime adapter capability

**Goal:** Expose a desired-state apply seam without breaking runtimes not included in the first slice.

**Done when:**
- A `DesiredStateApplier`-style interface exists in the runtime adapter layer.
- Compose, Docker, and Podman adapters expose the capability where supported.
- Factory wiring exposes the capability cleanly.
- Kubernetes remains on the existing deploy path until a later implementation slice.
- Tests prove unsupported runtime types fail explicitly rather than silently falling back.

**Key files:** `internal/adapters/runtime/factory.go`, runtime adapter interface/type file under `internal/adapters/runtime/`, `internal/service/runtime_lifecycle.go`.

**Dependencies:** Item 1.

**Size:** M.

### Item 5 — Compose phase 1 full-project renderer and apply path

**Goal:** Replace per-service image substitution with authoritative full-project Compose synthesis compatible with the existing `compose_dir` operational model.

**Done when:**
- A Compose ownership gate rejects or holds back environments whose `compose_dir` is not explicitly Bahia-owned before any authoritative file generation occurs.
- Deterministic rendering produces a canonical Compose file, generated env material, and render metadata under a Bahia-owned layout chosen by the implementation.
- Staged output is validated with `docker compose config -q` before swap.
- Apply uses full-project `docker compose --project-directory <compose_dir> up -d --remove-orphans`.
- Desired-state deploy path no longer relies on `<SERVICE>_IMAGE`, service-scoped `up`, or unconditional `--force-recreate`.
- Tests cover generated YAML stability, project naming, env redaction, staging failure, and existing managed sibling preservation.

**Key files:** `internal/adapters/runtime/compose_renderer.go` (new), `internal/adapters/runtime/compose.go`, `internal/service/runtime_lifecycle.go`.

**Dependencies:** Items 3–4.

**Size:** L.

### Item 6 — Docker Engine and Podman desired-state apply

**Goal:** Make direct Docker Engine operations consume the same desired-state contract as Compose.

**Done when:**
- Docker desired-state render maps deterministically to container, host, network, volume, and healthcheck configuration.
- Stable Bahia-managed container naming and labels are enforced.
- Desired-hash comparison enables no-op applies when nothing changed.
- Required networks/volumes are ensured before create/start.
- Podman reuses the Docker-compatible path where current adapter semantics support it.
- Tests cover no-op hash match, recreate on hash drift, network/volume ensure behavior, and rejection paths.

**Key files:** `internal/adapters/runtime/docker_renderer.go` (new), `internal/adapters/runtime/docker.go`, `internal/adapters/runtime/factory.go`.

**Dependencies:** Items 3–4.

**Size:** L.

### Item 7 — Observation normalization and drift alignment

**Goal:** Make desired-vs-observed comparison explicit and stable for Compose/Docker desired-state-managed workloads.

**Done when:**
- Runtime observations persist normalized runtime-state JSON and hash.
- Compose and Docker observation paths produce the same comparable field subset.
- Drift uses desired hash vs normalized observed hash for desired-state-managed services.
- Failure paths still attempt best-effort observation and publish explicit failure/drift state.
- Tests cover in-sync, drifted, unknown, secret redaction, and Compose/Docker normalization parity.

**Key files:** `internal/domain/models.go`, `internal/service/runtime_lifecycle.go`, `internal/adapters/runtime/compose.go`, `internal/adapters/runtime/docker.go`, reconcile/drift files under `internal/reconcile/` or `internal/service/` to validate, runtime-observation repository files.

**Dependencies:** Items 5–6.

**Size:** M.

### Item 8 — Nostr status/result/read-model enrichment

**Goal:** Expose desired-state truth through existing Nostr families without adding new kinds.

**Done when:**
- Existing status/result publishers carry the step progression emitted by Item 2 without changing kinds or d-tag coordinates.
- `7961`/`7962` include additive renderer, desired-state hash, environment revision, runtime target, and observation ID metadata where available.
- `31961`, `31967`, and `31968` expose sanitized desired/apply metadata additively.
- Catalog decoders tolerate richer projection content.
- Tests inject events/read models directly and prove validation, idempotency, replaceable semantics, and backward-compatible decoding.

**Key files:** `internal/adapters/nostr/projector.go`, `internal/adapters/nostr/catalog.go`, `internal/service/runtime_lifecycle.go`, `internal/controlplane/reactor.go` only if status/result publication needs handler-level changes.

**Dependencies:** Items 1, 5, 6, 7.

**Size:** M.

### Deferred follow-up — Compose phase 2 internal fragments

Per-service fragments and service-scoped Compose apply are intentionally deferred until phase 1 proves full-project synthesis, hashing, observation normalization, and environment locking in production-like staging. Phase 1 does not need fragment files: the generated full project remains the canonical operational output, with `docker-compose.yml`, `.bahia/env/<service-key>.env`, and `.bahia/render-state.json` rendered from the environment/deployment-unit plan and applied as a full project.

The fragment implementation work is represented in Beads. Its design must reference the final `ComposeRenderer` / `ComposeDesiredStateApplier` shape and cover fragment layout, dependency eligibility, project-wide network and volume declaration safety, explicit project-name preservation, and operator-visible diagnostics/metadata before any service-scoped Compose apply path is introduced. Until then, unsafe or ambiguous Compose changes should use full-project apply.

#### Implementation status (2026-06-08)

Fragment optimization is implemented in `internal/adapters/runtime/compose_fragment_eligibility.go` and `compose_fragment_layout.go`. The applier in `compose_desired_state.go` checks fragment eligibility before each apply and uses service-scoped `docker compose up -d --no-deps <service>` when safe. Full-project apply remains the fallback for all unsafe or ambiguous changes. Fragment files live under `.bahia/fragments/` and the full `docker-compose.yml` is always updated alongside any fragment apply.

### Item 9 — Rollout hardening, verification, and docs

**Goal:** Prove mixed-version safety and make the desired-state runtime model operable.

**Done when:**
- Staging validates that non-Bahia-owned `compose_dir` targets are rejected or held back by explicit operator config.
- Existing environments hydrate sibling desired state correctly on the first full-project Compose deploy.
- Direct Docker deploy, rollback-to-older-artifact, and direct runtime `action=deploy` all use the shared desired-state path.
- Docs describe Bahia-owned Compose directories and additive Nostr payload changes.
- PSTF artifacts capture acceptance criteria, test matrix, defects, and verification evidence for this feature.
- Beads issues exist for any deferred Kubernetes/Podman or operator-visible fragment work.

**Key files:** `docs/control-planes.md`, `docs/nostr-commands.md`, `docs/event-spec.md`, `docs/deployment.md`, `pstf/features/<feature-id>/`, integration tests under the repo’s current test layout, runtime adapter tests.

**Dependencies:** Items 1–8.

**Size:** M.

## Risks and Migration

- **Compose directory ownership:** the largest operational risk is accidentally treating a non-Bahia-owned `compose_dir` as managed. Gate phase 1 behind validation that the target directory is dedicated to Bahia-managed services.
- **Legacy environment hydration:** first render must include sibling services that lack desired snapshots. Missing hydration would remove services from generated output.
- **Rollback:** additive DB columns and additive Nostr fields are safe for old readers, but older application code must be validated against generated Compose projects before production rollout.
- **Concurrency:** environment-level advisory locking is safer than service-level locking for Compose but reduces Docker parallelism. Accept that tradeoff for consistent behavior across runtimes.
- **Secrets:** generated Compose env files may contain resolved secrets; ownership, permissions, cleanup, and redaction must be tested and documented.
- **Renderer drift:** Compose and Docker must normalize observations into the same semantic subset to avoid false drift.

## Open Questions

- Does `5961` deploy currently reach `RuntimeLifecycleService` directly, or through an intermediate orchestration layer that must be refactored before the shared deploy helper lands?
- Is every configured production `compose_dir` Bahia-owned and safe for authoritative regeneration, or does rollout need a per-environment opt-in flag before Item 5 can be enabled?

## References
- `internal/adapters/runtime/compose.go`
- `internal/adapters/runtime/docker.go`
- `internal/adapters/runtime/factory.go`
- `internal/adapters/runtime/resolver.go`
- `internal/service/runtime_lifecycle.go`
- `internal/domain/models.go`
- `internal/controlplane/reactor.go`
- `internal/adapters/nostr/catalog.go`
- `internal/adapters/nostr/projector.go`
- `docs/control-planes.md`
- `docs/plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md`
- `docs/plans/resource-pressure-orchestration-2026-05-24.md`
- Docker Compose config: https://docs.docker.com/reference/cli/docker/compose/config/
- Docker Compose project naming: https://docs.docker.com/reference/compose-file/version-and-name/
- Docker Engine API: https://docs.docker.com/reference/api/engine/
