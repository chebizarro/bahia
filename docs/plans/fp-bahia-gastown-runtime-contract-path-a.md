# Plan: fp-bahia-gastown-runtime-contract — Path A

**Fleet task:** `fp-bahia-gastown-runtime-contract` (fleet-planning ledger, kind 30900)
**Claimed revision:** `ecb2d854eabe509a427cbf622631063b06ab76008b05796debe46142d8e22e89`
**Execution attempt:** `attempt:e0585b546afb44bbabe9097fc38b8336169c46758601452c668463760c2da7b8`
**Goal:** Signer-first environment create/update **plus** deployment-unit management so Gastown can deploy to the `max` host as a direct-runtime Compose target. Unblocks `fp-gastown-bahia-reactivation`.

## Hard acceptance criteria (from the fleet task)

- Authorized signer can create/update the target **without direct DB mutation**
- Deploy path supports durable state + file-backed NIP-46 signer configuration
- **No secrets in argv, logs, events, or task evidence**
- Restart preserves state; health and rollback provable
- Fail-closed behavior preserved; no fallback to obsolete experimental relay/Blossom topology

## Architecture decision (from scoping)

`max` is **not** a new runtime type. It is modeled with existing abstractions:
- deployment unit with `runtime_type: compose`
- `endpoint_ref`: max managed endpoint alias
- `compose_dir`: Bahia-owned project dir on max
- explicit `execution_mode` (sdk or cli), ownership/reconcile policy

No Loom protocol changes. `buildDeployScript` (bare `docker run`) is NOT the vehicle; the desired-state Compose pipeline is.

## Scoping facts (verified by explore probe, Bahia HEAD `a87be77f`)

- Env model: `internal/domain/models.go:213-245` (typed `targeting` via migration 39)
- Deployment units: `internal/domain/deployment_unit.go` + migration 38; repo: `internal/repository/pg_deployment_unit.go`; JSON schema exists at `schemas/deployment_unit.json`
- Signer-first ContextVM handlers `environment/create|update|delete`: `internal/controlplane/encrypted_route_handlers.go:34-36,128-131,157-174,217-301` — payloads currently LACK `targeting`, `reconcile_mode`, `deployment_units`, org context
- Dormant REST DTOs already model the richer shape: `internal/api/dto/requests.go:59-84` — reuse this contract, don't invent a second one
- CLI: `cmd/cli/main.go:259-307` — `environments create` is a `signerFirstMutationUnavailable` stub; no `update`; no unit commands. Signer-first operator client: `cmd/cli/operator_nostr.go:83-171` (has deploy/rollback/adopt/policy but no env methods). Client lib: `pkg/client`
- Coordinator direct-runtime branch is hard-coded to local-image Compose: `internal/workflow/coordinator.go:167-168,276-290`
- Runtime factory (docker/compose/k8s/podman, cli/sdk modes): `internal/adapters/runtime/factory.go`; Compose runtime: `internal/adapters/runtime/compose.go`
- Desired-state spec already covers env-files, secret refs, volumes, restart, healthchecks: `internal/domain/runtime_desired_state.go`
- Adoption is the only current unit creator: `internal/service/adoption.go:102-106,664+`
- Doc drift: `docs/deployment.md:190-199` documents a nonexistent `POST /environments/{id}/units`

## Work items

- [x] **Item 1 — Server contract (pair):** DONE — commit `1c50c56c`. Extend ContextVM `environment/create|update` payloads to DTO parity (`targeting`, `reconcile_mode`, `deployment_units`, org context) reusing `internal/api/dto/requests.go` shapes + `schemas/deployment_unit.json` validation. Add an atomic service operation that persists environment + explicit units transactionally (reuse `DeploymentUnitRepository`; preserve implicit-default-unit rule when no units are given; protect units already referenced by state/runs/intents; support implicit→explicit transition). Unit tests for handler decode/validation and transactional persistence/rollback.
  **Done when:** a signed `environment/create` or `environment/update` ContextVM request carrying `deployment_units` persists env+units atomically; tests pass.
- [x] **Item 2 — Coordinator routing (pair):** DONE — commit `b3cae0e7`. Replace the hard-coded local-image Compose condition in `internal/workflow/coordinator.go` with deployment-unit/runtime-target resolution: if the resolved unit's runtime is Compose with a managed endpoint, execute via the runtime lifecycle + full desired-state Compose apply (env files, secret mounts, volumes, restart policy, healthchecks). Loom path untouched for other cases. Tests with a max-like Compose endpoint fixture proving env-file/secrets/volumes/restart/healthcheck reach the rendered Compose project.
  **Done when:** direct-runtime selection is driven by resolved deployment unit, not image-repository naming; fixture test passes.
