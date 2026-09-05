# Push-to-Deploy and Hive CI Artifact Publishing Runbook

This runbook captures two related tracks:

- immediate relief: rebuild and redeploy the live Bahia control plane on push;
- durable path: finish Hive CI artifact publishing so Bahia can own deployment from build artifacts.

The immediate track is intentionally operational glue. The durable track is the desired Bahia-native control-plane path.

## Current Live Shape

The live `edge-01` control plane currently runs from local Docker images referenced by `/srv/data/bahia-controlplane/docker-compose.yml`:

```text
bahia      local/bahia-controlplane-bahia:github-<sha>
bahia-relay local/bahia-controlplane-bahia:github-<sha>
bahia-web  local/bahia-controlplane-web:github-<sha>
```

The live compose file also mounts release docs from:

```text
/srv/data/bahia-controlplane/releases/github-<sha>/docs:/docs:ro
```

Any automated deploy must update both image tags and the mounted release docs path.

## Immediate Relief: Self-Hosted Runner Deploy

Use a containerized self-hosted runner on `edge-01` or `max`. The runner must have:

- access to the Bahia repo;
- Docker socket access for builds and Compose updates;
- write access to `/srv/data/bahia-controlplane`;
- labels such as `self-hosted`, `edge-01`, and `docker`;
- push access restricted to protected branches.

Do not run a long-lived bare-metal runner process. Run the runner as a Docker container or another explicitly managed infrastructure container.

### Runner Container

Register a GitHub self-hosted runner for `chebizarro/bahia`, then run it as a container:

```bash
mkdir -p /srv/data/github-runners/bahia-edge

docker run -d --name github-runner-bahia-edge \
  --restart unless-stopped \
  -e REPO_URL=https://github.com/chebizarro/bahia \
  -e RUNNER_NAME=edge-01-bahia \
  -e RUNNER_LABELS=edge-01,docker \
  -e RUNNER_TOKEN='<github registration token>' \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/data/bahia-controlplane:/srv/data/bahia-controlplane \
  -v /srv/data/github-runners/bahia-edge:/runner \
  myoung34/github-runner:latest
```

The registration token is short-lived and should come from the GitHub runner setup UI or API. Do not commit it.

### Workflow

The checked-in workflow is `.github/workflows/deploy-edge.yml`. It:

1. runs on pushes to `master`;
2. requires the `self-hosted`, `edge-01`, and `docker` runner labels;
3. serializes deploys with `cancel-in-progress: false`;
4. grants only `contents: read` GitHub token permissions;
5. installs the pinned root-level Node dependency for the Soul gallery gate and preflights its WebSocket runtime;
6. preflights the branch, SHA, Docker access, Compose path, release root, backup directory, and required local tools;
7. builds `local/bahia-controlplane-bahia:github-<shortsha>` from the repository root;
8. builds `local/bahia-controlplane-web:github-<shortsha>` from `web/Dockerfile`;
9. stages the checkout to `/srv/data/bahia-controlplane/releases/github-<shortsha>` with `rsync -a --delete --exclude .git`;
10. captures the pre-rollout validated relay-policy event ID/hash/author/timestamp and backs up the live Compose file;
11. resolves the locally built backend and web image IDs and writes immutable `repository@sha256:<digest>` references;
12. validates and applies the updated Compose file;
13. waits for `/ready`, then requires the same-or-newer hydrated relay-policy projection plus relay, web, and Soul gallery reachability;
14. automatically restores the previous Compose file and services if any post-mutation gate fails:

```bash
curl -fsS http://127.0.0.1:8080/ready
curl -fsS -H 'Accept: application/nostr+json' http://127.0.0.1:3334/relay >/dev/null
curl -fsS http://127.0.0.1:8081/ >/dev/null
```

The workflow uses these image/build arguments:

