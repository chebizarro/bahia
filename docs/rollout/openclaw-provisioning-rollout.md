# OpenClaw provisioning rollout plan

Task: **bahia-openclaw-rollout-conformance-20260819**

## Source and release pins

> **NOTE (2026-09-11):** Historical implementation commits are not promotion
> pins. Record the exact reviewed release commit and immutable deployed image
> digests in the release record before starting this procedure.

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

Enable one disposable production canary while incumbents remain unchanged.

> **NOTE (2026-09-11, updated):** The `bahia_openclaw_provisioning_*` metrics
> are appended to Bahia's `/metrics` when
> `soul_factory.openclaw_saga_store_dir` is configured. However, no binary
> drives the saga engine yet (bahia-lf0s4), so the series stays empty until a
> provisioning run is recorded. "Zero critical alerts" is only meaningful after
> those series are confirmed present in Prometheus; see
> `docs/runbooks/openclaw-provisioning-operations.md`.

Gate:

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
