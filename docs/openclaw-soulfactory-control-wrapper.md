# OpenClaw SoulFactory Control Wrapper Spec

`openclaw-soulfactory-control` is the host-local command used by the Bahia-owned OpenClaw SoulFactory sidecar. The sidecar owns Nostr validation, idempotency, capability publication, and result publication. The wrapper owns only local OpenClaw runtime effects.

This command is intentionally not a REST API. It receives one `OpenClawControlInvocation` JSON document on stdin and writes one `OpenClawControlOutcome` JSON document on stdout. It exits after producing the outcome; it does not run a persistent service.

## Implemented scope

The packaged wrapper currently supports exactly these runtime-control methods:

```text
soulfactory.provision
soulfactory.update
soulfactory.persona.update
soulfactory.revoke
```

Any other method, including `soulfactory.suspend`, `soulfactory.resume`, and `soulfactory.redeploy`, is rejected with a structured `unsupported_method` outcome by the wrapper unless a different command implementation is explicitly configured. The sidecar command driver also defaults to the same conservative method set, so unsupported methods are not advertised by default. `soulfactory.update` applies optimistic spec-hash checks and accepts either `update_mode=replace` with `resolved_spec`, or `update_mode=merge` with a patch over the persisted canonical prior spec.

The wrapper supports dedicated execution and a compatibility-only dry-run mode:

- **Dry run**: `OPENCLAW_SOULFACTORY_DRY_RUN=1` validates input, renders deterministic workspace/state/audit files, and returns the same outcome shape without invoking the OpenClaw CLI.
- **Dedicated runtime**: `OPENCLAW_SOULFACTORY_RUNTIME_MODE=per-agent-compose` creates or adopts one owned Docker Compose gateway for the requested soul.

Non-dry-run `existing-container` provisioning is rejected. This prevents an external soul from being inserted into or mutating an incumbent gateway such as Marjam or SNR.

## Goals

- Provision or bind an OpenClaw isolated agent from a SoulFactory runtime control request.
- Apply a complete or merge-patched desired spec to an existing managed agent.
- Persist enough local state for deterministic idempotent replay and safe revoke decisions.
- Update generated persona/system prompt files for an existing OpenClaw soul.
- Revoke an existing OpenClaw soul by unbinding routes and optionally deleting the workspace.
- Return structured success, rejected, or failed outcomes compatible with `soulfactory-runtime-control/v1`.
- Keep runtime side effects auditable under the generated agent workspace.
- Keep persistent OpenClaw runtime execution containerized.

## Non-goals

- Do not expose HTTP lifecycle routes.
- Do not publish Nostr events. The sidecar already signs and publishes `30317` and `38386`.
- Do not mint private keys inline in generated config.
- Do not create fake Bahia deployable artifacts, image tags, or digests.
- Do not silently accept unimplemented lifecycle methods.
- Do not delete user data unless `soulfactory.revoke` explicitly asks for workspace deletion.
- Do not launch persistent OpenClaw gateway or agent runtime processes directly on bare metal.

## Runtime deployment doctrine

Provisioned OpenClaw souls run under Docker, not as bare-metal user services or background processes. The wrapper creates a deterministic, ownership-labelled Compose project and gateway with separate persistent config, agent, and workspace mounts.

The wrapper does not run `openclaw gateway run`, `openclaw gateway start`, `go run`, `npm start`, or a user-level systemd service on the host. Agent configuration commands target only the newly reconciled owned container:

```text
openclaw --container <container> agents add ...
openclaw --container <container> agents set-identity ...
openclaw --container <container> agents bind ...
openclaw --container <container> agents unbind ...
openclaw --container <container> agents delete ...
```

Plugins are installed only in the dedicated gateway. A missing required plugin
is installed, the owned gateway is restarted, readiness is reconciled, and the
plugin inventory must report it loaded before agent creation continues.

The current `openclaw-nostr` configuration resolver exposes one top-level
Nostr identity per gateway. A shared gateway can therefore route that identity
to one soul, but must not be used to present multiple independent Signet
identities. Fleet onboarding that requires one Nostr identity per soul must
use a gateway/container per soul until the plugin supports multiple configured
accounts end to end.

## Packaging path

`openclaw-soulfactory-control` is built and packaged with the sidecar:

```bash
make build-openclaw-soulfactory-control
make build-openclaw-soulfactory-sidecar
# or
make build
```

The Bahia Docker image builds both binaries and copies them to:

```text
/usr/local/bin/openclaw-soulfactory-sidecar
/usr/local/bin/openclaw-soulfactory-control
```

## Command interface

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

Valid statuses are `success`, `rejected`, and `failed`. The wrapper returns structured JSON for both successful and rejected/failed invocations.

Malformed input or trailing JSON produces `failed` with `execution_failed`. Unsupported methods produce `rejected` with `unsupported_method` before local runtime mutation.

## Configuration

The wrapper reads configuration from environment variables:

- `OPENCLAW_SOULFACTORY_ROOT`: base directory for generated agent workspaces and state. Default: `~/.openclaw/soulfactory`.
- `OPENCLAW_SOULFACTORY_OPENCLAW_BIN`: OpenClaw CLI path. Default: `openclaw`.
- `OPENCLAW_SOULFACTORY_RUNTIME_MODE`: default `per-agent-compose`. `existing-container` is accepted only for dry-run compatibility and rejected for non-dry-run external provisioning.
- `OPENCLAW_SOULFACTORY_DOCKER_BIN`: Docker CLI path. Default: `docker`.
- `OPENCLAW_SOULFACTORY_IMAGE`: OpenClaw image reference pinned as `repository@sha256:<64 hex>`; required for non-dry-run provisioning.
- `OPENCLAW_SOULFACTORY_SOURCE_COMMIT`: pinned lowercase hexadecimal OpenClaw source commit; required for non-dry-run provisioning.
- `OPENCLAW_SOULFACTORY_CPUS`, `OPENCLAW_SOULFACTORY_MEMORY`, `OPENCLAW_SOULFACTORY_PIDS_LIMIT`: per-gateway resource limits. Defaults: `1.0`, `1g`, and `256`.
- `OPENCLAW_SOULFACTORY_DEFAULT_MODEL`: fallback model for `openclaw agents add --model`; non-dry-run provisioning requires either this value or `runtime.model`.
- `OPENCLAW_SOULFACTORY_DEFAULT_BINDINGS`: comma-separated channel bindings to add on non-dry-run provision.
- `OPENCLAW_SOULFACTORY_REQUIRED_PLUGINS`: comma-separated `plugin-id=install-source` requirements. Non-dry-run external souls require an explicit `nostr=<pinned-install-source>` entry.
- `OPENCLAW_SOULFACTORY_DRY_RUN`: when truthy (`1`, `true`, `yes`, `y`, or `on`), validate and render state but skip OpenClaw CLI mutations.

The resolved provision params supply `runtime.model`, `runtime.account_id` (or the Signet-created `bahia.nostr_pubkey`), and optional public `runtime.nostr` channel configuration. `runtime.secret_files` maps secret names to absolute host file paths. Files must be regular, owned by the wrapper user, and mode `0600` or stricter; recognized `nip46_client` and `nip46_connect` files become OpenClaw file SecretRefs under `/run/secrets`. Inline tokens, passwords, private keys, and NIP-46 secrets are rejected.

For the Lemmy-hosted Gemma 4 deployment, set:

```text
OPENCLAW_SOULFACTORY_DEFAULT_MODEL=lemmy-local/google_gemma-4-26B-A4B-it-Q4_K_M.gguf
OPENCLAW_SOULFACTORY_REQUIRED_PLUGINS=nostr=npm:openclaw-nostr
```

Plugin installation does not provision a Nostr identity. The container must
also receive a file-backed Signet NIP-46 client key and one-time pairing secret,
apply a deny-by-default Signet policy, bind `nostr:<account-id>` to the agent,
restart once to adopt the binding, remove the consumed pairing secret, and
prove a second restart resumes from the durable client key.

The sidecar injects these variables when it invokes the command:

- `SOULFACTORY_METHOD`
- `SOULFACTORY_AGENT_ID`
- `SOULFACTORY_SPEC_HASH`

The sidecar's command-driver method advertisement can be configured with `-methods` or `OPENCLAW_SOULFACTORY_METHODS`. Leave it unset, or set it to the implemented conservative set, unless the configured command really supports additional methods:

```bash
openclaw-soulfactory-sidecar \
  -command /usr/local/bin/openclaw-soulfactory-control \
  -methods soulfactory.provision,soulfactory.update,soulfactory.persona.update,soulfactory.revoke
```

## Local state layout

Each managed agent gets a deterministic directory:

```text
$OPENCLAW_SOULFACTORY_ROOT/
  agents/
    <agent-id>/
      agent/
      workspace/
        SOUL.md
        IDENTITY.md
        MEMORY.md
        AGENTS.md
        .openclaw/
          soulfactory.json
          soulfactory-persona.json
      state.json
      last-invocation.json
      last-outcome.json
```

`state.json` is wrapper-owned and records:

- `agent_id`
- `soul_id`
- `spec_hash`
- `state`: `running`, `revoked`, or `failed`
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
- operator/controller/runtime correlation fields when present in the invocation envelope

`.openclaw/soulfactory.json` is workspace-owned provenance and records the resolved SoulFactory params, operator request, controller pubkey, runtime pubkey, and spec hash. It must not contain private keys, an agent Signet bunker URI, or bunker connection secrets. SoulFactory removes that material before building the relay-visible runtime checkpoint; the wrapper also rejects inline private-secret fields in params.

## Provision flow

For `soulfactory.provision`, the wrapper:

1. Validates required params already accepted by the sidecar: `identity`, `runtime`, `permissions`, `relay_policy`, `workspace`, and `assets`.
2. Rejects unsafe `agent_id` values and inline private secret material before local mutation.
3. Rejects if an existing local state file has the same `agent_id` with a different `spec_hash`, or if the request is not an exact replay of the recorded invocation.
4. Requires dedicated mode, an immutable image digest, pinned source commit, exact Nostr account ID, and explicit Nostr plugin requirement.
5. Derives collision-safe project/container names from agent, operator-request, and idempotency/run IDs.
6. Renders identity files, OpenClaw config with plugin allowlist and file SecretRefs, ownership metadata, and a resource-limited Compose specification.
7. In dry-run, skips OpenClaw CLI mutations and records a warning.
8. Bootstraps missing required plugins in an ephemeral owned Compose container, then inspects and reconciles the gateway to health.
9. Verifies loaded plugins, creates the selected-model agent, sets identity, and binds exactly `nostr:<account-id>`.
10. Writes state/audit files with container/deployment ID, immutable image, config revision, workspace identifier, and agent/account lineage.

Exact replays return the same logical outcome without repeating mutation work. Conflicting replays reject with `duplicate_conflict`.

## Persona update flow

For `soulfactory.persona.update`, the wrapper:

1. Requires existing non-revoked state.
2. Requires the invocation `spec_hash` to match local state.
3. Parses SoulFactory persona runtime params.
4. Writes normalized persona data to `.openclaw/soulfactory-persona.json`.
5. Updates `SOUL.md` and `AGENTS.md` with the generated system prompt.
6. Records `agent_defaults_patch` and `last_persona_update` in `.openclaw/soulfactory.json`.
7. Writes state/audit files and returns success with a warning that live OpenClaw hot reload is not confirmed by the current CLI.

OpenClaw currently has no documented CLI command for hot-reloading agent defaults, so this method updates local workspace/runtime files and reports the limitation explicitly.

## Revoke flow

For `soulfactory.revoke`, the wrapper:

1. Requires existing local state.
2. Requires `reason` and `revoke_runtime_credentials` params.
3. Requires the invocation `spec_hash` to match local state.
4. Verifies persisted dedicated-resource ownership, unbinds the owned agent, and deletes only its matching Compose deployment.
5. If `delete_workspace=true`, removes that soul's generated workspace, config, and agent directories after the deployment is removed.
6. Records local state `revoked`, preserves audit files, and writes `last-invocation.json` / `last-outcome.json`.
7. If `revoke_runtime_credentials=true`, returns success with a warning that credential revocation was requested but no OpenClaw credential-revocation command is configured.

Exact replay of a successful or failed revoke uses the persisted outcome/state rather than repeating local commands.

## Error mapping

The wrapper produces these standard errors:

- `unsupported_method`: method is not implemented by this wrapper.
- `missing_required_param`: required local wrapper params or configuration are absent.
- `duplicate_conflict`: local state conflicts with the requested idempotency/spec identity.
- `spec_hash_mismatch`: persona update or revoke expected hash does not match local state.
- `runtime_unavailable`: OpenClaw CLI is missing or unusable.
- `execution_failed`: OpenClaw CLI command failed after validation, audit persistence failed, or input JSON was malformed.

`rejected` means no local runtime mutation was performed. `failed` means validation passed but a local command failed, input could not be decoded into the command contract, or a persistence step failed.

## Restart and reconciliation behavior

The wrapper persists `state.json`, `last-invocation.json`, and `last-outcome.json`; the sidecar separately persists the `38384` idempotency fingerprint and signed-result payload. Exact replay after either process restarts returns/republishes the cached logical result without repeating OpenClaw commands. A reused key with different request identity or spec is rejected.

Bahia may reconcile a valid late success `38386` after an earlier deploy-stage runtime timeout. That server-side path rebuilds the final public soul from the secret-free checkpoint embedded in `38384`; it does not call the wrapper or replay earlier provisioning stages.

## Verification plan

Use an isolated root for dry-run smoke tests:

```bash
OPENCLAW_SOULFACTORY_ROOT=/tmp/openclaw-soulfactory-test \
OPENCLAW_SOULFACTORY_DRY_RUN=1 \
openclaw-soulfactory-control < pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/provision-invocation.json
```

Required checks:

- Valid dry-run provision creates deterministic workspace files and returns `state=running`.
- Exact provision replay returns the same logical outcome without conflicting state.
- Conflicting provision replay returns `duplicate_conflict`.
- Unsupported methods return `unsupported_method`.
- Persona update writes persona/system prompt files, preserves runtime binding, and reports the hot-reload warning.
- Revoke without workspace deletion records revoked state and preserves audit files.
- Revoke with workspace deletion removes the generated workspace directory while preserving wrapper audit files.
- Non-dry-run provision uses `openclaw --container <container> ...` and does not run persistent bare-metal runtime commands.
- Sidecar-to-wrapper smoke verification proves signed `38384` request handling, wrapper invocation, correlated signed `38386` publication, cached replay, and conflicting idempotency-key rejection.

Run the focused gates:

```bash
python3 - <<'PY'
import json
from pathlib import Path
base = Path('pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER')
for path in sorted(base.rglob('*.json')):
    with path.open() as f:
        json.load(f)
    print(path)
PY

go test ./cmd/openclaw-soulfactory-control ./internal/soulfactory/openclawcontrol ./internal/soulfactory -count=1
make build-openclaw-soulfactory-control build-openclaw-soulfactory-sidecar
```
