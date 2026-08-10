# FP_BAHIA_SETTINGS_OBSERVED_DEPLOYMENTS_20260808 Verification Report

## Implemented behavior

- Signed `bahia.system-discovery.v1` announcements include a deterministic `observed_deployments` projection derived from service, environment, environment-service-state, and matching current runtime-observation read models.
- Desired-only states and stale observation coordinates are excluded; observed rows include runtime identity, version/image, infrastructure host/container, health, drift, and observation time.
- Discovery publishes with configured browser relay policy when `nostr.sidecar.enabled=false`, supporting the backend plus external `bahia-relay` container topology.
- Relevant domain events refresh only the replaceable discovery announcement; relay-set and NIP-65 policy events remain part of snapshot publication.
- Settings > Versions renders observed deployments as runtime truth and labels compile-time artifact metadata separately as **Build information**.

## Acceptance mapping

| Criterion | Evidence | Result |
| --- | --- | --- |
| AC1 | `TestProjectorPublishesObservedDeploymentsWithoutEmbeddedSidecar` validates current-observation matching, desired-only exclusion, fields, and deterministic order. | Pass |
| AC2 | The same projector test uses `Sidecar.Enabled=false` and verifies discovery plus browser/context/service relay sets. | Pass |
| AC3 | `version.test.js` validates observed payload normalization/order and proves static `versions.components` cannot create observed rows. | Pass |
| AC4 | Settings markup contains distinct Observed deployments and Build information groups; Svelte diagnostics and production build pass. | Pass |

## Quality gates

- `go test ./internal/adapters/nostr` — pass.
- `go build ./...` — pass.
- `npm run test:unit -- --run tests/unit/version.test.js` from `web/` — pass, 5 tests.
- `npm run lint` from `web/` — pass, 0 errors and 0 warnings.
- `npm run build` from `web/` — pass.

## Review

RepoPrompt Oracle review found no P0 blocking findings. The event-driven refresh path was narrowed after review so observation changes republish only the signed discovery announcement rather than redundantly republishing unchanged relay policy events.

## Verification boundary

A manual browser smoke test against a deployed backend/external-relay pair was not run in this repository session. Protocol projection, frontend mapping, Svelte compilation, and production asset generation are covered by the gates above.
