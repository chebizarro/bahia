# OpenClaw SoulFactory Wrapper Orchestration — 2026-06-11

Source specs:
- `docs/openclaw-soulfactory-sidecar.md`
- `docs/openclaw-soulfactory-control-wrapper.md`

Planning export recovered from ignored `prompt-exports/oracle-plan-2026-06-11-073500-openclaw-wrapper-pla-c93c.md`.

## Context

Existing Bahia flow already has Nostr-native SoulFactory provisioning, runtime capability discovery, `38384` runtime control requests, sidecar validation/idempotency, and `38386` result publication. The missing implementation boundary is the host-local stdin/stdout command expected by `OpenClawCommandDriver`:

```text
cmd/openclaw-soulfactory-sidecar -> OpenClawCommandDriver -> openclaw-soulfactory-control
```

Closed MVP/sidecar Beads are historical context only and should not be reopened: `bahia-0wc4`, `bahia-23at`, `bahia-euu6`, `bahia-2r4y`, `bahia-8ycd.4`.

## Work items

- [x] Item 1 — Wrapper core command/package
  - Goal: Implement `cmd/openclaw-soulfactory-control` plus `internal/soulfactory/openclawcontrol` for env config, stdin/stdout contract, dry-run, deterministic state/workspace layout, provision/persona.update/revoke, and error mapping.
  - Done when: dry-run provision creates state/workspace/outcome; replay is idempotent; spec conflicts reject; persona update and revoke behave as specified; forbidden persistent bare-metal runtime commands are absent; focused tests pass.
  - Key files: `cmd/openclaw-soulfactory-control`, `internal/soulfactory/openclawcontrol`, `internal/soulfactory/openclaw_sidecar.go`, `internal/soulfactory/runtime_adapter.go`.
  - Dependencies: none.
  - Size: large.

- [x] Item 2 — Packaging and sidecar method advertisement
  - Goal: Build/package wrapper alongside sidecar and make sidecar command-driver capability advertisements conservative/configurable.
  - Done when: Makefile and Dockerfile include wrapper binary; sidecar supports configured method list; default command-driver methods match conservative wrapper-supported set; tests/build target pass.
  - Key files: `Makefile`, `Dockerfile`, `cmd/openclaw-soulfactory-sidecar/main.go`, `internal/soulfactory/openclaw_sidecar.go`, `internal/soulfactory/openclaw_sidecar_test.go`.
  - Dependencies: Item 1 for final wrapper binary target, but sidecar method work can proceed concurrently if coordinated.
  - Size: medium.

- [x] Item 3 — Sidecar-to-wrapper smoke coverage
  - Goal: Prove the existing sidecar can invoke the real wrapper through `OpenClawCommandDriver` in dry-run and publish correlated `38386` results.
  - Done when: signed `38384` request invokes wrapper, temp-root wrapper state is created, sidecar publishes signed correlated `38386`, replay uses cached result without invoking wrapper again, idempotency conflict is covered without sleeps/timeouts.
  - Key files: `internal/soulfactory/openclaw_sidecar_test.go`, `cmd/openclaw-soulfactory-control`, `internal/soulfactory/openclawcontrol`.
  - Dependencies: Item 1; Item 2 if tests require built binary.
  - Size: medium.

- [x] Item 4 — PSTF artifacts and fixtures
  - Goal: Create `pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/` with feature spec, acceptance criteria, test matrix, verification report, and invocation fixtures.
  - Done when: every wrapper criterion maps to deterministic tests; verification report records exact commands/results; open questions become HITL or Beads.
  - Key files: `pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/*`.
  - Dependencies: Can scaffold early; final verification depends on Items 1–3.
  - Size: medium.

- [x] Item 5 — Docs, closeout, commit/push
  - Goal: Align docs with implemented wrapper behavior and complete Beads/PSTF/quality-gate/git closeout.
  - Done when: docs state supported methods and preserve no-REST/no-bare-metal doctrine; remaining OpenClaw semantic gaps are tracked in Beads; focused/full quality gates run as appropriate; changes are committed and pushed; git status is clean/up-to-date.
  - Key files: `docs/openclaw-soulfactory-sidecar.md`, `docs/openclaw-soulfactory-control-wrapper.md`, possibly `docs/user-guide/features/souls.md`, Beads/PSTF artifacts.
  - Dependencies: Items 1–4.
  - Size: medium.

## Beads created

- `bahia-7yf9` — Implement OpenClaw SoulFactory local control wrapper
- `bahia-zqn9` — Package and advertise OpenClaw wrapper support safely
- `bahia-j26z` — Verify sidecar-to-wrapper dry-run runtime-control smoke path
- `bahia-7qt9` — Add PSTF coverage for OpenClaw SoulFactory control wrapper
- `bahia-hvbs` — Close out OpenClaw wrapper rollout docs and verification

Dependency chain:
- `bahia-zqn9` depends on `bahia-7yf9`.
- `bahia-j26z` depends on `bahia-7yf9` and `bahia-zqn9`.
- `bahia-7qt9` depends on `bahia-7yf9`.
- `bahia-hvbs` depends on `bahia-7yf9`, `bahia-zqn9`, `bahia-j26z`, and `bahia-7qt9`.

## Agent progress

_To be updated by orchestrator as sub-agents complete._
