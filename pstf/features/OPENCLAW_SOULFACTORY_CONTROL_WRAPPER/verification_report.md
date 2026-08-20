# Verification Report — OPENCLAW_SOULFACTORY_CONTROL_WRAPPER

## Scope

This report covers PSTF mapping and fixtures for `openclaw-soulfactory-control`, the host-local command wrapper invoked by the Bahia OpenClaw SoulFactory sidecar.

The wrapper itself was implemented by Item 1. Item 4 / `bahia-7qt9` created the initial PSTF artifacts; Item 3 / `bahia-j26z` adds deterministic sidecar-to-wrapper smoke verification without changing packaging, user docs, or git history.

## Acceptance mapping

- OCSCW-AC-001: `TestRunReadsInvocationAndWritesOutcome`, `TestRunRejectsTrailingJSONDocument`, and `TestRunMalformedJSONReturnsStructuredFailure`.
- OCSCW-AC-002: `TestProvisionDryRunCreatesWorkspaceStateAndReplaysIdempotently`.
- OCSCW-AC-003: `TestProvisionRejectsDuplicateSpecConflict` and `TestProvisionRejectsUnsafeAgentIDAndInlinePrivateSecret`.
- OCSCW-AC-004: `TestPersonaUpdateWritesPersonaAndWarning`.
- OCSCW-AC-005: `TestRevokeRecordsStateAndHonorsWorkspaceDeletion` and `TestNonDryRunRevokeUsesPersistedContainerAndReplaysWithoutCommands`.
- OCSCW-AC-006: `TestUnsupportedMethodRejected`.
- OCSCW-AC-007: `TestNonDryRunUsesContainerizedOpenClawCommands`.
- OCSCW-AC-008: `TestCommandFailurePersistsFailedAuditState`.
- OCSCW-AC-009: `TestOpenClawSidecarCommandDriverInvokesWrapperDryRunAndCachesReplay`, `TestOpenClawSidecarRejectsIdempotencyReuseWithChangedParams`, and `TestOpenClawSidecarReportsIdempotencyPersistenceFailure`.
- OCSCW-AC-010: `TestProvisionRequiresLoadedRuntimePluginBeforeAgentMutation`, `TestProvisionContinuesWhenRequiredRuntimePluginIsLoaded`, and `TestRequiredPluginConfigRejectsAmbiguousInstallSource`.

## Runtime plugin and Marjam vertical slice — 2026-08-06

The wrapper now treats channel plugins as shared-runtime prerequisites. The
focused source was formatted and tested with Go 1.26.3 in an ephemeral
container. The container used the operator's existing read-only Git credential
file to fetch private module `git.sharegap.net/cascadia/cascadia-go` v1.0.1;
credential values were neither printed nor persisted in the repository.

```text
go test ./internal/soulfactory/openclawcontrol -count=1
ok github.com/openagentsinc/bahia/internal/soulfactory/openclawcontrol 0.153s
```

The broader wrapper command package also passed. The existing
`internal/soulfactory` package remains red for two baseline issues outside this
change: `TestOpenClawCommandDriverDefaultsToWrapperSupportedMethods` expects a
method list that omits the already-implemented `soulfactory.update`, and the
nested wrapper build requires `-buildvcs=false` when the worktree is mounted
without its parent Git metadata. These failures were not treated as evidence
for or against OCSCW-AC-010.

Live vertical-slice evidence on the existing Soul Factory runtime:

- `openclaw-nostr` plugin id `nostr` reports loaded and enabled and is pinned in `plugins.allow`.
- Marjam uses `lemmy-local/google_gemma-4-26B-A4B-it-Q4_K_M.gguf`; a live inbound Nostr DM invoked that model and returned HTTP 200.
- Signet reports one active persistent client for `marjam`.
- The one-time pairing secret was removed after adoption; a second gateway restart resumed NIP-46 and connected to both configured relays without connect fallback.
- Marjam is bound to `nostr:marjam`; an allowlisted NIP-17 canary DM was received and produced a signed self-echo reply event.

Scale caveat: the installed plugin currently resolves only one configured
top-level Nostr identity per gateway. Independent Soul Factory identities need
per-soul gateways until multi-account configuration is implemented end to end.

## Fixture coverage

Fixtures under `fixtures/` provide deterministic invocation and outcome examples for:

- `soulfactory.provision`
- `soulfactory.persona.update`
- `soulfactory.revoke` with `delete_workspace=false`
- `soulfactory.revoke` with `delete_workspace=true`

Fixture paths use `/tmp/openclaw-soulfactory-pstf` as an isolated verification root. Operators running fixtures should override `OPENCLAW_SOULFACTORY_ROOT` to an isolated temporary directory for local verification.

## Verification commands

Commands for this PSTF slice:

```text
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/feature_spec.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/acceptance_criteria.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/test_matrix.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/defects.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/provision-invocation.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/provision-success-outcome.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/persona-update-invocation.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/persona-update-success-outcome.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/revoke-keep-workspace-invocation.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/revoke-keep-workspace-success-outcome.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/revoke-delete-workspace-invocation.json
python3 -m json.tool pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/revoke-delete-workspace-success-outcome.json

go test ./cmd/openclaw-soulfactory-control ./internal/soulfactory/openclawcontrol ./internal/soulfactory
```

