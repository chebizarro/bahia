# Test Matrix — DESIRED_STATE_RUNTIME

## Status: draft

## Coverage Summary

- **Criteria total:** 16
- **Criteria with mapped tests:** 16
- **Known gaps:** none (all criteria have at least one mapped test)

---

## Test Cases

### DSR-T-001 — Canonical hash stability and golden fixtures

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-001 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/domain/runtime_desired_state_test.go` |
| **Work item** | DSR-WI-01 |

**Description:** Prove that canonical hash serialization is deterministic across runs, uses sorted keys, stable list ordering, includes schema version, and excludes volatile fields. Golden hash fixtures must be locked so that changes to serialization break tests explicitly.

**Steps:**
1. Build a desired service spec with known field values.
2. Compute the canonical hash.
3. Assert against a golden fixture value.
4. Modify a volatile field (timestamp) and assert hash unchanged.
5. Modify a semantic field (image ref) and assert hash changed.

**Expected result:** Hash matches golden fixture, volatile fields do not affect hash, semantic changes produce different hashes.

---

### DSR-T-002 — Secret redaction in desired-state specs

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-001 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/domain/runtime_desired_state_test.go` |
| **Work item** | DSR-WI-01 |

**Description:** Prove that secret-backed env entries retain refs and redaction metadata, and that resolved plaintext never appears in serialized desired-state snapshots.

**Steps:**
1. Build a desired service spec with literal env values and secret-backed entries.
2. Serialize the spec to canonical JSON.
3. Assert secret refs are present; assert resolved plaintext is absent.

**Expected result:** Literal env values appear normally; secret entries contain only ref/source metadata.

---

### DSR-T-003 — Migration adds desired-state columns without breaking existing queries

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-002 |
| **Type** | integration |
| **Status** | not_implemented |
| **Target path** | `internal/db/migrations/` test or `internal/repository/` test |
| **Work item** | DSR-WI-01 |

**Description:** Prove that the additive migration adds desired-state JSONB and hash columns to deployment intents, deployment runs, environment service state, and runtime observations, and that repository load/save round-trips preserve the new fields.

**Steps:**
1. Run migrations against a test database.
2. Insert rows with desired-state and hash fields populated.
3. Load rows and assert field round-trip integrity.
4. Run existing queries and assert no breakage.

**Expected result:** New columns exist, data round-trips correctly, existing queries remain functional.

---

### DSR-T-004 — Shared deploy helper convergence

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-003 |
| **Type** | integration |
| **Status** | implemented |
| **Target path** | `internal/controlplane/reactor_policy_gate_test.go::TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState`, `internal/controlplane/reactor_policy_gate_test.go::TestHandleRollbackRequestExecutesSharedDesiredStateDeployPath`, `internal/controlplane/reactor_policy_gate_test.go::TestDirectRuntimeDeployResultCarriesDesiredHashFromState`, `internal/service/runtime_lifecycle_locking_test.go::TestConvergence_DeployAndDeployWithStatusUseSameHelper` |
| **Work item** | DSR-WI-02 |

**Description:** Prove that deployment request execution, direct runtime `action=deploy`, and rollback-to-artifact all flow through the same internal desired-state deploy helper for Compose/Docker-managed deploys. Assert that `restart` and `stop` do not create desired-state snapshots.

**Steps:**
1. Execute a deploy via the deployment request path; assert a desired-state snapshot/hash is persisted and `RuntimeLifecycleService.DeployWithStatus` is invoked.
2. Execute a deploy via direct `action=deploy`; assert terminal result metadata carries the desired hash from persisted desired state.
3. Execute rollback-to-artifact; assert the rollback intent/run carry desired snapshot/hash, status progression is emitted, and `RuntimeLifecycleService.DeployWithStatus` applies the selected artifact.
4. Execute `restart` and `stop`; assert no desired-state snapshot creation.

**Expected result:** Deploy request, direct deploy action, and rollback all converge through the shared desired-state helper; restart/stop skip desired-state assembly.

---

### DSR-T-005 — Concurrent same-environment deploys serialize deterministically

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-004 |
| **Type** | integration |
| **Status** | not_implemented |
| **Target path** | `internal/service/runtime_apply_lock_test.go` |
| **Work item** | DSR-WI-02 |

**Description:** Prove that concurrent deploys targeting the same environment are serialized by the advisory lock without sleep-based waiting, and that lock release occurs deterministically on both success and failure paths.

