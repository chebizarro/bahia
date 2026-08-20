# OpenClaw provisioning rollout plan

Task: **bahia-openclaw-rollout-conformance-20260819**

## Source pins

| Component | Commit |
| --- | --- |
| Integrated Bahia baseline | f88ec8fa418485670803c3ee72e2dd10d7de601e |
| Dedicated runtime orchestration | 490835adaecca1f1871d30f34082d7ff85566f21 |
| Signet/NIP-46 enrollment | a2058571bdbf2aca61f28a64078209e1f58651a1 |
| Durable saga hardening | f8c6b36b42db68bc4213850543088fa464edd99f |
| Runtime integration merge | 05f1c085ca67dcde938ae26488fab59e2da8da22 |
| Signet integration merge | f7638149fc0ef226262da9ac29333dad9670d2a9 |
| Saga integration merge | c781b0883f38faa981a111f4abdbb4cc18a99d70 |

The final task commit supersedes the baseline pin after merge.

## Image and configuration pin gate

Repository sources do not contain the immutable OCI digests currently deployed for Bahia, OpenClaw, Signet, or relay. No canary may start until Track B records all four as repository@sha256 with 64 lowercase hexadecimal characters, together with OpenClaw source commit, config revision, plugin integrity, and prior-version digests. Historical mutable recovery tags are forbidden promotion inputs.

The identical digest set moves from disposable to second canary to production. Rebuild, retag, or digest substitution restarts acceptance.

## Phase 0: offline and disposable

1. Run: go test ./internal/soulfactory/saga/... -run TestOpenClawProvisioningConformanceDisposableEnvironment
2. Run build, full tests, touched-package vet, alert rule tests, and secret scan.
3. Restore sanitized backup shapes into an isolated disposable environment.
4. Provision two disposable identities sequentially and concurrently.
5. Exercise replay/conflict, Bahia/Signet/runtime restart, relay backfill, and every-stage compensation.
6. Require unique identities, runtimes, bindings, models, routes, volumes, and DM replies without cross-routing.
7. Rehearse prior-digest rollback without removing valid identities.

Exit only with sanitized evidence and zero critical alerts.

## Phase 1: first canary

Enable one disposable production canary while incumbents remain unchanged. Gate:

- pinned build/instance visible in metrics/logs
- exact-client Signet policy and durable reconnect
- relay NIP-11, NIP-42 when required, OK, EOSE/backfill, CLOSED
- dedicated runtime labels/limits
- real selected-model inference
- independent encrypted DM round-trip
- terminal 7950 and 31951
- unchanged Marjam and SNR reachability

Any critical alert or incumbent regression triggers rollback.

## Phase 2: second canary

Provision a second disposable soul concurrently with reconciliation of the first. Verify no shared identity, account, runtime, route, volume, model, DM recipient, or terminal correlation. Restart runtimes independently, then Bahia and Signet, and repeat DM gates.

## Phase 3: controlled production

Enable admission for a bounded reviewed cohort. Keep previous Bahia, sidecar, OpenClaw, Signet policy, and relay digests available. Monitor stage age, retry/reconcile/rollback, readiness, DM gate, terminal projection, denial, orphan, and mismatch metrics.

SNR is adopted without key recreation. Marjam remains unchanged absent separate approval.

## Rollback invariant

Rollback restores executables/configuration, not identity history. Do not revert or delete valid Signet identities, durable NIP-46 client material, accepted public events, completed workspaces, or audit lineage.

Disable admission, restore prior pinned digests/policy, backfill through EOSE, and reconcile durable runs from inspected reality.
