# OpenClaw SoulFactory Control Wrapper Spec

`openclaw-soulfactory-control` is the host-local command used by the Bahia-owned OpenClaw SoulFactory sidecar. The sidecar owns Nostr validation, idempotency, capability publication, and result publication. The wrapper owns only local OpenClaw runtime effects.

This command is intentionally not a REST API. It receives one `OpenClawControlInvocation` JSON document on stdin and writes one `OpenClawControlOutcome` JSON document on stdout.

## Goals

- Provision or bind an OpenClaw isolated agent from a SoulFactory runtime control request.
- Preserve enough local state for idempotent update, suspend, resume, redeploy, and revoke decisions.
- Use supported OpenClaw host-local commands before direct config edits.
- Return structured success, rejected, or failed outcomes compatible with `soulfactory-runtime-control/v1`.
- Keep runtime side effects auditable under the generated agent workspace.
- Keep persistent OpenClaw runtime execution containerized.

## Non-Goals

- Do not expose HTTP lifecycle routes.
- Do not publish Nostr events. The sidecar already signs and publishes `30317` and `38386`.
- Do not mint private keys inline in generated config.
- Do not create fake Bahia deployable artifacts, image tags, or digests.
- Do not delete user data unless `soulfactory.revoke` explicitly asks for workspace deletion.
- Do not launch persistent OpenClaw gateway or agent runtime processes directly on bare metal.

## Runtime Deployment Doctrine

Provisioned OpenClaw souls must run under Docker, not as bare-metal user services or background processes. The wrapper is allowed to be a host-local command because it is an adapter invoked by the sidecar, but any persistent OpenClaw runtime it creates or targets must be one of:

- an existing containerized OpenClaw gateway that supports isolated agents;
- a per-soul Docker Compose project rendered and owned by the wrapper;
- a Bahia-managed container deployment once Bahia runtime ownership is wired for this path.

The wrapper must not run `openclaw gateway run`, `openclaw gateway start`, `go run`, `npm start`, or a user-level systemd service as the long-lived runtime for a provisioned soul. If the OpenClaw CLI is used from the host, it should target a containerized runtime through `openclaw --container <name> ...`, `docker exec`, or an equivalent container control path.

For the first `max` deployment, the recommended mode is `existing-container`: the wrapper creates/binds isolated agent state and routes inside an already containerized OpenClaw gateway. A later `per-agent-compose` mode may create one containerized OpenClaw gateway per soul when isolation requirements justify the extra resources.

## Command Interface

Input is the exact JSON object passed by `OpenClawCommandDriver`:

```json
{
  "envelope": {},
  "method": "soulfactory.provision",
  "agent_id": "agent-alice",
  "soul_id": "agent-alice",
  "spec_hash": "sha256:...",
  "params": {}
}
```

Output:

```json
{
  "status": "success",
  "result": {
    "agent_id": "agent-alice",
    "runtime": "openclaw",
    "runtime_binding": "openclaw://agents/agent-alice",
    "state": "running",
    "spec_hash": "sha256:...",
    "observed_at": 1715700005,
    "warnings": []
  },
  "error": null
}
```

Valid statuses are `success`, `rejected`, and `failed`.

## Configuration

The wrapper reads configuration from environment variables:

- `OPENCLAW_SOULFACTORY_ROOT`: base directory for generated agent workspaces and state. Default: `~/.openclaw/soulfactory`.
- `OPENCLAW_SOULFACTORY_OPENCLAW_BIN`: OpenClaw CLI path. Default: `openclaw`.
- `OPENCLAW_SOULFACTORY_RUNTIME_MODE`: `existing-container` or `per-agent-compose`. Default: `existing-container`.
- `OPENCLAW_SOULFACTORY_CONTAINER`: container name used when `OPENCLAW_SOULFACTORY_RUNTIME_MODE=existing-container`.
- `OPENCLAW_SOULFACTORY_DEFAULT_MODEL`: fallback model for `openclaw agents add --model`.
- `OPENCLAW_SOULFACTORY_DEFAULT_BINDINGS`: comma-separated channel bindings to add on provision, optional.
- `OPENCLAW_SOULFACTORY_DRY_RUN`: when `1`, validate and render state but skip OpenClaw CLI mutations.

