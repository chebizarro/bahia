# Adoption / Import Live-Network Operator Checklist and Evidence Template

Issue: `bahia-y294`  
Purpose: agent-usable execution checklist and evidence template for the manual staging/live-network signoff required before production enablement.

Use this document **while executing** the rollout signoff. It is intentionally procedural. The verification matrix in [`adoption-live-network-verification.md`](adoption-live-network-verification.md) remains the normative gate definition; this checklist turns that matrix into an operator run sheet.

## Operator instructions

1. Work from the release commit that is being proposed for rollout.
2. Fill every placeholder in this document as you execute the run.
3. For each failed row:
   - stop at the first unsafe point;
   - capture evidence;
   - create a follow-up issue;
   - mark the row `FAIL` or `BLOCKED`;
   - do **not** mark production ready.
4. If a row is genuinely not applicable, record `N/A`, explain why, and capture approver signoff for the exception.
5. Do not paste raw secrets, client keys, JWT secrets, or unredacted sensitive env values into this document.

## Run metadata

| Field | Value |
| --- | --- |
| Release commit SHA | `<fill>` |
| Target environment | `<fill>` |
| Bahia API base URL | `<fill>` |
| Auth mode under test | `JWT` / `NIP-98` / `both` |
| Execution start (UTC) | `<fill>` |
| Execution end (UTC) | `<fill>` |
| Primary operator | `<fill>` |
| Additional approvers | `<fill>` |
| Evidence bundle location | `<fill path / ticket / object store URL>` |
| Compose takeover policy for this environment | `disabled` / `enabled for named services only` / `not applicable` |

## Environment prerequisites

Check each box before LN-01 starts.

- [ ] Release commit is deployed to staging/live-like environment.
- [ ] `auth.enabled=true`.
- [ ] `adoption.enabled=true`.
- [ ] `direct_runtime_actions.enabled=true`.
- [ ] Operator allowlists are configured.
- [ ] `adoption.allow_raw_docker_hosts=false` unless an explicit break-glass scenario is being tested.
- [ ] At least two `runtime.endpoints.<ref>` aliases are configured.
- [ ] At least one endpoint uses remote Docker TLS/mTLS.
- [ ] Monitoring can reach `/metrics` with the required auth mode.
- [ ] A non-critical candidate workload is available for import.
- [ ] Rollback owner/procedure for the candidate workload is confirmed.

## Evidence bundle requirements

Collect these artifacts as files or links under the evidence bundle location.

- [ ] Release manifest / deployment record
- [ ] Redacted staging config excerpt
- [ ] Command transcript or shell log
- [ ] API request/response captures with secrets redacted
- [ ] Relevant log excerpts with request IDs
- [ ] Metrics snapshots
- [ ] Database row IDs / record references for imported entities
- [ ] Follow-up issue IDs for any failures
- [ ] Final approver signoff note

## Automated gate confirmation

Record the release-commit results before manual LN rows begin.

| Check | Command | Result (`PASS`/`FAIL`) | Evidence |
| --- | --- | --- | --- |
| Config gate safety | `go test ./internal/config` | `<fill>` | `<fill>` |
| Auth-enabled privileged routing | `go test ./internal/api/router -run 'TestPrivilegedRoutes|TestAdoptionRoutes'` | `<fill>` | `<fill>` |
| Managed endpoint governance | `go test ./internal/service -run 'TestAdoptionService.*Endpoint|TestAdoptionServiceRejectsRaw' && go test ./internal/adapters/runtime -run 'Endpoint|TLS|Resolver'` | `<fill>` | `<fill>` |
| Redaction and secret handling | `go test ./internal/service -run 'TestAdoptionService.*Sensitive|Redacts' && go test ./internal/api/handlers -run Adoption` | `<fill>` | `<fill>` |
| Transactional import and idempotency | `go test ./internal/service -run 'TestAdoptionServiceImportTransactional|TestAdoptionServiceImportRetries|TestAdoptionServiceImportSeeds'` | `<fill>` | `<fill>` |
| Direct runtime guardrails | `go test ./internal/service -run RuntimeLifecycle && go test ./internal/api/handlers -run RuntimeLifecycle` | `<fill>` | `<fill>` |
| Client contract | `go test ./pkg/client -run 'Adoption|RuntimeAction|Privileged'` | `<fill>` | `<fill>` |
| CLI parsing helpers | `go test ./cmd/cli -run Adoption` | `<fill>` | `<fill>` |
| Full Go regression | `go test ./...` | `<fill>` | `<fill>` |