**Steps:**
1. Start two concurrent deploys for the same environment.
2. Assert one acquires the lock and the other waits.
3. Complete the first deploy; assert the second proceeds.
4. Simulate a failure during apply; assert the lock is released and the failed run is persisted.

**Expected result:** Deploys serialize without interleaving; locks release on success and failure.

---

### DSR-T-006 — Status publication includes step progression

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-003, DSR-AC-012 |
| **Type** | integration |
| **Status** | not_implemented |
| **Target path** | `internal/service/runtime_lifecycle_test.go` |
| **Work item** | DSR-WI-02 |

**Description:** Prove that status events emitted during desired-state deploy include the step progression metadata on the canonical status path: `building_desired_state`, `locking_environment`, `rendering`, `applying`, `observing`, `projecting`.

**Steps:**
1. Execute a desired-state deploy with a captured status publisher.
2. Collect all emitted status events.
3. Assert the expected step progression sequence.

**Expected result:** Status events carry step metadata in the expected order on existing canonical observables; legacy `6961` fixtures remain migration inventory only.

---

### DSR-T-007 — Desired-state builder produces correct specs with Bahia labels

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-005 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/service/runtime_desired_state_builder_test.go` |
| **Work item** | DSR-WI-03 |

**Description:** Prove the builder maps service/environment/artifact/runtime config/secrets into a canonical desired service spec with correct Bahia labels, env/secret separation, and typed renderer extensions.

**Steps:**
1. Build a spec from a known service, environment, artifact, and runtime config.
2. Assert Bahia labels contain service/environment/artifact/intent/run IDs and desired hash.
3. Assert literal env values are separated from secret refs.
4. Assert renderer extensions are typed (Compose, Docker).
5. Assert `Service.RuntimeTargetName()` is used as the service key source.

**Expected result:** Builder output matches expected contract for all fields.

---

### DSR-T-008 — Legacy sibling hydration on first full-project render

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-006 |
| **Type** | integration |
| **Status** | not_implemented |
| **Target path** | `internal/service/runtime_environment_plan_test.go` |
| **Work item** | DSR-WI-03 |

**Description:** Prove that environment-plan assembly includes sibling services that have no stored desired-state snapshots by reconstructing from persisted registry/runtime/artifact state.

**Steps:**
1. Set up an environment with 3 managed services; only the target service has a desired-state snapshot.
2. Assemble the environment plan.
3. Assert all 3 services appear in the plan.
4. Assert hydrated legacy specs are persisted opportunistically.
5. Assert deleted/tombstoned services are excluded.

**Expected result:** First render includes all siblings; hydrated specs are saved for future use.

---

### DSR-T-009 — Desired-state adapter capability wiring and unsupported type rejection

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-007 |
| **Type** | unit |
| **Status** | implemented |
| **Target path** | `internal/adapters/runtime/desired_state_capability_test.go`; `internal/adapters/runtime/resolver_desired_state_test.go` |
| **Work item** | DSR-WI-04 |

**Description:** Prove that Compose and Docker expose the desired-state apply capability, and that unsupported types (Kubernetes) fail explicitly rather than silently falling back.

**Steps:**
1. Request desired-state capability for Compose runtime type; assert success.
2. Request desired-state capability for Docker runtime type; assert success.
3. Request desired-state capability for Kubernetes runtime type; assert explicit error.

**Expected result:** Compose/Docker return the capability; Kubernetes returns an explicit unsupported error.

---

### DSR-T-010 — Compose full-project rendering stability

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-008 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/adapters/runtime/compose_renderer_test.go` |
| **Work item** | DSR-WI-05 |

**Description:** Prove that the Compose renderer produces deterministic YAML, env files, and render metadata from an environment plan. Assert project naming, env redaction, and sibling preservation.

**Steps:**
1. Render a full environment plan with 3 services.
2. Assert `docker-compose.yml` content is deterministic across runs.
3. Assert per-service `.env` files are generated under `.bahia/env/`.
4. Assert `render-state.json` captures renderer metadata.
5. Assert explicit Compose project `name:` is set.
6. Assert secret values are redacted in generated env files where required.

**Expected result:** Deterministic, complete, and correctly structured Compose project output.

---