The sidecar also injects:

- `SOULFACTORY_METHOD`
- `SOULFACTORY_AGENT_ID`
- `SOULFACTORY_SPEC_HASH`

## Local State Layout

Each managed agent gets a deterministic directory:

```text
$OPENCLAW_SOULFACTORY_ROOT/
  agents/
    <agent-id>/
      workspace/
        SOUL.md
        IDENTITY.md
        MEMORY.md
        AGENTS.md
        .openclaw/
          soulfactory.json
      state.json
      last-invocation.json
      last-outcome.json
```

`state.json` is wrapper-owned and records:

- `agent_id`
- `soul_id`
- `spec_hash`
- `state`: `running`, `suspended`, `revoked`, or `failed`
- `runtime_binding`
- `workspace`
- `agent_dir`
- `runtime_mode`
- `container`
- `created_at`
- `updated_at`
- `last_method`
- `last_reason`
- `warnings`

`.openclaw/soulfactory.json` is workspace-owned provenance and records the resolved SoulFactory params, operator request, controller pubkey, runtime pubkey, and spec hash. It must not contain private keys.

## Provision Flow

For `soulfactory.provision`, the wrapper:

1. Validates required params already accepted by the sidecar: `identity`, `runtime`, `permissions`, `relay_policy`, `workspace`, and `assets`.
2. Rejects if an existing local state file has the same `agent_id` with a different `spec_hash`, unless the existing state is already bound to the same SoulFactory operator request.
3. Resolves the containerized runtime target from `OPENCLAW_SOULFACTORY_RUNTIME_MODE`.
4. Creates the workspace directory.
5. Renders `IDENTITY.md`, `SOUL.md`, `AGENTS.md`, `MEMORY.md`, and `.openclaw/soulfactory.json`.
6. Runs `openclaw --container <container> agents add <agent-id> --workspace <workspace> --agent-dir <agent-dir> --non-interactive --json`, adding `--model` when configured or supplied by params. An equivalent `docker exec <container> openclaw agents add ...` path is acceptable.
7. Runs `openclaw --container <container> agents set-identity --agent <agent-id> --identity-file <workspace>/IDENTITY.md --json`.
8. Adds configured bindings with `openclaw --container <container> agents bind --agent <agent-id> --bind <binding> --json`.
9. Writes `state.json`, `last-invocation.json`, and `last-outcome.json`.

Success result:

```json
{
  "agent_id": "agent-alice",
  "runtime": "openclaw",
  "runtime_binding": "openclaw://agents/agent-alice",
  "state": "running",
  "spec_hash": "sha256:...",
  "workspace": "/home/majordomo/.openclaw/soulfactory/agents/agent-alice/workspace",
  "agent_dir": "/home/majordomo/.openclaw/soulfactory/agents/agent-alice/agent",
  "runtime_mode": "existing-container",
  "container": "openclaw-gateway",
  "warnings": []
}
```

## Update Flow

For `soulfactory.update`, the wrapper:

1. Requires an existing non-revoked state.
2. Validates `previous_spec_hash` matches current local state.
3. Applies `resolved_spec` or `patch` to the generated workspace files.
4. Updates identity when name, avatar, or identity metadata changes.
5. Records `new_spec_hash` as the active `spec_hash`.

The first MVP may support `resolved_spec` only and reject merge patches with `unsupported_method` or `missing_required_param` until patch semantics are implemented.

## Persona Flow

For `soulfactory.persona.update`, the wrapper:

1. Requires an existing non-revoked state.
2. Writes the normalized persona to `.openclaw/soulfactory-persona.json`.
3. Updates `SOUL.md` and `AGENTS.md` with the generated `system_prompt_override` and constraints.
4. Records `agent_defaults_patch` in `.openclaw/soulfactory.json`.

