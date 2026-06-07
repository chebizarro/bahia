# Adoption / Import Signer-First Operator Checklist and Evidence Template

Status: **Primary signer-first execution checklist**
Epic: `bahia-sqfx`
Verification/signoff execution: `bahia-sqfx.5`

This document is the operator run sheet and evidence template for staged/live signer-first rollout verification of adoption/import and direct-runtime actions.

It is now the primary execution checklist for this feature set.
Legacy HTTP/NIP-98 operator verification remains compatibility-only and secondary.

## Gate policy

For operator-only signer-first slices, evidence is now layered:

1. deterministic in-repo verification establishes implementation behavior;
2. a stored local Docker+relay rehearsal artifact is required before release approval;
3. staged/live execution of SF-01 through SF-11 remains required for production enablement.

This checklist covers the staged/live layer. The rehearsal artifact is mandatory input to release approval, but it does not replace staged/live signoff.

## Scope

Primary control surface under test:
- signer-first adoption scan/import over public Nostr control-plane requests
- signer-first direct-runtime `deploy` / `restart` / `stop`
- operator pubkey authorization
- status/result correlation via relay subscriptions
- managed endpoint governance, redaction, rollback, concurrency, observability, and disable/rollback checks

Secondary / compatibility-only:
- legacy privileged HTTP/NIP-98 operator endpoints
- raw Docker host request mode behind explicit break-glass/fallback behavior

## Operator instructions

1. Execute from the release commit proposed for rollout.
2. Capture evidence for every row.
3. For each failure or blocker:
   - stop at the first unsafe point;
   - capture evidence;
   - file a follow-up issue;
   - mark the row `FAIL` or `BLOCKED`;
   - do **not** mark production ready.
4. If a row is not applicable, record `N/A`, explain why, and capture approver signoff.
5. Do not paste raw secrets, private keys, bunker secrets, cert material, or unredacted sensitive env values into this document.

## Run metadata

| Field | Value |
| --- | --- |
| Release commit SHA | `<fill>` |
| Target environment | `<fill>` |
| Bahia API base URL | `<fill>` |
| Control-plane relay URL(s) under test | `<fill>` |
| Signer mode under test | `local keyer` / `NIP-46` / `<fill>` |
| Operator pubkey(s) | `<fill redacted if needed>` |
| Execution start (UTC) | `<fill>` |
| Execution end (UTC) | `<fill>` |
| Primary operator | `<fill>` |
| Additional approvers | `<fill>` |
| Evidence bundle location | `<fill>` |
| Local rehearsal artifact bundle | `<fill path + timestamp>` |
| Managed endpoint refs under test | `<fill>` |
| Compose takeover policy for this environment | `disabled` / `enabled for named services only` / `not applicable` |
| ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) capability evidence | `<fill path + timestamp>` |
| Relay `/relay` reachability evidence | `<fill path + timestamp or N/A>` |
| Signer capability evidence (`signEvent`, request/result correlation path) | `<fill>` |
| Compatibility explicit relay configuration approved? | `yes` / `no` |

## Environment prerequisites

Check each box before SF-01 starts.

- [ ] Release commit is deployed to staging/live-like environment.
- [ ] Signer-first operator transport for adoption/import/direct-runtime is enabled.
- [ ] Operator signer is available and can publish signed request events.
- [ ] Operator pubkeys are allowlisted for signer-first adoption/import/direct-runtime actions.
- [ ] At least two `runtime.endpoints.<ref>` aliases are configured.
- [ ] At least one endpoint uses remote Docker TLS/mTLS.
- [ ] `adoption.allow_raw_docker_hosts=false` unless explicit break-glass testing is approved.
- [ ] ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) and relay/topology evidence are captured for the release candidate.
- [ ] A stored local Docker+relay signer-first rehearsal artifact exists for this release commit.
- [ ] A non-critical candidate workload is available for import.
- [ ] Rollback owner/procedure for the candidate workload is confirmed.
- [ ] Event/status/result subscriptions and observability capture are available for operators.

## Evidence bundle requirements

Collect these artifacts as files or links under the evidence bundle location.

- [ ] Release manifest / deployment record
- [ ] Redacted staging config excerpt
- [ ] Signer/operator setup notes (no secrets)
- [ ] Local rehearsal artifact bundle (Docker+relay simulation)
- [ ] CLI transcript showing signer-first request publication and terminal result handling
- [ ] Request event IDs and correlated status/result event IDs
- [ ] ContextVM discovery (`11316`-`11320`) plus NIP-51 relay sets (`30002`) capture
- [ ] Relay `/relay` reachability capture when sidecar validation is in scope
- [ ] Relevant logs with request IDs / event IDs / actor pubkey
- [ ] Metrics snapshots
- [ ] Database row IDs / imported entity references
- [ ] Follow-up issue IDs for failures/blockers
- [ ] Final approver signoff note

## Automated gate confirmation

Record release-commit results before manual SF rows begin.

