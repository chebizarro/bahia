# Edge deployment and Hive-CI artifact publishing

Bahia has two distinct operational paths:

- `.github/workflows/deploy-edge.yml` builds and installs an exact Bahia
  revision on the edge host;
- `.github/workflows/hive-ci-build.yml` builds application images under the
  Hive-CI runner contract and publishes signed artifact evidence for Bahia.

Do not treat an edge installation as artifact registration, or a successful CI
build as a deployment approval.

## Edge deployment

### Runner contract

The edge workflow runs only on a runner labelled `self-hosted`, `edge-01`,
`docker`. The runner needs Docker Engine/Compose, git, Python 3, Node/pnpm for
the gallery gate, write access to `/srv/data/bahia-controlplane`, and read
access to the repository through `GITHUB_TOKEN`.

Dispatch with a full commit:

```bash
gh workflow run deploy-edge.yml \
  -f release_revision=<40-hex-commit> \
  -f release_retention_count=5 \
  -f image_retention_days=30 \
  -f wheelhouse_allowed_pubkeys='<comma-separated-64-hex-pubkeys>'
```

The widget allowlist may be empty; empty denies every widget publisher. The
workflow has one concurrency group and does not cancel a deployment already in
progress.

### Preflight

Before dispatch, confirm:

```bash
ssh edge-01 'docker compose -f /srv/data/bahia-controlplane/docker-compose.yml config >/dev/null'
ssh edge-01 'test -w /srv/data/bahia-controlplane'
ssh edge-01 'curl -fsS http://172.17.0.1:8080/health'
```

The rendered web service must carry non-empty
`PUBLIC_BAHIA_BOOTSTRAP_RELAYS` and `PUBLIC_BAHIA_SERVICE_PUBKEYS`.
`scripts/check_web_bootstrap_compose.py` enforces that invariant. The workflow
also runs the relay-policy gate and image-admission policy before changing the
stack.

### Apply and verify

The workflow builds local SHA-tagged daemon and web images, stages the exact
source/docs tree, saves the host Compose file, updates it with
`scripts/deploy_edge_compose_update.py`, and applies the stack. It then waits
for readiness:

```bash
curl -fsS http://172.17.0.1:8080/ready
```

After a successful run, verify:

```bash
ssh edge-01 'docker compose -f /srv/data/bahia-controlplane/docker-compose.yml ps'
ssh edge-01 'docker compose -f /srv/data/bahia-controlplane/docker-compose.yml images'
curl -fsS https://bahia.sharegap.net/health
curl -fsS https://bahia.sharegap.net/ready
curl -fsS -H 'Accept: application/nostr+json' https://bahia.sharegap.net/relay
```

Confirm that the web bootstrap trusts the deployed service pubkey and that a
REQ against the advertised relay reaches `EOSE`.

### Failure behavior and retention

The gated apply owns rollback: on failure it restores the saved Compose file
and its safe image references, reapplies the stack and reports the original
error. Do not perform an unrelated manual Compose mutation while the workflow
is running.

The final cleanup keeps the configured number of release trees and Compose
backups and removes only Bahia-tagged images older than the configured age.
It never runs `docker image prune -a`.

## Hive-CI build publication

`.github/workflows/hive-ci-build.yml` is a `workflow_dispatch` workflow. The
Hive-CI runner executes it with `act` after a trusted kind-`5401` workflow-run
event selects the repository workflow. The execution environment supplies the
Harbor credentials and the branch/commit metadata; GitHub-hosted runners are
not the credential boundary for this workflow.

The workflow requires a full `GITHUB_SHA` on `refs/heads/master`, then:

1. logs in to Harbor;
2. builds and pushes `harbor.sharegap.net/cascadia/bahia:<tag>`;
3. resolves its immutable manifest digest;
4. builds and pushes `harbor.sharegap.net/cascadia/bahia-web:<tag>` with the
   runtime bootstrap build arguments;
5. writes `.hiveci-result.json`.

Result schema:

```json
{
  "imageRepo": "harbor.sharegap.net/cascadia/bahia",
  "imageTag": "<tag>",
  "imageDigest": "sha256:<64-hex>",
  "logURL": "<actions-or-local-run-url>"
}
```

All four fields must be non-empty. The runner places `image_repo`,
`image_tag` and `image_digest` in the signed kind-`5402` result. Bahia leaves a
result in `artifact_pending` when any field is absent and registers only a
digest-pinned artifact after the result, repository/workflow identity and
release policy pass validation.

The web image is pushed for deployment use but is not a second artifact in the
single-artifact `5402` result.

## Bahia pipeline checks

The daemon consumes `5402` through `internal/pipeline/bridge.go`. For a result
to become deployable, verify:

- the result signer and workflow match the configured Hive-CI policy;
- `image_digest` is an immutable `sha256` manifest digest;
- the release identity has no conflicting accepted attestation;
- the `hiveci-result` and `hiveci-release` canonical records are present;
- the artifact record contains the signed repository, tag, digest and
  provenance metadata.

Operational state is relay-backed. Do not seed or repair artifact acceptance
by editing PostgreSQL rows. Correct the signed result or policy and let the
pipeline replay it idempotently.

## OpenClaw sidecar release

`.github/workflows/deploy-openclaw-soulfactory-sidecar.yml` is independent of
the edge workflow. It runs on `self-hosted`, `max`, `docker`, accepts an exact
`release_sha` (or a published release tag), builds a SHA-tagged sidecar image,
recreates only `openclaw-soulfactory-sidecar`, verifies `/ready`, and uploads a
sanitized image-digest record. See the
[Soul Factory sidecar runbook](soul-factory-sidecar-runbook.md).