```bash
tag="github-${GITHUB_SHA::7}"

docker build \
  --build-arg VERSION_BASE=0.1.0 \
  --build-arg GIT_COMMIT="$GITHUB_SHA" \
  --build-arg VERSION="0.1.0-$GITHUB_SHA" \
  -t "local/bahia-controlplane-bahia:$tag" .

docker build \
  -f web/Dockerfile \
  --build-arg PUBLIC_BAHIA_BOOTSTRAP_RELAYS="wss://bahia.sharegap.net/relay" \
  --build-arg PUBLIC_BAHIA_SERVICE_PUBKEYS="37202fe3be21ff51b97531655d3f053cf1999f30c9e27ab0f44bf364d8b53dcc" \
  --build-arg PUBLIC_BAHIA_GIT_COMMIT="$GITHUB_SHA" \
  --build-arg PUBLIC_BAHIA_WEB_VERSION="0.1.0-$GITHUB_SHA" \
  -t "local/bahia-controlplane-web:$tag" web
```

### Compose Mutation Helper

`scripts/deploy_edge_compose_update.py` is the only supported mutator for the live Compose file in this workflow. The helper accepts:

```bash
python3 scripts/deploy_edge_compose_update.py \
  --compose-file /srv/data/bahia-controlplane/docker-compose.yml \
  --tag github-<7 lowercase hex characters> \
  --release-dir /srv/data/bahia-controlplane/releases/github-<7 lowercase hex characters> \
  --backend-image sha256:<64 hex local image ID> \
  --web-image sha256:<64 hex local image ID>
```

It updates exactly:

- `bahia.image` and `relay.image` to the supplied backend digest reference;
- `web.image` to the supplied web digest reference;
- the single `/srv/data/bahia-controlplane/releases/.../docs:/docs:ro` mount to `<release-dir>/docs:/docs:ro`.

It refuses to write when any of these safety checks fail:

- tag is not `github-<7 lowercase hex characters>`;
- release directory is not `/srv/data/bahia-controlplane/releases/<tag>`;
- `bahia`, `relay`, or `web` service is missing;
- an expected service has no image line;
- an expected service has more than one image line;
- the release docs mount is missing;
- more than one release docs mount is present.

Deterministic helper coverage lives in `test/scripts/test_deploy_edge_compose_update.py` and runs with:

```bash
python3 -m unittest discover -s test/scripts -p 'test_*.py'
```

### Operational Notes

- The Soul gallery gate prefers the Node runtime's global `WebSocket` and loads the pinned root `ws` devDependency when the global is absent (including Node 18). Run `npm ci --include=dev --ignore-scripts && npm run check:soul-gallery-gate` to validate startup without contacting a relay; `npm test` exercises both global-present and global-absent startup paths.
- `/health` is liveness, not the active-tier readiness gate. Before declaring the edge rollout ready, operators must also require `curl -fsS http://127.0.0.1:8080/ready`; Bahia returns `503` when required readiness checks fail.
- The workflow automatically restores the prior Compose file and restarts the three services when the post-rollout readiness, projection, relay, or web gate fails.
- Keep `cancel-in-progress: false`; interrupted production deploys are worse than serialized deploys.
- The workflow updates the existing compose file in place, after backing it up.
- Builds retain local tags as handles, but the live Compose file is updated with immutable digest references.
- This path bypasses Bahia's deployment model. It is acceptable only until the durable Hive-CI path tracked by `bahia-fj0z` satisfies the retirement conditions below.

## Durable Path: Hive CI Artifact Publishing

The desired flow is:

```text
git push
  -> grasp-gitea publishes kind 5401 workflow run
  -> hive-ci-runner consumes 5401 and runs the workflow
  -> workflow builds/pushes image and prints a BAHIA_ARTIFACT marker
  -> loom-worker publishes an ordinary kind 5402 workflow result with immutable image metadata
  -> Bahia correlates the trusted 5401/5402 and registers the verified artifact
  -> optional release-provenance bridge publishes a second terminal RELEASE 5402
  -> Bahia verifies its complete supply-chain envelope and registers a digest-only artifact
  -> an operator separately signs an authorized ContextVM promotion intent
  -> Bahia/Loom executes a staged canary from the registered digest
```

