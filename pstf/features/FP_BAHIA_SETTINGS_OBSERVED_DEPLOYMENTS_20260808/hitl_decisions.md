# HITL decisions — FP_BAHIA_SETTINGS_OBSERVED_DEPLOYMENTS_20260808

## D1 — Unmanaged-instance exposure: option B (owner decision)

Public signed inventory events carry only a redacted aggregate for unmanaged / observed-only runtime workloads: counts per target and environment (`total`, `managed`, `unmanaged`) with `scan_state` (`complete` or `unavailable`) and `scanned_at`, from which consumers derive staleness. No per-instance host or target alias, image, digest, version, or container identity is published. Per-instance detail stays behind the authenticated operator path (`adoption/scan`, `adoption.allowed_pubkeys`). Managed deployments are published per row.

## D2 — Adoption requests must be encrypted (derived from D1)

Before this change, a plaintext ContextVM `adoption/scan` request got a plaintext `25910` response on the relay. Anyone subscribed could read the per-instance detail, which contradicts D1's requirement that detail reach only authorized requesters. `adoption/scan` and `adoption/import` now refuse requests that are not NIP-59 wrapped. The CLI must pass `--encrypted --service-pubkey <hex>`. Authorization is checked first, so unlisted requesters still get the authorization error.

Operator impact: `bahia adopt scan|import` without `--encrypted` now fails with an explicit JSON-RPC error. `--http-fallback` does not apply, because it only covers requests that no relay accepted. Follow-up: make the CLI default to encryption for adoption commands, or pre-validate before publishing.

## D3 — Discovery is not inventory

The `observed_deployments` array that 18a5079a added to `bahia.system-discovery.v1` is removed rather than kept as a rollout fallback. It published `observed_host`, which falls back to the raw Docker host, and container IDs publicly, and it was not authoritative. Discovery-only clients keep working; they simply receive no deployment rows. No legacy discovery rendering path remains in the web app.

## D4 — Freshness budget

`stale_after_seconds` = projector snapshot repair interval + 2 × reconcile interval (default 10 m + 2 m = 720 s). Observations refresh every reconcile pass, but the inventory is republished on material events and at the repair interval, so the published `observed_at` may lag by up to the repair interval. Unmanaged scan aggregates use the same budget. Scans are operator-initiated, so they normally display as stale with their age.

## Open question for the owner

- Should Bahia run periodic background adoption scans of configured runtime endpoints so that unmanaged counts stay fresh? This changes Docker-socket access patterns and is out of scope without an explicit decision.
