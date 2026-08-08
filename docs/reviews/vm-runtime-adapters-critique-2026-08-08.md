# Critique: VM Runtime Adapters plan (2026-08-08)

Scope: seams, contradictions, over-planning, ordering questions. Plan reviewed: `docs/plans/vm-runtime-adapters-2026-08-08.md`.

## 1. Top 3 under-specified seams

**A. The desired-state capability layer is missing from the plan entirely.**
The plan's "runtime contract" (§Background) lists only `Observer`/`Runtime`/`LifecycleRuntime`. It omits `DesiredStateApplier` (`internal/adapters/runtime/desired_state_capability.go:23`), which every shipped runtime implements (`docker_desired_state.go`, `compose_desired_state.go`, `kubernetes_desired_state.go`, `podman_desired_state.go`). Worse, `NormalizedObservation` (`internal/domain/runtime_desired_state.go:983-1008`) is container-shaped — `image_ref`, `command`, `entrypoint`, `ports`, `volumes`, `env` — with no slot for vcpus/memory/disk/firmware, and normalization exists only for Docker inspect data (`observation_normalizer.go:31`). Work item 4's "spec-hash drift input" is one clause covering: does `vm.Runtime` implement `DesiredStateApplier` or return `ErrDesiredStateNotSupported`, and how does a VM spec produce a `NormalizedObservation`/`NormalizedHash`? As written, VM drift is either vacuous or spuriously non-empty.

**B. The "closed set" is wider than item 3 admits.**
Adding `vm-firecracker`/`vm-qemu` also requires: a new migration widening `CHECK (runtime_type IN (...))` at `internal/db/migrations/000038_deployment_units.up.sql:18`; MCP tool schema enums at `internal/mcp/server.go:291` and `:327`; TS unions at `test/e2e-agent/types.ts:73` and `test/e2e-agent/drivers/api.ts:74`. None appear in item 3. The migration in particular is a separate, ordered unit of work.

**C. Artifact → release-dir resolution is one sentence for the entire deploy input.**
`Deploy` receives a single string built by `imageRefForArtifact` (`internal/service/runtime_lifecycle.go:1261-1269`) as `repo + "@" + digest`. §3 asserts `ImageRepo` = path under `image_root` and `ImageDigest` = sha256 of `manifest.json`, but never states the digest string format (`sha256:` prefixed?), whether a filesystem-path repo survives existing artifact validation and registry-auth handling, or who rejects a malformed ref. This is the seam most likely to force a rework mid-item-4.

## 2. Contradictions / missing dependencies

- **Cross-repo release step is absent.** cascadia-go is already a pinned dependency (`go.mod:8`, `v1.0.1`) fetched through an Athens proxy with the version pinned literally in `.github/workflows/deploy-edge.yml:65`. Items 1–2 therefore require a cascadia-go tag, a bahia `go.mod` bump, and a CI pin update — an unlisted work item on the critical path. "Items 1–2 land in cascadia-go first (independent of bahia items 3–4)" is true for code, false for CI.
- **Windows health is contradictory.** Decision 3 puts guest metrics in scope and §4 makes agent ping the discriminator between `healthy` and `degraded`; Open Question 3 accepts hypervisor-state-only for Windows. There is no specified `HealthStatus` mapping for "no agent expected", so Windows guests land permanently `degraded`.
- **Dependency line vs. item order.** "2 gates 4's artifact resolution for qcow2" but item 2 is cascadia-go work sequenced alongside item 1, while the stated chain is `3 → 4 → {5,6}`. If libvirt/qcow2 is the first driver, item 2 — not item 1 — is the true first blocker.

## 3. Over-planning — cut or simplify

- **Cut §5/item 8 (console log tail) from v1.** `Tail`/`Follow` over a growing console file with timestamp parsing is a self-contained sub-project delivering little next to state + health. Return "logs unsupported for VM runtimes" and file it as a follow-up.
- **Cut metrics from item 1; ship `ping`/`pong` only.** Metrics frames + /proc collection + a Windows build-tag stub is the largest cascadia-go item and it gates item 7, i.e. it sits on the cross-repo critical path to buy `Metadata` fields nothing yet consumes.
- **Reconsider Decision 2 (both engines in one pass).** libvirt gets instance persistence free from `virsh define`; item 6's firecracker path (supervised VMM, pidfile/API-socket registry, orphan reap/adopt across bahia restarts) is a materially larger and riskier build. Ship libvirt first, prove the artifact/observe/drift seams end-to-end, then add firecracker behind the already-validated `Hypervisor` interface.
- The plan is otherwise appropriately sized; resist adding detail to §2's driver interface before seam A is answered — it will change the interface.

## 4. Questions that change implementation order

1. **Do VM adapters implement `DesiredStateApplier` in v1?** Yes → item 4 absorbs a desired-state renderer + VM normalizer and must fully precede 5/6. No → the reconciliation path needs an explicit unsupported branch, and someone must confirm the environment planner tolerates it at runtime. This is the single highest-leverage unknown.
2. **Do VMs run on the bahia host or on remote hosts?** Both drivers as described assume local `virsh` and local `state_dir`. Remote targets reshape config/resolver (item 3) and the entire Observe transport — this must be settled before item 3, not during item 5.
3. **libvirt-only v1?** If yes, item 6 leaves v1, item 1 shrinks to ping-only, and item 2 becomes the first cross-repo blocker — a different ordering than the plan's.
4. **When does the `runtime_type` migration land?** If deployment-unit rows can be written before the CHECK widens, the migration splits out of item 3 and ships first.