### Required terminal RELEASE 5402 contract

Artifact registration consumes the canonical **second** kind `5402` emitted by
the grasp-gitea release-provenance path. Both the signed tags and JSON content
must identify `RELEASE`, and the result must be terminal and successful. The
content mirrors producer schema `hiveci.release-provenance.v1` with
`release_identity`, full `lineage`, `execution.worker_identity`, immutable
`manifest`, `sbom`, and `provenance` descriptors, plus the Signet artifact
attestation. Bahia joins the trusted signed kind `5401`, worker admission, and
repository policy evidence before accepting it.

The producer's optional `image_tag` is evidence only. It is never an artifact
identity, lookup, copy, or deployment input. The older `.hiveci-result.json`
shape used for an ordinary build-result `5402` does **not** qualify as a terminal
RELEASE registration.

### Hive Workflow Contract

The Loom `ci/workflow-run` profile does not read `.hiveci-result.json`. The
Hive-executed workflow must:

1. Build backend and web images.
2. Push images to Harbor or another registry Bahia can inspect.
3. Resolve the pushed manifest digest.
4. Print exactly one `BAHIA_ARTIFACT=<json>` line to stdout. The JSON keys are
   `image_repo`, `image_tag`, and `image_digest`.

Loom copies those three values into both the signed 5402 tags and its JSON
content. Bahia accepts the tag values first and uses the JSON content as a
compatibility source. A malformed content envelope is logged at WARN and can
only fall back to complete signed tags.

Example core shell:

```bash
set -euo pipefail

sha="${GITHUB_SHA:-$(git rev-parse HEAD)}"
short="${sha:0:7}"
image_repo="harbor.sharegap.net/cascadia/bahia"
image_tag="master-$short"

docker build \
  --build-arg VERSION_BASE=0.1.0 \
  --build-arg GIT_COMMIT="$sha" \
  --build-arg VERSION="0.1.0-$sha" \
  -t "$image_repo:$image_tag" .

docker push "$image_repo:$image_tag"

digest="$(docker inspect --format='{{index .RepoDigests 0}}' "$image_repo:$image_tag" | sed 's/^.*@//')"

printf 'BAHIA_ARTIFACT={"image_repo":"%s","image_tag":"%s","image_digest":"%s"}\n' \
  "$image_repo" "$image_tag" "$digest"
```

If backend and web stay as separate images, either:

- emit two Hive results mapped by two pipeline policies; or
- define a multi-artifact result extension before expecting Bahia to deploy both from one `5402`.

The simpler production path is one canonical backend/control-plane image through Hive CI, while the immediate relief workflow handles the split backend/web local-image deployment until retirement.

### Bahia Requirements

Bahia needs:

- `hiveci.enabled=true`;
- trusted CI dispatcher pubkeys configured;
- relay list including the relay where `5401` and `5402` are published;
- a `hiveci_pipeline_policies` row matching the repo coordinate and workflow path;
- registry inspection configured for the target registry;
- a trusted release-attestor key and OCI/Blossom evidence resolver for RELEASE results.

The bridge handles ordinary successful build-result `5402` events as the live
Loom integration path. The result must be signed either by the ephemeral
`publisher` declared by the trusted 5401 or by a key in
`hiveci.trusted_loom_worker_pubkeys`. It must include `image_repo`, `image_tag`,
and a full lowercase `sha256:<64 hex>` manifest digest. This path is separate
from terminal RELEASE acceptance.

A terminal RELEASE result is registered only after manifest, SBOM, and in-toto
provenance bytes match every signed descriptor and lineage binding. The
artifact identity is `repository@sha256:digest`; any signed image tag is stored
only as evidence. CI success does not promote production. The legacy `auto_deploy_staging` policy
metadata may create a staging intent; protected environments leave that intent
pending approval. Production promotion remains a separately authorized
control-plane action.

### Edge-01 Astillero configuration

