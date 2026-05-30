# Review: Runtime Control Client Migration (bahia-aop4)

**Date:** 2026-05-30
**Scope:** Tasks 9–11 — Runtime control seam, Docker API clients, Compose executor flow
**Verdict:** ✅ **Pass — all acceptance criteria met, no blocking issues**

---

## Context / Scope

This review covers the final implementation of the runtime control client migration, spanning three tasks:

| Task | Feature ID | Title |
|------|-----------|-------|
| 9 | `bahia-xrjj` | Introduce Runtime Control Client Seam |
| 10 | `bahia-ha43` | Migrate Docker/Podman to API-Owned Control Clients |
| 11 | `bahia-e6uz` | Convert Compose Execution to Unit-Owned Renderer/Executor Flow |

**Key files reviewed:**
- `internal/adapters/runtime/control_client.go` (237 lines)
- `internal/adapters/runtime/docker.go` (856 lines)
- `internal/adapters/runtime/docker_apply.go` (280 lines)
- `internal/adapters/runtime/docker_desired_state.go` (424 lines)
- `internal/adapters/runtime/compose_executor.go` (99 lines)
- `internal/adapters/runtime/compose_desired_state.go` (277 lines)
- `docs/deployment.md` (350 lines)
- PSTF artifacts: `pstf/features/bahia-xrjj/`, `bahia-ha43/`, `bahia-e6uz/`

---

## 1. Acceptance Criteria Verification

### Task 9: Runtime Control Client Seam (`bahia-xrjj`)

| AC | Description | Status |
|----|-------------|--------|
| AC1 | Runtime control interface exposes execution-mode metadata and stays narrow | ✅ `RuntimeControlClient` has single `ExecutionMode()` method |
| AC2 | Docker/Podman desired-state apply reports `execution_mode=engine_api` | ✅ `dockerEngineControlClient.ExecutionMode()` returns `ExecutionModeEngineAPI` |
| AC3 | Compose desired-state apply reports `execution_mode=cli` | ✅ `CLIComposeExecutor.ExecutionMode()` returns `ExecutionModeCLI` |
| AC4 | Compose compatibility mode is explicit in config | ✅ Documented in `deployment.md`; factory rejects misconfigured Compose targets |
| AC5 | Deployment docs describe execution mode values | ✅ "Runtime execution mode" section in `deployment.md` |

### Task 10: Docker/Podman API-Owned Control Clients (`bahia-ha43`)

| AC | Description | Status |
|----|-------------|--------|
| AC1 | Docker desired-state apply delegates lookup and mutations to `DockerControlClient` | ✅ `docker_apply.go` routes all operations through `o.ControlClient()` |
| AC2 | Engine API operations owned by `dockerEngineControlClient` | ✅ All 9 operations implemented in `control_client.go` |
| AC3 | Execution mode sourced from control client in apply results | ✅ `executionMode := control.ExecutionMode()` used consistently |
| AC4 | Legacy `Deploy` delegates to control client | ✅ `docker.go:Deploy` calls `control.PullImage`, `control.CreateContainer`, `control.StartContainer` |

### Task 11: Compose Unit-Owned Renderer/Executor Flow (`bahia-e6uz`)

| AC | Description | Status |
|----|-------------|--------|
| AC1 | Full-project rendering uses only target deployment unit's services | ✅ `selectComposeDeploymentUnitPlan` filters to target unit |
| AC2 | Compose ownership enforced before desired-state commands | ✅ `ValidateOwnership` called before staging (Step 1) |
| AC3 | CLI executor validates staged output and applies under `RuntimeControlClient` seam | ✅ `ComposeExecutor` embeds `RuntimeControlClient`; `Validate` + `Up` flow |
| AC4 | No service image env substitution, service-scoped `up`, or force recreate | ✅ Comments and code confirm; no `--force-recreate`, no `<SERVICE>_IMAGE` |
| AC5 | Apply rejects non-Compose runtime type before staging | ✅ `selectComposeDeploymentUnitPlan` checks `RuntimeType` |

**Result: 14/14 acceptance criteria verified ✅**

---

## 2. Code Consistency and Patterns

### Interface Design

The migration follows a clean layered pattern:

```
RuntimeControlClient (narrow metadata seam)
├── DockerControlClient (Engine API mutations)
│   └── dockerEngineControlClient (concrete impl)
└── ComposeExecutor (CLI transport)
    └── CLIComposeExecutor (concrete impl)
```

**Strengths:**
- The `RuntimeControlClient` seam is intentionally narrow (single method), preventing interface bloat.
- Each runtime-specific client extends the common seam with only the operations their appliers need.
- Compile-time interface assertions (`var _ DockerControlClient = (*dockerEngineControlClient)(nil)`) catch contract drift early.
- `DockerObserver.ControlClient()` supports injection for testing while defaulting to production implementation.

**Pattern consistency:**
- Error wrapping follows `fmt.Errorf("context: %w", err)` consistently.
- Context propagation via `context.Context` is correct across all API calls.
- HTTP status code handling is thorough (e.g., `StopContainer` accepts 204, 200, and 304).
- Structured logging with `zap` fields is used consistently.
- The `DockerContainerConfigs` type cleanly separates container, host, and networking concerns.

### Observations (non-blocking)

1. **`PullImage` drain loop** (`control_client.go:88-91`): The pull response body is drained with a simple read loop. This works correctly but discards pull progress. If pull-progress reporting is ever needed, this would need rework. Current behavior is intentional and adequate.

2. **API version pinning**: All Docker API calls use `/v1.44/`. This is consistent throughout and appropriate for the target Docker/Podman compatibility level.

