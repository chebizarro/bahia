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

Add `.github/workflows/deploy-edge.yml`:

```yaml
name: Deploy Bahia Edge

on:
  push:
    branches: [master]

concurrency:
  group: bahia-edge-deploy
  cancel-in-progress: false

jobs:
  deploy:
    runs-on: [self-hosted, edge-01, docker]
    steps:
      - uses: actions/checkout@v4

      - name: Build and deploy local images
        env:
          COMPOSE_FILE: /srv/data/bahia-controlplane/docker-compose.yml
          RELEASE_ROOT: /srv/data/bahia-controlplane/releases
          PUBLIC_BAHIA_BOOTSTRAP_RELAYS: wss://bahia.sharegap.net/relay
          PUBLIC_BAHIA_SERVICE_PUBKEYS: 37202fe3be21ff51b97531655d3f053cf1999f30c9e27ab0f44bf364d8b53dcc
        run: |
          set -euo pipefail

          tag="github-${GITHUB_SHA::7}"
          release_dir="$RELEASE_ROOT/$tag"
          backup="/srv/data/bahia-controlplane/backups/compose-$(date +%Y%m%d-%H%M%S).yml"

          mkdir -p "$release_dir" /srv/data/bahia-controlplane/backups
          rsync -a --delete --exclude .git ./ "$release_dir/"

          docker build \
            --build-arg VERSION_BASE=0.1.0 \
            --build-arg GIT_COMMIT="$GITHUB_SHA" \
            --build-arg VERSION="0.1.0-$GITHUB_SHA" \
            -t "local/bahia-controlplane-bahia:$tag" .

          docker build \
            -f web/Dockerfile \
            --build-arg PUBLIC_BAHIA_BOOTSTRAP_RELAYS="$PUBLIC_BAHIA_BOOTSTRAP_RELAYS" \
            --build-arg PUBLIC_BAHIA_SERVICE_PUBKEYS="$PUBLIC_BAHIA_SERVICE_PUBKEYS" \
            --build-arg PUBLIC_BAHIA_GIT_COMMIT="$GITHUB_SHA" \
            --build-arg PUBLIC_BAHIA_WEB_VERSION="0.1.0-$GITHUB_SHA" \
            -t "local/bahia-controlplane-web:$tag" web

          cp "$COMPOSE_FILE" "$backup"

          COMPOSE_FILE="$COMPOSE_FILE" TAG="$tag" RELEASE_DIR="$release_dir" python3 - <<'PY'
          import os
          from pathlib import Path

          path = Path(os.environ["COMPOSE_FILE"])
          tag = os.environ["TAG"]
          release_dir = os.environ["RELEASE_DIR"]

          service = None
          out = []
          for line in path.read_text().splitlines():
              stripped = line.strip()
              if line.startswith("  ") and not line.startswith("    ") and stripped.endswith(":"):
                  service = stripped[:-1]
              if stripped.startswith("image:") and service in {"bahia", "relay"}:
                  line = f"    image: local/bahia-controlplane-bahia:{tag}"
              elif stripped.startswith("image:") and service == "web":
                  line = f"    image: local/bahia-controlplane-web:{tag}"
              elif "/srv/data/bahia-controlplane/releases/" in line and ":/docs:ro" in line:
                  line = f"      - {release_dir}/docs:/docs:ro"
              out.append(line)

          path.write_text("\n".join(out) + "\n")
          PY

          docker compose -f "$COMPOSE_FILE" up -d bahia relay web

          curl -fsS http://127.0.0.1:8080/health
          curl -fsS -H 'Accept: application/nostr+json' http://127.0.0.1:3334/relay >/dev/null
          curl -fsS http://127.0.0.1:8081/ >/dev/null
```

### Operational Notes

- Keep `cancel-in-progress: false`; interrupted production deploys are worse than serialized deploys.
- The workflow updates the existing compose file in place, after backing it up.
- The workflow uses local images because the current live stack already uses local tags.
- This path bypasses Bahia's deployment model. It is acceptable as a temporary bootstrap shortcut, not the final architecture.

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

For now, the simpler production path is one canonical backend/control-plane image through Hive CI, while the immediate relief workflow keeps handling the split backend/web local-image deployment.

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

### Implementation Order

1. Land the immediate self-hosted deploy workflow.
2. Confirm pushes to `master` rebuild and roll the live edge stack.
3. Add the Hive workflow that writes `.hiveci-result.json`.
4. Confirm `grasp-gitea` publishes `5401` for that workflow path.
5. Confirm `hive-ci-runner` publishes `5402` with image metadata.
6. Add or repair the matching `hiveci_pipeline_policies` row.
7. Verify Bahia creates the artifact instead of `artifact_pending`.
8. Enable auto-deploy policy only after artifact registration is stable.

## Retirement Condition For Immediate Relief

The direct compose-mutating workflow can be removed when:

- Hive CI emits valid artifact metadata for Bahia pushes;
- Bahia registers artifacts from `5402`;
- Bahia creates or accepts deployment intents for the target environment;
- deployment execution updates the same live compose stack safely;
- rollback is verified from a previous artifact.
