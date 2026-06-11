# EDGE_DEPLOY_WORKFLOW Verification Report

## Scope

Implemented the Immediate Relief self-hosted edge deploy vertical slice tracked by `bahia-irii`.

Included:

- `.github/workflows/deploy-edge.yml`
- `scripts/deploy_edge_compose_update.py`
- `test/scripts/test_deploy_edge_compose_update.py`
- `docs/push-to-deploy-and-hiveci-runbook.md`
- `pstf/features/EDGE_DEPLOY_WORKFLOW/*`

Excluded:

- durable Hive-CI artifact publishing, which remains tracked by `bahia-fj0z`
- Harbor image publishing
- Bahia artifact registration from Hive-CI `5402` results
- auto-deploy policy enablement

## Acceptance Mapping

- AC1: Covered by static review of `.github/workflows/deploy-edge.yml` for `master` trigger, `[self-hosted, edge-01, docker]`, `permissions: contents: read`, and non-canceling concurrency.
- AC2: Covered by static review of backend and web `docker build` steps and image tags in `.github/workflows/deploy-edge.yml`.
- AC3: Covered by static review of release staging to `/srv/data/bahia-controlplane/releases/github-<shortsha>` using `rsync -a --delete --exclude .git`.
- AC4: Covered by `python3 -m unittest discover -s test/scripts -p 'test_*.py'` and `python3 -m py_compile scripts/deploy_edge_compose_update.py test/scripts/test_deploy_edge_compose_update.py`.
- AC5: Covered by static review of `docker compose -f "$COMPOSE_FILE" up -d bahia relay web` and curl checks for `8080/health`, `3334/relay` with `Accept: application/nostr+json`, and `8081/`.
- AC6: Covered by this PSTF artifact set and runbook text explicitly assigning the durable Hive-CI path to `bahia-fj0z`.

## Verification Evidence

- `python3 -m unittest discover -s test/scripts -p 'test_*.py'` passed on 2026-06-11: 5 tests ran in 0.004s and returned OK. The failure-path tests intentionally printed helper error messages for duplicate image lines, missing release docs mount, missing relay service, and unsafe tag rejection.
- `python3 -m py_compile scripts/deploy_edge_compose_update.py test/scripts/test_deploy_edge_compose_update.py` passed on 2026-06-11.
- Static review confirmed `.github/workflows/deploy-edge.yml` implements the `master` push trigger, `[self-hosted, edge-01, docker]` labels, `contents: read`, serialized non-canceling concurrency, safe preflight, local backend/web builds, release rsync, live compose backup, helper invocation, `docker compose up -d bahia relay web`, and the three required curl health checks.
- Static review confirmed `docs/push-to-deploy-and-hiveci-runbook.md` references the checked-in workflow/helper instead of the removed inline mutation prototype and keeps durable Hive-CI artifact publishing assigned to `bahia-fj0z`.