### DSR-T-011 — Compose staging validation rejects invalid output

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-008 |
| **Type** | integration |
| **Status** | not_implemented |
| **Target path** | `internal/adapters/runtime/compose_renderer_test.go` |
| **Work item** | DSR-WI-05 |

**Description:** Prove that staged Compose output is validated with `docker compose config -q` before swap, and that invalid output prevents the atomic replacement.

**Steps:**
1. Generate valid staged output; assert validation passes and swap occurs.
2. Generate intentionally invalid staged output; assert validation fails and swap does not occur.

**Expected result:** Invalid Compose output never reaches the live location.

---

### DSR-T-012 — Compose apply uses full-project up without force-recreate

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-008 |
| **Type** | integration |
| **Status** | implemented |
| **Target path** | `internal/adapters/runtime/compose_desired_state_test.go`, `internal/adapters/runtime/compose_safety_test.go` |
| **Work item** | DSR-WI-05 |

**Description:** Prove that the desired-state Compose apply uses `docker compose up -d --remove-orphans` (full project), not service-scoped `up <service>`, `--force-recreate`, or `<SERVICE>_IMAGE` env substitution.

**Steps:**
1. Execute a desired-state Compose apply with a command capture.
2. Assert the command uses `up -d --remove-orphans` without `--force-recreate` or service scope.
3. Assert no `<SERVICE>_IMAGE` env var substitution in the execution environment.
4. Assert phase-1 apply succeeds from the rendered full project without creating or reading per-service fragment files.

**Expected result:** Full-project apply with no legacy per-service patterns or fragment dependency.

---

### DSR-T-013 — Docker Engine desired-state no-op on hash match

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-009 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/adapters/runtime/docker_renderer_test.go` |
| **Work item** | DSR-WI-06 |

**Description:** Prove that when an existing Bahia-managed container's `bahia.desired_hash` label matches the desired hash and pull policy does not force work, the apply is a no-op.

**Steps:**
1. Set up a fake Docker inspect returning a container with matching `bahia.desired_hash`.
2. Execute desired-state apply.
3. Assert no stop/remove/create/start calls were made.
4. Assert observation was still collected.

**Expected result:** Hash-matching container is left untouched; observation is returned.

---

### DSR-T-014 — Docker Engine desired-state recreate on hash drift

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-009 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/adapters/runtime/docker_renderer_test.go` |
| **Work item** | DSR-WI-06 |

**Description:** Prove that when desired hash differs from the existing container's label, the apply path pulls (per policy), ensures volumes/networks, stops/removes the existing container, creates a replacement, and starts it.

**Steps:**
1. Set up a fake Docker inspect returning a container with a different `bahia.desired_hash`.
2. Execute desired-state apply with pull policy enabled.
3. Assert the sequence: pull, ensure volumes/networks, stop, remove, create, attach networks, start.

**Expected result:** Full container lifecycle is executed in the correct order.

---

### DSR-T-015 — Observation normalization excludes volatile fields

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-010 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/domain/runtime_desired_state_test.go` or `internal/service/observation_test.go` |
| **Work item** | DSR-WI-07 |

**Description:** Prove that normalized observations exclude container IDs, timestamps, ephemeral IPs, Compose-generated non-semantic labels, and secret plaintext, while including image ref/digest, command, entrypoint, env, ports, volumes, restart policy, networks, and Bahia labels.

**Steps:**
1. Build a raw observation with all fields including volatile ones.
2. Normalize it.
3. Assert volatile fields are absent.
4. Assert semantic fields are present.
5. Assert secret env values are redacted (only key presence retained).

**Expected result:** Normalized observation contains only the comparable semantic subset.

---

### DSR-T-016 — Compose and Docker observation normalization parity

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-010 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/adapters/runtime/observation_normalization_test.go` |
| **Work item** | DSR-WI-07 |

**Description:** Prove that Compose and Docker observations of semantically identical service state produce identical normalized hashes.

**Steps:**
1. Build a Compose raw observation and a Docker raw observation for the same logical service state.
2. Normalize both.
3. Assert their hashes are identical.

**Expected result:** Cross-renderer normalization parity is proven.

---