## Manual execution template

Use one section per LN row. Fill every field.

### LN-01 — Auth-enabled operator access

**Goal**: prove privileged adoption/direct-runtime routes enforce production auth and operator authorization.

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Start / end time (UTC): `<fill>`
- Operator: `<fill>`
- Preconditions checked: `<fill>`
- Commands executed:
  ```text
  <fill>
  ```
- Expected result:
  - no credential => `401`
  - non-operator => `403`
  - operator => reaches configured flow
  - NIP-98 deployments validate signed requests end-to-end
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes / exceptions: `<fill>`

### LN-02 — Multi-host managed endpoint scan

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  bahia adopt scan --target <host-a> --target <host-b>
  ```
- Checks:
  - both scans succeed
  - response uses endpoint refs only
  - no raw docker host or cert material appears in responses/logs
  - host attribution is correct
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-03 — Remote Docker TLS/mTLS

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Endpoint ref under test: `<fill>`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - scan/import succeeds through server-managed TLS
  - cert/key paths or contents do not leak into client-visible surfaces
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-04 — Raw-host rejection

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Command executed:
  ```text
  bahia adopt scan --raw-target breakglass=tcp://127.0.0.1:2375
  ```
- Checks:
  - request fails with policy error
  - no Docker call is made
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-05 — Redaction / secret import

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Workload under test: `<fill>`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - scan/import responses show only safe values and redacted key names
  - imported sensitive env exists only in Bahia secrets
  - deploy merges secrets correctly
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-06 — Transaction rollback on controlled failure

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Failure method used: `<fill>`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - import reports failure
  - no partial service/environment/build/artifact/state/observation rows remain
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-07 — Concurrent duplicate import

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill two concurrent invocations>
  ```
- Checks:
  - one canonical service/build/artifact identity remains
  - no duplicate identities or inconsistent state
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-08 — Direct-runtime guardrails

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - imported direct-runtime workload action succeeds
  - non-adopted and mismatched-host actions fail closed
  - no state mutation occurs before failed runtime action
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-09 — Compose takeover decision

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Decision: `disabled` / `approved for named services only` / `not applicable`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - takeover-disabled scan marks compose candidates non-adoptable
  - if enabled, operator explicitly signs off and restart/deploy semantics are accepted
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Approver: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-10 — Observability / audit

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands / dashboards inspected:
  ```text
  <fill>
  ```
- Checks:
  - typed events publish after persistence
  - metrics counters/durations move as expected
  - logs include request ID, actor, endpoint ref, result
  - no secret leakage in logs/metrics/events
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### LN-11 — Rollback / disable

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands executed:
  ```text
  <fill>
  ```
- Checks:
  - privileged routes return `404` after disable + restart
  - imported records remain readable
  - original workload owner can resume control
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

## Follow-up issue log

| Issue | Triggered by | Severity | Blocking production? | Notes |
| --- | --- | --- | --- | --- |
| `<fill>` | `<LN row>` | `<fill>` | `yes/no` | `<fill>` |

## Final signoff summary

| Item | Value |
| --- | --- |
| All automated checks pass | `yes/no` |
| LN-01..LN-11 pass or are explicitly approved as N/A | `yes/no` |
| Any blocking follow-up issues remain open | `yes/no` |
| Production enablement recommendation | `GO` / `NO-GO` |
| Final approver(s) | `<fill>` |
| Final signoff timestamp (UTC) | `<fill>` |

## Final statement

- Production enablement decision: `<GO / NO-GO>`
- Reason: `<fill>`
- If `NO-GO`, required next actions: `<fill>`