- [x] **Item 3 — CLI + client:** DONE — commit `358342bb`. `pkg/client`: signed publish/correlate for `environment/create|update`. `cmd/cli/operator_nostr.go`: extend `cliOperatorClient`. `cmd/cli/main.go`: make `environments create` functional, add `environments update`, add `environments units list|create|update` (signer-first; list may use read API; no REST mutation revival; no secrets in argv).
  **Done when:** `bahia environments create/update` and `units` subcommands publish signed ContextVM mutations matching Item 1's contract; CLI tests pass.
- [x] **Item 4 — Docs, gates, review:** DONE — commit `7c17855c`. Fix `docs/deployment.md` unit-endpoint drift; document the max Compose target pattern (endpoint alias, compose_dir, execution mode, durable state, file-backed signer config); run full quality gates (`go test ./...`, vet, web tests if touched); oracle review of the diff.
  **Done when:** gates green, docs accurate, review findings addressed.

## Status log

- 2026-08-02: Task claimed (revision `ecb2d854…`). Plan authored from explore-probe scoping report.
- 2026-08-02: Item 2 done (`b3cae0e7`): coordinator resolves explicit unit from intent/desired state else env default; Bahia-managed Compose units with endpoint_ref+compose_dir go through runtime lifecycle + DesiredStateApplier; misconfigured/failed Compose routing errors out — never falls back to Loom; non-Compose units keep Loom path. New option `WithDeploymentUnitRouting`.
- 2026-08-02: Item 3 done (`358342bb`): CLI `environments create|update` + `environments units list|create|update`, signer-first only, no HTTP mutation fallback; units mutations use read-merge-write of the complete unit set via signed `environment/update`; environment GET/list reads now embed explicit units (or marked implicit default); surgical controlplane fix strips only transport-owned `_meta` before strict decode (other unknown fields still rejected, two-sided test added); unit spec JSON `--file` support; no secrets via argv.
- 2026-08-02: Item 4 done (`7c17855c`): max Compose endpoint/ownership/signer documentation completed; quality gates and initial review completed.
- 2026-08-02: Review remediation (`bahia-2p35n`): default-unit lookup now honors normalized targeting and fails closed; pre-intent desired-state snapshots resolve Bahia-managed units; complete-set unit writes require a locked `expected_updated_at` precondition with bounded CLI remerge/retry; run completion retains deployment-unit identity; unit create/update can change `default_unit_key` atomically.
- 2026-08-02: Item 1 done (`1c50c56c`): `environment/create|update` ContextVM payloads now accept `org_id`, `targeting`, `reconcile_mode`, `deployment_units[]` (full unit shape per `schemas/deployment_unit.json`), `deploy_strategy`, `protected`; update requires `id`. Semantics: `deployment_units` omitted ⇒ unit set unchanged; provided ⇒ complete desired explicit set; `[]` ⇒ back to implicit default; referenced units cannot be removed (whole txn rolls back, incl. runtime-observation refs); with explicit units `targeting.default_unit_key` must name one; unknown fields rejected; `org_id` RBAC-checked both orgs on move. Responses include `status`, `environment`, `environment_id`, `deployment_units`.

## Item 1 payload contract (for CLI work)

```json
{
  "org_id": "uuid",
  "name": "required (create)",
  "id": "required (update)",
  "expected_updated_at": "required when update supplies deployment_units",
  "loom_worker_selector": {},
  "runtime_config": {},
  "targeting": {"default_unit_key": "default", "failure_domain_labels": {}, "secret_scope_mode": "service|environment|unit", "default_reconcile_mode": "observe_only|auto_apply|approval_required|disabled"},
  "reconcile_mode": "observe_only|auto_apply|approval_required|disabled",
  "deployment_units": [{"key": "required", "display_name": "", "runtime_type": "docker|compose|kubernetes|podman", "endpoint_ref": "", "compose_dir": "", "namespace": "", "network_profile": {}, "ownership_mode": "bahia_managed|adopted|external", "reconcile_mode": "...", "runtime_config": {}}],
  "deploy_strategy": "replace|blue_green|canary",
  "protected": false
}
```

## Constraints for all agents

- This branch of work must be committed but NOT pushed until the orchestrator/user says so.
- Never place secrets or bunker URLs in argv, logs, code, or test fixtures.
- Preserve fail-closed behavior everywhere (reject on missing signer/authorization rather than falling back).
