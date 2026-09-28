# HITL decisions — FP_BAHIA_SETTINGS_OBSERVED_DEPLOYMENTS_20260808

## D1 — Unmanaged-instance exposure: option B (owner decision)

Public signed inventory events carry only a redacted aggregate for unmanaged / observed-only runtime workloads: counts per target and environment (`total`, `managed`, `unmanaged`) with `scan_state` (`complete` or `unavailable`) and `scanned_at`, from which consumers derive staleness. No per-instance host or target alias, image, digest, version, or container identity is published. Per-instance detail stays behind the authenticated operator path (`adoption/scan`, `adoption.allowed_pubkeys`). Managed deployments are published per row.

## D2 — Adoption requests must be encrypted (derived from D1)

Before this change, a plaintext ContextVM `adoption/scan` request got a plaintext `25910` response on the relay. Anyone subscribed could read the per-instance detail, which contradicts D1's requirement that detail reach only authorized requesters. `adoption/scan` and `adoption/import` now refuse requests that are not NIP-59 wrapped. The CLI must pass `--encrypted --service-pubkey <hex>`. Authorization is checked first, so unlisted requesters still get the authorization error.

Operator impact: `bahia adopt scan|import` without `--encrypted` now fails with an explicit JSON-RPC error. `--http-fallback` does not apply, because it only covers requests that no relay accepted. Follow-up: make the CLI default to encryption for adoption commands, or pre-validate before publishing.

## D3 — Discovery is not inventory

The `observed_deployments` array that 18a5079a added to `bahia.system-discovery.v1` is removed rather than kept as a rollout fallback. It published `observed_host`, which falls back to the raw Docker host, and container IDs publicly, and it was not authoritative. Discovery-only clients keep working; they simply receive no deployment rows. No legacy discovery rendering path remains in the web app.

## D4 — Freshness budget

`stale_after_seconds` = projector snapshot repair interval + 2 × reconcile interval (default 10 m + 2 m = 720 s). Observations refresh every reconcile pass, but the inventory is republished on material events and at the repair interval, so the published `observed_at` may lag by up to the repair interval. Unmanaged scan aggregates use this budget when background scanning is off: operator-initiated scans then display as stale with their age. With background scanning on (D5), target scans carry their own budget, repair interval + 2 × (scan interval + jitter) + scan timeout (default 1320 s).

## D5 — Background adoption scans (owner decision, 2026-09-28)

Owner decision: Bahia runs background adoption scans of its configured runtime targets and endpoints, so the unmanaged aggregate counts stay fresh. This resolves the open question the original branch left.

Implementation choices (branch `task/bahia-background-adoption-scans-20260928`):

- **Default on only where adoption is enabled.** `adoption.background_scan.enabled` unset follows `adoption.enabled`, and `false` opts out. `true` without adoption is a config error. This is safe because:
  - it scans only `runtime.endpoints` aliases already configured for adoption, reusing the transport and credentials Bahia already uses there;
  - it never scans raw `docker_host` targets;
  - its Docker calls are GET list/inspect only;
  - it publishes only D1 aggregates.

  Deployments that never enabled adoption gain no new Docker access.
- **Same scan logic** as `adoption/scan` (`AdoptionService.Scan`, origin `background`), one target per call.
- **Bounded:**
  - interval 5 m (1 m–24 h);
  - jitter 30 s (≤ interval/2);
  - per-target timeout 1 m;
  - concurrency 2 (≤ 8);
  - exponential backoff up to 1 h;
  - one cycle at a time, overlap skipped rather than queued.
- **Traffic.** Target-scan aggregates republish only on a material change, or as a heartbeat once per repair interval. This goes through the existing dedupe, coalescing, and relay-backoff path.
- **Retirement.** Target-scan coordinates outside the configured scope are tombstoned: background-origin ones immediately, operator ad-hoc ones once stale. This closes the "scan coordinates never retired" residual, but only while background scanning is enabled.

Access-scope note for the owner: the Docker Engine API has no read-only credential. The certificate or socket Bahia already holds for an endpoint is full-control, so background scanning adds calls, not privilege. Operators who need stricter isolation can narrow `adoption.background_scan.targets`, or front the endpoint with an authorization proxy that allows only the three GET routes. See `docs/adoption-production-rollout.md#background-adoption-scans`.