Recorded results:

```text
python3 - <<'PY'
import json
from pathlib import Path
base = Path('pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER')
for path in sorted(base.rglob('*.json')):
    with path.open() as f:
        json.load(f)
    print(path)
PY
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/acceptance_criteria.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/defects.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/feature_spec.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/persona-update-invocation.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/persona-update-success-outcome.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/provision-invocation.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/provision-success-outcome.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/revoke-delete-workspace-invocation.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/revoke-delete-workspace-success-outcome.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/revoke-keep-workspace-invocation.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/fixtures/revoke-keep-workspace-success-outcome.json
pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/test_matrix.json

go test ./cmd/openclaw-soulfactory-control ./internal/soulfactory/openclawcontrol ./internal/soulfactory
ok  	github.com/openagentsinc/bahia/cmd/openclaw-soulfactory-control	0.266s
ok  	github.com/openagentsinc/bahia/internal/soulfactory/openclawcontrol	0.695s
ok  	github.com/openagentsinc/bahia/internal/soulfactory	8.094s
```

## Final closeout verification — 2026-06-11

Item 5 / `bahia-hvbs` reran focused docs/source/PSTF/build checks after aligning documentation to the implemented conservative wrapper behavior. A focused Oracle review found three rollout implementation gaps, which were fixed before final verification: params-only idempotency conflicts are now detected, file idempotency-store persistence failures now publish failed `38386` outcomes instead of silently dropping errors, and inline private secret detection now rejects common Nostr private-key fields/values while allowing `*_ref` references.

Commands and results:

```text
python3 -c 'import json; from pathlib import Path; base=Path("pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER");
for p in sorted(base.rglob("*.json")):
    json.load(p.open()); print(p)'
# PASS: all OPENCLAW_SOULFACTORY_CONTROL_WRAPPER JSON artifacts and fixtures parsed.

! grep -R "first MVP\|recommended first-pass\|records local state suspended\|records local state running and restores\|Supports strategy=restart" docs/openclaw-soulfactory-control-wrapper.md docs/openclaw-soulfactory-sidecar.md docs/user-guide/features/souls.md
# PASS: stale aspirational lifecycle wording was absent from updated docs.

! grep -R "openclaw gateway run\|openclaw gateway start\|npm start\|user-level systemd" internal/soulfactory/openclawcontrol/control.go cmd/openclaw-soulfactory-control/main.go cmd/openclaw-soulfactory-sidecar/main.go internal/soulfactory/openclaw_sidecar.go
# PASS: touched production source did not contain persistent bare-metal OpenClaw runtime launch commands.

go test ./cmd/openclaw-soulfactory-control ./internal/soulfactory/openclawcontrol ./internal/soulfactory -count=1
ok  	github.com/openagentsinc/bahia/cmd/openclaw-soulfactory-control	0.259s
ok  	github.com/openagentsinc/bahia/internal/soulfactory/openclawcontrol	0.443s
ok  	github.com/openagentsinc/bahia/internal/soulfactory	8.024s

GOCACHE=/tmp/bahia-go-build-cache GOMODCACHE=/tmp/bahia-go-mod-cache make build-openclaw-soulfactory-control build-openclaw-soulfactory-sidecar
# PASS: built bin/openclaw-soulfactory-control and bin/openclaw-soulfactory-sidecar.

GOCACHE=/tmp/bahia-go-build-cache GOMODCACHE=/tmp/bahia-go-mod-cache go test ./... -count=1
# FAIL: unrelated existing migration-manifest coverage gap:
# internal/nostrmigration TestKindConstantsAreMappedOrJustified reports
# internal/kinds.LongFormDraft=30024 is neither mapped in the migration manifest nor explicitly justified.
# The rerun confirmed OpenClaw packages passed in the broad suite:
# ok github.com/openagentsinc/bahia/internal/soulfactory 9.197s
# ok github.com/openagentsinc/bahia/internal/soulfactory/openclawcontrol 2.032s
# Tracked as Bead bahia-8j5h.
```

The initial focused build target emitted sandbox-related Go module stat-cache warnings when using the default user module cache, so the recorded packaging verification above reran with `GOCACHE` and `GOMODCACHE` in `/tmp` and passed cleanly.

## Results

Final OpenClaw wrapper closeout result: all PSTF JSON artifacts and fixtures parsed successfully; focused wrapper CLI/package tests passed; the sidecar smoke path passed for signed `38384` wrapper invocation, temp-root state creation, signed correlated `38386` publication, cached replay, idempotency-conflict rejection including params-only conflicts, and failed-result publication when idempotency persistence fails; updated docs now state the implemented conservative method set, dry-run behavior, existing-container/container-targeted behavior, no REST lifecycle API, no persistent bare-metal runtime, explicit unsupported-method rejection, sidecar `-methods` / env configuration, and package/build paths.

No OpenClaw wrapper PSTF defects remain. The only broad-suite failure observed during closeout is tracked separately as `bahia-8j5h` because it concerns `internal/nostrmigration` kind-manifest coverage for `LongFormDraft=30024`, not the OpenClaw wrapper rollout.