The checked-in Compose configuration does not enable HiveCI by default. The
live edge configuration must add the following block, replacing every angle-
bracketed value with the observed deployment value:

```yaml
nostr:
  # Keep the existing sidecar settings. When mirror_external is false, the
  # external Hive/Loom relay must also appear under nostr.relays or loom.relays.
  relays:
    - "wss://<relay-carrying-5401-and-5402>"

loom:
  relays:
    - "wss://<relay-carrying-5401-and-5402>"

hiveci:
  enabled: true
  auto_register_builds: true
  trusted_ci_pubkeys:
    - "<grasp-gitea-5401-signer-hex>"
  trusted_loom_worker_pubkeys:
    - "<loom-worker-5402-signer-hex>"
  policies:
    - repo_coordinate: "30617:<repository-owner-pubkey>:<astillero-repository-id>"
      workflow_path: ".gitea/workflows/ci.yml"
      branch_pattern: "main"
      service_name: astillero
      environment_name: edge-01-production

harbor:
  enabled: true
  url: "https://harbor.<deployment-domain>"
  username: "<registry-read-account>"
  password: "<registry-read-password>"
```

The existing `astillero` service must have `artifact_repo` equal to the 5402
`image_repo` exactly. The workflow must print the `BAHIA_ARTIFACT` marker shown
above; the current Astillero CI workflow otherwise produces no artifact
identity for Bahia to register.

Do not add `trusted_release_attestors` for the ordinary Loom result path. That
key enables the stricter second RELEASE-5402 verifier and additionally requires
Bahia's OCI evidence service, full lineage/SBOM/provenance descriptors, worker
admission evidence, and the metadata constraints shown below.

On startup, verify the `hive-ci bridge enabled` log includes the expected relay
and non-zero trusted-key/policy counts. A `hiveci_disabled`,
`trusted_loom_worker_pubkeys_missing`, `unauthorized_signer`,
`missing_or_invalid_digest`, or `unmapped_repository` WARN identifies the exact
gate that prevented registration.

### Seeding the Pipeline Policy Row

Pipeline policies can be seeded in two ways: via config (preferred) or via the
operator SQL script.

#### Config-driven seeding (preferred)

Add the policy to `bahia.yml` under `hiveci.policies`.  Bahia resolves
service and environment by name and idempotently ensures the row exists on
every startup:

```yaml
hiveci:
  enabled: true
  trusted_ci_pubkeys:
    - "<hive-ci-dispatcher-pubkey>"
  trusted_release_attestors:
    - "<hive-ci-release-attestor-pubkey>"
  policies:
    - repo_coordinate: "30617:<grasp-gitea-pubkey>:chebizarro/bahia"
      workflow_path: ".gitea/workflows/release.yml"
      service_name: bahia
      environment_name: edge-01
      metadata:
        workflow_digest: "<sha256-hex-from-signed-5401>"
        policy_digest: "<policy-digest-from-signed-5401>"
        review_policy: "<review-policy-from-signed-5401>"
        source_repo_identity: "gitea.example/chebizarro/bahia"
        release_image_repository: "harbor.example/chebizarro/bahia"
        release_attestors:
          - "<hive-ci-release-attestor-pubkey>"
        rollback_compatibility:
          compatible_from_digests:
            - "sha256:<currently-staged-manifest>"
        health_contract: {type: http, path: /health, timeout_seconds: 10}
        readiness_contract: {type: http, path: /ready, timeout_seconds: 15}
```

The `repo_coordinate` is whatever grasp-gitea puts in the `["a", ...]` tag of
kind-5401 events.  Use the discovery query below to find it once 5401 events
have been ingested:

```bash
docker compose -f /srv/data/bahia-controlplane/docker-compose.yml \
  exec -T postgres psql -U bahia -d bahia \
  -c "SELECT repo_coordinate, workflow_path, count(*), max(event_created_at)
      FROM hiveci_workflow_runs
      GROUP BY 1, 2 ORDER BY 4 DESC;"
```