OpenClaw currently has no documented CLI command for hot-reloading agent defaults. Until one exists, the wrapper returns success with a warning when files are updated but live runtime reload is not confirmed.

## Suspend And Resume Flow

OpenClaw CLI currently exposes binding and deletion operations, but no explicit isolated-agent suspend or resume primitive.

For MVP:

- `soulfactory.suspend` records local state `suspended` and optionally removes configured route bindings with `openclaw agents unbind --agent <agent-id> --all --json`.
- `soulfactory.resume` records local state `running` and restores configured route bindings.
- Both methods return warnings that session cancellation/process stop is not enforced unless a future OpenClaw runtime stop/start command is configured.

The sidecar capability announcement should include these methods only when the operator accepts binding-level suspend semantics or when a real stop/start integration is available.

## Redeploy Flow

For `soulfactory.redeploy`, the wrapper:

1. Requires an existing non-revoked state.
2. Supports `strategy=restart` by refreshing identity and route bindings.
3. Rejects `strategy=rebuild` and `strategy=migrate` until OpenClaw exposes a host-local rebuild/migration command.

## Revoke Flow

For `soulfactory.revoke`, the wrapper:

1. Requires an existing state.
2. Removes route bindings with `openclaw agents unbind --agent <agent-id> --all --json`.
3. If `delete_workspace=true`, runs `openclaw agents delete <agent-id> --force --json`.
4. If `delete_workspace=false`, leaves workspace and agent state on disk and records local state `revoked`.

`revoke_runtime_credentials=true` is accepted only when the wrapper has a configured credential revocation implementation. Otherwise the wrapper records `revoked` and returns a warning that credential revocation was not performed.

## Error Mapping

The wrapper should produce these standard errors:

- `unsupported_method`: method is not implemented by this wrapper mode.
- `missing_required_param`: required local wrapper params are absent.
- `duplicate_conflict`: local state conflicts with the requested idempotency/spec identity.
- `spec_hash_mismatch`: update/redeploy expected hash does not match local state.
- `runtime_unavailable`: OpenClaw CLI is missing or unusable.
- `execution_failed`: OpenClaw CLI command failed after validation.

`rejected` means no local runtime mutation was performed. `failed` means validation passed but a local command failed or partially completed.

## Capability Advertisement

The sidecar should advertise only methods supported by the configured wrapper mode.

Recommended first-pass method set:

```text
soulfactory.provision
soulfactory.update
soulfactory.persona.update
soulfactory.suspend
soulfactory.resume
soulfactory.redeploy
soulfactory.revoke
```

Conservative first deployment on `max` should advertise:

```text
soulfactory.provision
soulfactory.persona.update
soulfactory.revoke
```

The broader method set can be enabled after suspend/resume/redeploy semantics are verified against the live OpenClaw runtime.

## Verification Plan

Use an isolated OpenClaw profile for smoke tests:

```bash
OPENCLAW_SOULFACTORY_ROOT=/tmp/openclaw-soulfactory-test \
OPENCLAW_SOULFACTORY_DRY_RUN=1 \
openclaw-soulfactory-control < fixtures/provision-invocation.json
```

Then run a non-dry-run test with `openclaw --profile soulfactory-test agents list --json` or an explicit test state directory so no production agent is touched.

Required checks:

- Valid provision creates deterministic workspace files and returns `state=running`.
- Exact replay returns the same logical outcome without conflicting state.
- Conflicting replay returns `duplicate_conflict`.
- Persona update writes persona/system prompt files and preserves runtime binding.
- Revoke without workspace deletion unbinds and preserves audit files.
- Revoke with workspace deletion removes the OpenClaw isolated agent only in an isolated test profile.

## Open Questions

- Whether OpenClaw should grow explicit `agents suspend` and `agents resume` commands.
- Whether persona/default updates can be hot-reloaded through gateway-local RPC instead of waiting for process/session restart.
- Whether route bindings should come from SoulFactory `permissions`, OpenClaw wrapper config, or both with policy intersection.
- Whether generated workspaces should be git-initialized by the wrapper or left to SoulFactory/Bahia workspace provisioning.