### DSR-T-017 — Drift detection: in_sync, drifted, and unknown states

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-011 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/service/drift_test.go` or inline in lifecycle tests |
| **Work item** | DSR-WI-07 |

**Description:** Prove drift detection logic for the three possible states.

**Steps:**
1. Set desired hash = observed hash, health acceptable; assert `in_sync`.
2. Set desired hash != observed hash; assert `drifted`.
3. Simulate observation failure; assert `unknown`.

**Expected result:** Each state is correctly derived from hash comparison and observation availability.

---

### DSR-T-018 — Nostr result/read-model enrichment with backward-compatible decoding

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-013 |
| **Type** | contract |
| **Status** | not_implemented |
| **Target path** | `internal/adapters/nostr/catalog_test.go` |
| **Work item** | DSR-WI-08 |

**Description:** Prove that desired-state metadata is additive on the canonical ContextVM/state projection contract and that catalog/projection decoders tolerate richer content without breaking. Legacy `7961`/`7962` and `31961`/`31967`/`31968` shapes may remain only as migration fixtures.

**Steps:**
1. Build enriched canonical result/status/read-model events with renderer, desired hash, environment or unit revision, and observation ID.
2. Decode with the current catalog/projector path.
3. Assert new fields are accessible when present.
4. Decode enriched events with a pre-enrichment decoder mock; assert no parsing failure.

**Expected result:** Enriched events decode correctly; older decoders tolerate the extra data.

---

### DSR-T-019 — Compose ownership gate rejects non-Bahia-owned directories

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-014 |
| **Type** | unit |
| **Status** | not_implemented |
| **Target path** | `internal/adapters/runtime/compose_renderer_test.go` |
| **Work item** | DSR-WI-05 |

**Description:** Prove that the ownership gate rejects environments whose `compose_dir` is not explicitly Bahia-owned, and that no files are generated or written before the gate passes.

**Steps:**
1. Configure an environment with a non-Bahia-owned `compose_dir`.
2. Attempt to render; assert explicit rejection error.
3. Assert no files were written to the target directory.

**Expected result:** Non-owned directories are rejected before any I/O.

---

### DSR-T-020 — Failure path: lock release, failed run persistence, and terminal Nostr failure

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-015 |
| **Type** | integration |
| **Status** | not_implemented |
| **Target path** | `internal/service/runtime_lifecycle_test.go` |
| **Work item** | DSR-WI-02 |

**Description:** Prove that when desired-state apply fails, the advisory lock is released, the failed run is persisted, best-effort observation is attempted, and a terminal failure result is published.

**Steps:**
1. Simulate a failure during the apply phase.
2. Assert the advisory lock is released.
3. Assert the failed run is persisted with failure metadata.
4. Assert best-effort observation was attempted.
5. Assert a terminal `7961` failure result was published.

**Expected result:** Clean failure handling with no leaked locks or missing persistence.

---

### DSR-T-021 — Rollout: existing environment hydration on first Compose deploy

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-006, DSR-AC-011, DSR-AC-016 |
| **Type** | integration |
| **Status** | implemented |
| **Target path** | `internal/service/runtime_lifecycle_test.go::TestRuntimeLifecycleDesiredStateDeployHydratesLegacySiblingsAndRecordsDrift` |
| **Work item** | DSR-WI-09 |

**Description:** Prove that existing environments with managed services but no stored desired-state snapshots hydrate correctly on the first full-project Compose deploy path.

**Steps:**
1. Set up an environment with target, stored-snapshot sibling, and legacy sibling state rows.
2. Trigger `RuntimeLifecycleService.Deploy` through a Compose-capable desired-state runtime seam.
3. Assert the desired-state apply request includes target, stored sibling, and reconstructed legacy sibling specs.
4. Assert the legacy sibling spec is persisted opportunistically.
5. Assert post-apply observation records a current observation and advances target drift to `in_sync` when the normalized observed hash matches the desired hash.

**Expected result:** First deploy includes all managed services without data loss, persists hydrated legacy specs, and records observation/drift evidence after apply.

---

### DSR-T-022 — Rollout: Compose ownership inventory and gate config

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-014, DSR-AC-016 |
| **Type** | unit + config verification |
| **Status** | implemented |
| **Target path** | `internal/config/config_test.go`, `internal/adapters/runtime/resolver_test.go`, `config.yaml` |
| **Work item** | DSR-WI-09 |

**Description:** Prove that staging/production Compose ownership decisions are recordable through runtime target config and enforced by the Compose ownership gate before rollout.

**Steps:**
1. Load YAML and environment-variable runtime target config containing `bahia_owned`.
2. Resolve a Compose runtime with `bahia_owned=true` and an unmarked temp directory; assert the ownership gate passes by explicit operator config.
3. Resolve a Compose runtime where persisted `Environment.runtime_config` sets `bahia_owned=false` over a true default; assert an unmarked temp directory is blocked with `not_owned`.
4. Verify `config.yaml` records staging and production `compose_dir` targets with `bahia_owned: false`, requiring a valid marker or later operator approval before authoritative generation.

**Expected result:** Each configured staging/production `compose_dir` has a recordable ownership status, and unsafe/unknown directories remain blocked by gate behavior.

---

### DSR-T-023 — Rollout: deploy request/direct action/rollback desired-state convergence

| Field | Value |
|-------|-------|
| **AC IDs** | DSR-AC-003, DSR-AC-016 |
| **Type** | integration |
| **Status** | implemented |
| **Target path** | `internal/controlplane/reactor_policy_gate_test.go`, `internal/controlplane/reactor_nostr_event_semantics_test.go`, `internal/adapters/runtime/compose_desired_state_test.go`, `internal/adapters/runtime/docker_apply_test.go` |
| **Work item** | DSR-WI-09 |

**Description:** Prove that deploy request, direct runtime deploy action, and rollback-to-artifact use the desired-state apply path for Compose/Docker-covered deploys and expose terminal desired-state evidence.

**Steps:**
1. Execute the deploy request path and assert desired snapshot/hash persistence, run apply metadata, status progression, and terminal result metadata.
2. Execute direct runtime `action=deploy` and assert desired-state status/result evidence, including terminal desired hash when persisted state is available.
3. Execute rollback-to-artifact and assert the selected rollback artifact is applied through `RuntimeLifecycleService.DeployWithStatus`, with desired hash, apply metadata, status progression, and terminal result.
4. Run Compose/Docker desired-state adapter tests and assert Compose desired-state apply does not use `<SERVICE>_IMAGE`, service-scoped `up`, or unconditional `--force-recreate`.

**Expected result:** All three entrypoints share the desired-state apply path for Compose/Docker deploys, publish progression and terminal results, persist desired/apply metadata where available, and avoid legacy Compose image substitution in desired-state-managed deploys.

---

## Coverage Matrix

| Criterion | Test IDs | Covered |
|-----------|----------|---------|
| DSR-AC-001 | DSR-T-001, DSR-T-002 | yes |
| DSR-AC-002 | DSR-T-003 | yes |
| DSR-AC-003 | DSR-T-004, DSR-T-006, DSR-T-023 | yes |
| DSR-AC-004 | DSR-T-005 | yes |
| DSR-AC-005 | DSR-T-007 | yes |
| DSR-AC-006 | DSR-T-021 | yes |
| DSR-AC-007 | DSR-T-009 | yes |
| DSR-AC-008 | DSR-T-010, DSR-T-011, DSR-T-012 | yes |
| DSR-AC-009 | DSR-T-013, DSR-T-014 | yes |
| DSR-AC-010 | DSR-T-015, DSR-T-016 | yes |
| DSR-AC-011 | DSR-T-017 | yes |
| DSR-AC-012 | DSR-T-006 | yes |
| DSR-AC-013 | DSR-T-018 | yes |
| DSR-AC-014 | DSR-T-019, DSR-T-022 | yes |
| DSR-AC-015 | DSR-T-020 | yes |
| DSR-AC-016 | DSR-T-021, DSR-T-022, DSR-T-023 | partial |

## Implementation Notes

- **Determinism:** All tests must use deterministic fixtures, fake clocks, and captured publishers. No live Docker/Compose/relay dependencies in unit or integration tests.
- **Golden fixtures:** Hash stability tests (DSR-T-001) must lock golden values in committed fixture files so serialization changes are caught explicitly.
- **Observation parity:** DSR-T-016 is critical for preventing false drift across renderers. Maintain this test as a cross-cutting regression guard.
- **Recommended sequence:** Start with domain/hash tests (DSR-T-001, DSR-T-002), then persistence (DSR-T-003), then builder/plan (DSR-T-007, DSR-T-008), then adapter capability (DSR-T-009), then renderer tests (DSR-T-010-014), then observation/drift (DSR-T-015-017), then Nostr (DSR-T-018), and finally rollout (DSR-T-021 and DSR-T-022).