| Check | Command | Result (`PASS`/`FAIL`) | Evidence |
| --- | --- | --- | --- |
| Config and operator allowlist safety | `go test ./internal/config` | `<fill>` | `<fill>` |
| Signer-first control-plane operator transport | `go test ./internal/controlplane` | `<fill>` | `<fill>` |
| Managed endpoint governance and redaction | `go test ./internal/service -run 'TestAdoptionService.*Endpoint|TestAdoptionServiceRejectsRaw|TestAdoptionService.*Sensitive|Redacts' && go test ./internal/api/handlers -run Adoption` | `<fill>` | `<fill>` |
| Transactional import and idempotency | `go test ./internal/service -run 'TestAdoptionServiceImportTransactional|TestAdoptionServiceImportRetries|TestAdoptionServiceImportSeeds'` | `<fill>` | `<fill>` |
| Direct-runtime guardrails | `go test ./internal/service -run RuntimeLifecycle && go test ./internal/api/handlers -run RuntimeLifecycle` | `<fill>` | `<fill>` |
| Signer-first operator client contract | `go test ./pkg/client -run 'Operator|SystemInfo|Adoption|RuntimeAction'` | `<fill>` | `<fill>` |
| Signer-first CLI workflow | `go test ./cmd/cli -run 'Operator|Adoption'` | `<fill>` | `<fill>` |
| Full Go regression | `go test ./...` | `<fill>` | `<fill>` |

## Manual execution template

Use one section per SF row. Fill every field.

### SF-01 — Signer/operator authorization

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - malformed or unsigned request is rejected
  - valid signed non-operator request is rejected
  - valid signed operator request is accepted
  - status/result events correlate to the originating request event id and operator pubkey
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### SF-02 — Relay routing and result/status correlation

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - signer-first request reaches the intended relay path/topology
  - status/result events correlate to the request event id
  - duplicate or unrelated events are ignored safely
  - operator sees terminal success/failure without HTTP polling assumptions
- Evidence paths: `<fill>`
- Notes: `<fill>`

### SF-03 — Multi-host managed endpoint scan

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  bahia adopt scan --target <host-a> --target <host-b>
  ```
- Checks:
  - scan can target at least two managed endpoint refs
  - request/result surfaces use endpoint refs only
  - no raw Docker host or cert material leaks into operator-visible surfaces
- Evidence paths: `<fill>`
- Notes: `<fill>`

### SF-04 — Raw-host rejection / compatibility boundary

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  bahia adopt scan --raw-target breakglass=tcp://127.0.0.1:2375
  ```
- Checks:
  - signer-first path rejects raw-host usage
  - compatibility explicit relay configuration requires explicit `--http-fallback`
  - no unmanaged runtime call occurs when raw-host mode is disabled
- Evidence paths: `<fill>`
- Notes: `<fill>`

### SF-05 — Redaction / secret import

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - scan/import responses expose only safe values and redacted key names
  - imported sensitive env exists only in Bahia secrets
  - deploy merges secrets correctly
- Evidence paths: `<fill>`
- Notes: `<fill>`

### SF-06 — Transaction rollback on controlled failure

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - failed import reports failure through signer-first result flow
  - no partial service/environment/build/artifact/state/observation rows remain
- Evidence paths: `<fill>`
- Notes: `<fill>`

### SF-07 — Concurrent duplicate import

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill two concurrent invocations>
  ```
- Checks:
  - concurrent signer-first imports converge to one canonical identity set
  - no duplicate service/build/artifact identities remain
- Evidence paths: `<fill>`
- Notes: `<fill>`

### SF-08 — Direct-runtime guardrails

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  bahia services actions restart --service <service-id> --environment <env-id>
  bahia services actions stop --service <service-id> --environment <env-id>
  bahia services actions deploy --service <service-id> --environment <env-id> [--artifact <artifact-id>]
  ```
- Checks:
  - adopted workload action succeeds
  - non-adopted or mismatched-host actions fail closed
  - failed runtime actions do not mutate desired state first
- Evidence paths: `<fill>`
- Notes: `<fill>`

### SF-09 — Compose takeover decision

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Decision: `disabled` / `approved for named services only` / `not applicable`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - takeover-disabled scan marks compose candidates non-adoptable
  - if enabled, operator explicitly signs off and restart/deploy semantics are accepted
- Evidence paths: `<fill>`
- Approver: `<fill>`
- Notes: `<fill>`

### SF-10 — Observability / audit

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands / dashboards inspected:
  ```text
  <fill>
  ```
- Checks:
  - typed adoption/runtime events publish after persistence
  - logs include request/event IDs, actor pubkey, endpoint ref, result
  - no secret leakage in logs/metrics/events
  - operators can follow status/result progress without polling
- Evidence paths: `<fill>`
- Notes: `<fill>`

### SF-11 — Rollback / disable

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - disabling signer-first adoption/direct-runtime paths closes the execution surface
  - existing imported state remains inspectable
  - original workload owner can resume control
- Evidence paths: `<fill>`
- Notes: `<fill>`

## Secondary compatibility-only checks

Run only if the release owner explicitly requires legacy HTTP compatibility evidence.
These checks are not the primary production gate.

- HTTP privileged endpoints still reject `Authorization: Bearer ...` with `401` when auth is enabled.
- Any legacy NIP-98 operator flow under test is documented as compatibility-only.
- Compatibility failures do not override signer-first production signoff unless a release requirement explicitly depends on them.

## Production readiness statement

Production readiness for adoption/import/direct-runtime now depends on signer-first operator verification.
Release approval requires a stored local rehearsal artifact plus passing automated gates.
Production enablement additionally requires passing staged/live SF-01 through SF-11.
Do not use the legacy HTTP/NIP-98 checklist as the primary signoff surface.