All six release constraints above are mandatory and exact-match. Omitting a
constraint, using an empty attestor list, or leaving placeholder values causes
RELEASE registration to fail closed. The rollback and health/readiness objects
are also required before a later canary promotion can be authorized.

After adding the config, restart Bahia.  The startup log will show
`hiveci pipeline policy ensured` for each configured policy.

#### Operator SQL script (ad-hoc / pre-config)

`scripts/seed_hiveci_pipeline_policy.sql` provides a standalone operator path
for creating or hardening the policy row without restarting Bahia. It reconciles
an existing matching row in place and inserts only when none exists, including
when `branch_pattern` is NULL:

```bash
REPO_COORD="30617:<grasp-gitea-pubkey>:chebizarro/bahia"
SERVICE_NAME="bahia"
ENV_NAME="edge-01"
WORKFLOW_DIGEST="<sha256-hex-from-5401>"
POLICY_DIGEST="<policy-digest-from-5401>"
REVIEW_POLICY="<review-policy-from-5401>"
SOURCE_REPO="<gitea-host/org/repo>"
IMAGE_REPO="<harbor-host/project/repo>"
RELEASE_ATTESTOR="<release-attestor-pubkey>"
PREVIOUS_DIGEST="sha256:<currently-staged-manifest>"
HEALTH_PATH="/health"
READINESS_PATH="/ready"

sed -e "s|:REPO_COORDINATE:|$REPO_COORD|g" \
    -e "s|:SERVICE_NAME:|$SERVICE_NAME|g" \
    -e "s|:ENV_NAME:|$ENV_NAME|g" \
    -e "s|:WORKFLOW_DIGEST:|$WORKFLOW_DIGEST|g" \
    -e "s|:POLICY_DIGEST:|$POLICY_DIGEST|g" \
    -e "s|:REVIEW_POLICY:|$REVIEW_POLICY|g" \
    -e "s|:SOURCE_REPO:|$SOURCE_REPO|g" \
    -e "s|:IMAGE_REPO:|$IMAGE_REPO|g" \
    -e "s|:RELEASE_ATTESTOR:|$RELEASE_ATTESTOR|g" \
    -e "s|:PREVIOUS_DIGEST:|$PREVIOUS_DIGEST|g" \
    -e "s|:HEALTH_PATH:|$HEALTH_PATH|g" \
    -e "s|:READINESS_PATH:|$READINESS_PATH|g" \
  scripts/seed_hiveci_pipeline_policy.sql \
  | docker compose -f /srv/data/bahia-controlplane/docker-compose.yml \
      exec -T postgres psql -U bahia -d bahia
```

The script also includes discovery queries for repo coordinates, services,
environments, and existing policies.

#### Promotion authorization

Do not enable promotion through Hive-CI policy metadata. Registered digests are
eligible inputs to the separate signed promotion-intent control-plane path;
until an authorized intent is accepted, no environment desired state changes.

### Implementation Order

1. ✅ Land the immediate self-hosted deploy workflow.
2. Confirm pushes to `master` rebuild and roll the live edge stack.
3. ✅ Add the Hive workflow that writes `.hiveci-result.json`.
4. Confirm `grasp-gitea` publishes `5401` for that workflow path.
5. Confirm `hive-ci-runner` publishes `5402` with image metadata.
6. ✅ Script for `hiveci_pipeline_policies` row available; run seeder once
   repo coordinate is known (see §Seeding above).
7. Verify Bahia creates the artifact instead of `artifact_pending`.
8. Submit a separately signed, RBAC-authorized ContextVM `service/deploy` promotion intent and verify the staged canary before any wider rollout.

## Retirement Condition For Immediate Relief

The direct compose-mutating workflow can be removed when:

- Hive CI emits valid artifact metadata for Bahia pushes;
- Bahia registers artifacts from `5402`;
- Bahia creates or accepts deployment intents for the target environment;
- deployment execution updates the same live compose stack safely;
- rollback is verified from a previous artifact and the restored stack passes `/ready` (not merely `/health`).
