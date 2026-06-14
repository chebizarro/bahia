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
5. preflights the branch, SHA, Docker access, Compose path, release root, backup directory, and required local tools;
6. builds `local/bahia-controlplane-bahia:github-<shortsha>` from the repository root;
7. builds `local/bahia-controlplane-web:github-<shortsha>` from `web/Dockerfile`;
8. stages the checkout to `/srv/data/bahia-controlplane/releases/github-<shortsha>` with `rsync -a --delete --exclude .git`;
9. backs up `/srv/data/bahia-controlplane/docker-compose.yml` to `/srv/data/bahia-controlplane/backups/compose-github-<shortsha>-<utc timestamp>.yml`;
10. invokes `scripts/deploy_edge_compose_update.py` to update the live Compose images and docs mount;
11. validates the resulting Compose file with `docker compose -f "$COMPOSE_FILE" config --quiet`;
12. runs `docker compose -f "$COMPOSE_FILE" up -d bahia relay web`;
13. verifies:

```bash
curl -fsS http://127.0.0.1:8080/health
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
  --release-dir /srv/data/bahia-controlplane/releases/github-<7 lowercase hex characters>
```

It updates exactly:

- `bahia.image` and `relay.image` to `local/bahia-controlplane-bahia:<tag>`;
- `web.image` to `local/bahia-controlplane-web:<tag>`;
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

- Keep `cancel-in-progress: false`; interrupted production deploys are worse than serialized deploys.
- The workflow updates the existing compose file in place, after backing it up.
- The workflow uses local images because the current live stack already uses local tags.
- This path bypasses Bahia's deployment model. It is acceptable only until the durable Hive-CI path tracked by `bahia-fj0z` satisfies the retirement conditions below.

## Durable Path: Hive CI Artifact Publishing

The desired flow is:

```text
git push
  -> grasp-gitea publishes kind 5401 workflow run
  -> hive-ci-runner consumes 5401 and runs the workflow
  -> workflow builds/pushes image and writes .hiveci-result.json
  -> hive-ci-runner publishes kind 5402 workflow result
  -> Bahia ingests 5402 and registers build/artifact
  -> Bahia creates or receives deployment intent
  -> Bahia/Loom executes deployment
```

### Required 5402 Artifact Fields

Bahia's Hive CI subscriber accepts artifact metadata from either `5402` tags or JSON content:

```json
{
  "image_repo": "harbor.sharegap.net/cascadia/bahia",
  "image_tag": "master-<sha>",
  "image_digest": "sha256:...",
  "log_url": "..."
}
```

The current runner also reads `.hiveci-result.json` from the workflow checkout. The workflow should write:

```json
{
  "imageRepo": "harbor.sharegap.net/cascadia/bahia",
  "imageTag": "master-<sha>",
  "imageDigest": "sha256:...",
  "logURL": "..."
}
```

Without `image_repo`, `image_tag`, and `image_digest`, Bahia correctly leaves the result in `artifact_pending`.

### Hive Workflow Contract

The Hive-executed workflow should:

1. Build backend and web images.
2. Push images to Harbor or another registry Bahia can inspect.
3. Resolve the pushed manifest digest.
4. Write `.hiveci-result.json` in the repository root.

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

cat > .hiveci-result.json <<JSON
{
  "imageRepo": "$image_repo",
  "imageTag": "$image_tag",
  "imageDigest": "$digest",
  "logURL": "local://hive-ci/${sha}"
}
JSON
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
- policy metadata if auto deploy is wanted.

The bridge already transitions successful `5402` results:

- missing image metadata -> `artifact_pending`;
- missing registry manifest -> `artifact_pending`;
- valid image metadata and manifest -> build and artifact records;
- `auto_deploy_staging=true` metadata -> deployment intent.

### Seeding the Pipeline Policy Row

`scripts/seed_hiveci_pipeline_policy.sql` inserts the `hiveci_pipeline_policies`
row idempotently.  Run it by piping into the `postgres` container via
`docker compose exec`.

**Step 1 — discover the NIP-34 repo coordinate.**
The `repo_coordinate` is whatever grasp-gitea puts in the `["a", ...]` tag of
kind-5401 events.  The discovery query at the top of the SQL file shows every
coordinate already ingested:

```bash
docker compose -f /srv/data/bahia-controlplane/docker-compose.yml \
  exec -T postgres psql -U bahia -d bahia \
  -c "SELECT repo_coordinate, workflow_path, count(*), max(event_created_at)
      FROM hiveci_workflow_runs
      GROUP BY 1, 2 ORDER BY 4 DESC;"
```

**Step 2 — seed (substitute the repo coordinate).**

```bash
REPO_COORD="30617:<grasp-gitea-pubkey>:chebizarro/bahia"

sed "s|:REPO_COORDINATE:|$REPO_COORD|g" \
  scripts/seed_hiveci_pipeline_policy.sql \
  | docker compose -f /srv/data/bahia-controlplane/docker-compose.yml \
      exec -T postgres psql -U bahia -d bahia
```

The script resolves the `bahia` service and `edge-01` environment by name.
`ON CONFLICT DO NOTHING` makes it safe to run multiple times.

**Step 3 (later) — enable auto-deploy after artifact registration is stable.**

Only after step 8 of the Implementation Order is verified:

```bash
REPO_COORD="30617:<grasp-gitea-pubkey>:chebizarro/bahia"

docker compose -f /srv/data/bahia-controlplane/docker-compose.yml \
  exec -T postgres psql -U bahia -d bahia -c "
UPDATE hiveci_pipeline_policies
SET    metadata   = '{\"auto_deploy_staging\": true, \"staging_environment\": \"edge-01\"}'::jsonb,
       updated_at = now()
WHERE  repo_coordinate = '${REPO_COORD}'
  AND  workflow_path   = '.github/workflows/hive-ci-build.yml';"
```

### Implementation Order

1. ✅ Land the immediate self-hosted deploy workflow.
2. Confirm pushes to `master` rebuild and roll the live edge stack.
3. ✅ Add the Hive workflow that writes `.hiveci-result.json`.
4. Confirm `grasp-gitea` publishes `5401` for that workflow path.
5. Confirm `hive-ci-runner` publishes `5402` with image metadata.
6. ✅ Script for `hiveci_pipeline_policies` row available; run seeder once
   repo coordinate is known (see §Seeding above).
7. Verify Bahia creates the artifact instead of `artifact_pending`.
8. Enable auto-deploy policy only after artifact registration is stable.

## Retirement Condition For Immediate Relief

The direct compose-mutating workflow can be removed when:

- Hive CI emits valid artifact metadata for Bahia pushes;
- Bahia registers artifacts from `5402`;
- Bahia creates or accepts deployment intents for the target environment;
- deployment execution updates the same live compose stack safely;
- rollback is verified from a previous artifact.