3. **`strings.NewReader(string(bodyJSON))`** in `CreateContainer` and `ConnectNetwork`: The `string()` conversion creates a copy. Could use `bytes.NewReader(bodyJSON)` directly. Non-blocking; negligible performance impact.

---

## 3. Test Coverage Assessment

### Quantitative Coverage

The runtime package has **19 test files** covering the migration-relevant code:

| Area | Key Test Files | Migration-Specific Tests |
|------|---------------|------------------------|
| Control client seam | `desired_state_capability_test.go` | Interface compilation, request/result construction |
| Docker apply | `docker_apply_test.go` | `TestApplyDesiredState_DelegatesMutationsToControlClient`, `TestDockerDeploy_DelegatesMutationsToControlClient` |
| Docker desired state | `docker_desired_state_test.go` | Config mapping, label lookup, `FindBahiaManagedContainer` |
| Compose apply | `compose_desired_state_test.go` | Unit selection, ownership, validation, dry-run, multi-service |
| Compose executor | `compose_safety_test.go` | No force-recreate, no service-image env, validation-before-promote |
| Podman delegation | `podman_desired_state_test.go` | Delegation to Docker-compatible path |
| Factory | `factory_test.go` | Compose CLI execution-mode requirements |

### Test Quality

**Strong points:**
- **`recordingDockerControlClient`**: A well-designed test double that records all control client calls, enabling precise verification that mutations are delegated correctly.
- **`applyMockState`**: Comprehensive HTTP-level mock for Docker Engine API with configurable failure injection for each operation (pull, stop, remove, create, start).
- **Negative/error path coverage**: Tests for nil spec, pull failures (fatal vs. warning), stop/remove/create/start failures, ownership failures, validation failures.
- **Dry-run coverage**: Both Docker and Compose appliers have dry-run tests verifying no mutations occur.
- **Determinism tests**: Compose renderer and Docker config mapping are tested for deterministic output.

**Coverage gaps (non-blocking):**
- No dedicated unit tests for `ConnectNetwork` in isolation (tested indirectly through `attachAdditionalNetworks` integration path).
- `PullImage` registry auth header construction is tested via `docker_test.go` but not directly through the control client path.

### PSTF Artifacts

All three features have complete PSTF evidence:
- ✅ `feature_spec.json` — well-defined scope and acceptance criteria
- ✅ `acceptance_criteria.json` — criteria mapped to tests
- ✅ `test_matrix.json` — coverage mapped to acceptance criteria
- ✅ `verification_report.md` — passing test runs documented (2026-05-30)
- ✅ `defects.json` — empty arrays (no open defects)
- ✅ `hitl_decisions.md` — human-in-the-loop decision records present

---

## 4. Documentation Review

### `docs/deployment.md`

The deployment guide comprehensively covers the runtime control client migration:

- **Runtime execution mode section**: Clearly explains `engine_api` vs. `cli` modes and when each applies.
- **Compose compatibility requirement**: Explicitly states Compose must set `execution_mode: cli`.
- **Desired-state apply flow**: Documents the unit-owned rendering, staging, validation, and full-project apply sequence.
- **Anti-patterns documented**: "No per-service image environment substitution, service-scoped `up`, or unconditional `--force-recreate`."
- **Configuration examples**: YAML and environment variable examples for runtime targeting, including endpoint aliases and TLS.

**Documentation quality:** Complete and accurate. The deployment guide serves as both operator reference and architectural record for the runtime control boundaries.

---

## 5. Placeholder / Stub Code Check

**Result: No placeholder or stub code found ✅**

Searched all five core implementation files for `TODO`, `FIXME`, `HACK`, `stub`, and `placeholder`. The only matches were documentation comments explaining that redacted placeholders are intentionally never emitted in secret handling — this is correct behavior, not stub code.

All implementations are complete:
- `dockerEngineControlClient`: All 9 interface methods fully implemented with HTTP calls
- `CLIComposeExecutor`: `Validate` and `Up` fully implemented with CLI invocation
- `ComposeDesiredStateApplier`: Full 7-step apply flow implemented
- `DockerObserver.ApplyDesiredState`: Full 9-step apply flow implemented

---

## 6. Recommendations

### No blocking issues found.

### Minor improvements (optional, future work):

1. **`bytes.NewReader` over `strings.NewReader`** in `control_client.go:161,209` — avoids an unnecessary string copy of the JSON body. Trivial optimization.

2. **Pull progress callback** — `PullImage` currently drains the response body silently. A future enhancement could accept an optional progress callback for operator visibility during long pulls.

3. **Structured error types** — Control client methods return `fmt.Errorf` strings. If callers ever need to distinguish error types programmatically (e.g., "image not found" vs. "registry unreachable"), typed errors would help. Not needed today.

---

## Summary

The runtime control client migration is **complete and well-executed**. The implementation:

- Introduces a clean, narrow control seam (`RuntimeControlClient`) that separates execution-mode metadata from runtime-specific mutation operations
- Centralizes all Docker Engine API mutations behind `DockerControlClient`, eliminating scattered HTTP calls
- Routes Compose execution through a dedicated `ComposeExecutor` with CLI transport, enforcing the full-project apply pattern
- Is thoroughly tested with 19 test files, including migration-specific delegation tests and comprehensive error-path coverage
- Has complete PSTF evidence with no open defects
- Is fully documented in `deployment.md` with operator-facing guidance

All 14 acceptance criteria across the three tasks are verified. No placeholder code, no open defects, no blocking issues.
