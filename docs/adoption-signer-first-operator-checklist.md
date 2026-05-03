# Adoption / Import Signer-First Operator Checklist (Draft)

Status: **Draft replacement checklist**  
Epic: `bahia-sqfx`  
Drafting task: `bahia-sqfx.1`  
Execution successor to legacy signoff: `bahia-sqfx.5`

This document is the draft replacement for the deprecated HTTP/NIP-98 adoption/import operator checklist.
It defines the **target-state signer-first operator verification flow** for adoption/import and direct-runtime actions.

## Important current-state note

**Do not execute this checklist as a production gate yet.**

As of this draft:
- the legacy HTTP/NIP-98 operator checklist has been deprecated;
- the signer-first operator rollout gate is **not yet fully implemented**;
- signer-first adoption/import/direct-runtime execution is being tracked by:
  - `bahia-sqfx.2` — signer-first operator transport
  - `bahia-sqfx.3` — signer-first CLI/operator workflow
  - `bahia-sqfx.4` — final signer-first verification matrix/runbook
  - `bahia-sqfx.5` — staged/live signer-first signoff execution

Use this document to shape implementation and future staged/live evidence capture.

## Scope

This checklist assumes operator workflows no longer treat privileged HTTP routes as the primary control surface.
Instead, operator actions are expected to run through signer-first control-plane requests over Nostr event flows, with operator authorization based on pubkey policy and event verification.

In scope:
- adoption scan/import operator requests
- direct-runtime lifecycle operator requests
- signer-first request publication and result/status correlation
- operator pubkey authorization
- managed runtime endpoint governance
- redaction, rollback, concurrency, observability, and rollback/disable checks

Out of scope for this draft:
- legacy HTTP/NIP-98 privileged route validation as the primary gate
- treating `Authorization: Nostr ...` rollout checks as the canonical operator signoff surface

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
| Control-plane relay URL(s) under test | `<fill>` |
| Signer mode under test | `NIP-07` / `NIP-46` / `local keyer` / `<fill>` |
| Operator pubkey(s) | `<fill redacted if needed>` |
| Execution start (UTC) | `<fill>` |
| Execution end (UTC) | `<fill>` |
| Primary operator | `<fill>` |
| Additional approvers | `<fill>` |
| Evidence bundle location | `<fill>` |
| Managed endpoint refs under test | `<fill>` |
| Compose takeover policy for this environment | `disabled` / `enabled for named services only` / `not applicable` |
| `/api/v1/system/info` capability evidence | `<fill path + timestamp>` |
| Relay `/relay` reachability evidence | `<fill path + timestamp>` |
| Signer capability evidence (`signEvent`, correlation-capable request flow, any required crypto capability) | `<fill>` |

## Target-state prerequisites

Check each box before signer-first LN rows begin.

- [ ] Release commit is deployed to staging/live-like environment.
- [ ] Signer-first operator transport for adoption/import/direct-runtime is enabled.
- [ ] Operator signer is available and can publish the required request events.
- [ ] Operator pubkeys are allowlisted for signer-first adoption/import/direct-runtime actions.
- [ ] At least two `runtime.endpoints.<ref>` aliases are configured.
- [ ] At least one endpoint uses remote Docker TLS/mTLS.
- [ ] `adoption.allow_raw_docker_hosts=false` unless explicit break-glass testing is approved.
- [ ] `/api/v1/system/info` and relay/topology evidence are captured for the release candidate.
- [ ] A non-critical candidate workload is available for import.
- [ ] Rollback owner/procedure for the candidate workload is confirmed.
- [ ] Event/status/result subscriptions and observability capture are available for operators.

## Evidence bundle requirements

- [ ] Release manifest / deployment record
- [ ] Redacted staging config excerpt
- [ ] Signer/operator setup notes (no secrets)
- [ ] Event publication transcript or CLI transcript
- [ ] Request event IDs and correlated status/result event IDs
- [ ] `/api/v1/system/info` capture
- [ ] Relay `/relay` reachability capture when sidecar validation is in scope
- [ ] Relevant logs with request IDs / event IDs
- [ ] Metrics snapshots
- [ ] Database row IDs / imported entity references
- [ ] Follow-up issue IDs for failures/blockers
- [ ] Final approver signoff note

## Draft execution template

### SF-01 — Signer/operator authorization

**Goal**: prove signer-first operator requests enforce operator pubkey authorization and reject invalid or unauthorized requests.

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Commands / actions executed:
  ```text
  <fill>
  ```
- Expected result:
  - unsigned / malformed request is rejected
  - valid signed non-operator request is rejected
  - valid signed operator request is accepted into the configured flow
  - result/status correlation is tied to the originating request event
- Actual result: `<fill>`
- Evidence paths: `<fill>`
- Follow-up issue(s): `<fill or none>`
- Notes: `<fill>`

### SF-02 — Relay routing and result/status correlation

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - operator request reaches the intended relay path/topology
  - status/result events correlate to the request event id
  - duplicate or unrelated events are ignored safely
  - operator sees terminal success/failure without HTTP polling assumptions
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-03 — Multi-host managed endpoint scan

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - signer-first operator scan can target at least two managed endpoint refs
  - responses and correlated results use endpoint refs only
  - no raw Docker host or cert material leaks into operator-visible surfaces
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-04 — Raw-host rejection

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - raw-host scan/import requests are rejected when disabled
  - no runtime call is made
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-05 — Redaction / secret import

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - scan/import responses expose only safe values and redacted key names
  - imported sensitive env exists only in Bahia secrets
  - deploy merges secrets correctly
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-06 — Transaction rollback on controlled failure

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - failed import reports failure through signer-first result flow
  - no partial service/environment/build/artifact/state/observation rows remain
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-07 — Concurrent duplicate import

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - concurrent signer-first imports converge to one canonical identity set
  - no duplicate service/build/artifact identities remain
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-08 — Direct-runtime guardrails

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - adopted workload action succeeds
  - non-adopted or mismatched-host actions fail closed
  - failed runtime actions do not mutate desired state first
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-09 — Compose takeover decision

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - takeover-disabled scan marks compose candidates non-adoptable
  - if enabled, operator explicitly signs off and restart/deploy semantics are accepted
- Approver: `<fill>`
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-10 — Observability / audit

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - typed adoption/runtime events publish after persistence
  - logs include request/event IDs, actor pubkey (or mapped operator identity), endpoint ref, result
  - no secret leakage in logs/metrics/events
  - operators can follow status/result progress without polling
- Evidence: `<fill>`
- Notes: `<fill>`

### SF-11 — Rollback / disable

- Status: `PASS` / `FAIL` / `BLOCKED` / `N/A`
- Checks:
  - disabling signer-first adoption/direct-runtime paths closes the execution surface
  - existing imported state remains inspectable
  - original workload owner can resume control
- Evidence: `<fill>`
- Notes: `<fill>`

## Production readiness statement

This draft becomes the primary production signoff checklist only after the signer-first operator path exists and `bahia-sqfx.2`, `bahia-sqfx.3`, and `bahia-sqfx.4` are complete.
Until then, this document is a planning and implementation target, not an executable signoff artifact.
