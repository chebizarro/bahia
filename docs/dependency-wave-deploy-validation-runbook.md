# Dependency-wave deploy validation runbook

This runbook covers the live-only remainder of **bahia-8p0qf** (container base
images wave 4) and **bahia-ycohv** (GitHub Actions wave 1). It does **not**
authorize a deployment or release. Commands under an **authorized live window**
heading mutate production or publish artifacts and require separate approval.

## Verified without a deployment

The following evidence was collected on 2026-09-23 from revision
'e73c003c9637b6fdff13b0fcfb6ab8e6b19c2890'.

### Workflow and action-runtime matrix

| Workflow | Effective runtime | Wave-1 action | Conclusion |
| --- | --- | --- | --- |
| 'deploy-edge.yml' | '[self-hosted, edge-01, docker]' | None; checkout uses 'git' | The only registered runner, 'edge-01-bahia', is online, Linux/X64, and Actions Runner '2.335.1'. |
| 'deploy-openclaw-soulfactory-sidecar.yml' | '[self-hosted, max, docker]' | 'actions/upload-artifact@v7' | No 'max' runner is registered, so this job cannot schedule. |
| 'hive-ci-build.yml' | Declares 'ubuntu-latest'; the durable path executes it through Hive's 'nektos/act' runtime | 'actions/checkout@v7' | The deployed 'act' version is not recorded here. 'act' first added Node 24 action support in 'v0.2.81'. |
| 'loom-release.yml' | 'ubuntu-latest' release runner | None | Its tag-triggered image publication still needs an authorized release, but it has no wave-1 action-runtime gap. |

'actions/checkout@v7', 'actions/setup-go@v7', 'actions/setup-node@v7',
'actions/upload-artifact@v7', and 'pnpm/action-setup@v6' all declare
'runs.using: node24'. A self-hosted GitHub Actions runner therefore needs
version '2.327.1' or newer. The Go/web PR jobs run their upgraded actions on
GitHub-hosted runners; the uncovered paths are the absent 'max' runner and
Hive's separately managed 'act' runtime. The version floors come from the
[Actions Runner 2.327.1 release](https://github.com/actions/runner/releases/tag/v2.327.1)
and the [act 0.2.81 release](https://github.com/nektos/act/releases/tag/v0.2.81).

Recheck the registered runners immediately before an approved run:

~~~bash
gh api repos/chebizarro/bahia/actions/runners --paginate \
  | tee runners.json \
  | jq '.runners[] | {id,name,os,status,busy,version,labels:[.labels[].name]}'
~~~

Fail the preflight unless the selected runner is online, Linux/X64, carries all
workflow labels, and satisfies the version floor:

~~~bash
RUNNER_NAME=edge-01-bahia
runner_version="$(jq -r --arg name "$RUNNER_NAME" \
  '.runners[] | select(.name == $name) | .version' runners.json)"
test -n "$runner_version"
printf '%s\n%s\n' 2.327.1 "$runner_version" | sort -V -C
~~~

### Wave-4 local image evidence

Clean 'git archive' build contexts were used. No production service, registry,
or relay was contacted.

| Image | Local image ID | Evidence |
| --- | --- | --- |
| Backend | 'sha256:7502c33b5c9f6edeae052f7c9872aa5429652d37dae7797c6cbc44a9e0112959' | Linux/amd64; revision label matches 'e73c003c…'; Alpine 3.24.2; non-root UID 100/GID 101; server, relay, and CLI help entrypoints execute. |
| Web | 'sha256:b21a01ec66d39363d7b803982425200410bc3ad7d56455b00de388dff4c030d9' | Linux/amd64; nginx config valid; Docker health became 'healthy'; HTTP 200; bootstrap relay/pubkey substitution succeeded. |
| Recovery | 'sha256:ebb8d7a443f92b9bca31895654ff24f409a4d8e3f46d3877d938dc3d67297dc4' | Linux/amd64; Alpine 3.24.2; server and relay help execute; Docker CLI 29.5.3 and Compose 5.1.4 run; read-only API 1.48 negotiation succeeded with local Engine 28.0.4. |

The recovery inputs copied from that backend build are static x86-64 ELF
binaries built with Go 1.27.0 and 'CGO_ENABLED=0':

~~~text
bahia-relay   sha256:9b960392357b2d1527ef845aee402ced121933419b71cb72f45b7b87390f8d6e
bahia-server  sha256:b2cf0a3e54acd2e5561422d6ad630b6a67f204140273a356edc5c0fca535db93
~~~

The four deploy/release workflows and all Compose YAML contain no remaining
'golang:1.26.3', 'alpine:3.21', or 'node:22' base reference. The application
tags in 'docker-compose.edge-soulfactory.yml' and
'docker-compose.late-result.yml' are intentionally unchanged prebuilt images,
not base-image references; do not silently retag them.

## bahia-8p0qf: authorized live window

Local builds do not establish production volume/socket permissions, database
and relay behavior, target-daemon compatibility, or rollback.

### 1. Capture the pre-run and rollback inputs

Use the existing 'docs/investigations/' convention for the final sanitized
record. Keep secrets and raw environment files out of it. On 'edge-01':

~~~bash
set -euo pipefail
export REV='<approved 40-hex Bahia revision>'
export COMPOSE=/srv/data/bahia-controlplane/docker-compose.yml
[[ "$REV" =~ ^[0-9a-f]{40}$ ]]

date -u +%FT%TZ
hostname -f
sha256sum "$COMPOSE"
docker compose -f "$COMPOSE" config --images
docker compose -f "$COMPOSE" images
curl -fsS http://127.0.0.1:8080/ready
~~~

Record the current image references/IDs and verify the workflow's automatic
rollback target is available before continuing.

### 2. Dispatch the edge workflow

This command deploys:

~~~bash
gh workflow run deploy-edge.yml --ref master \
  -f release_revision="$REV" \
  -f release_retention_count=5 \
  -f image_retention_days=30

RUN_ID="$(gh run list --workflow deploy-edge.yml --event workflow_dispatch \
  --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$RUN_ID" --exit-status
gh run view "$RUN_ID" --json databaseId,headSha,status,conclusion,url,jobs
~~~

Record the run ID/URL, runner, image IDs and revision labels, release directory,
pre-run references, and rollback backup path. If a post-mutation gate fails,
require evidence that the rollback-safe Compose state was restored and '/ready'
passes.

### 3. Verify the deployed edge stack

~~~bash
set -euo pipefail
export COMPOSE=/srv/data/bahia-controlplane/docker-compose.yml

docker compose -f "$COMPOSE" config --quiet
docker compose -f "$COMPOSE" ps
docker compose -f "$COMPOSE" images
curl -fsS http://127.0.0.1:8080/ready | tee ready.json
curl -fsS -H 'Accept: application/nostr+json' \
  http://127.0.0.1:3334/relay | tee relay-nip11.json
curl -fsS http://127.0.0.1:8081/ >/dev/null

docker compose -f "$COMPOSE" exec -T postgres pg_isready -U bahia -d bahia
docker compose -f "$COMPOSE" exec -T postgres \
  psql -U bahia -d bahia -Atqc 'select 1'

docker compose -f "$COMPOSE" exec -T bahia id
docker compose -f "$COMPOSE" exec -T bahia sh -c \
  'test -w /var/lib/bahia/nostr-archive; stat -c "%u:%g %a %n" /var/lib/bahia/nostr-archive /var/run/docker.sock'
docker compose -f "$COMPOSE" exec -T relay sh -c \
  'test -w /var/lib/bahia/relay-sidecar; stat -c "%u:%g %a %n" /var/lib/bahia/relay-sidecar'

host_socket_gid="$(stat -c '%g' /var/run/docker.sock)"
docker compose -f "$COMPOSE" exec -T bahia sh -c \
  "id -G | tr ' ' '\n' | grep -Fx '$host_socket_gid'"

docker compose -f "$COMPOSE" exec -T bahia sh -c \
  'test -s /etc/ssl/certs/ca-certificates.crt; test -e /usr/share/zoneinfo/UTC; date -u; wget -q --spider https://api.github.com/'

docker compose -f "$COMPOSE" logs --no-color --since 30m \
  postgres bahia relay web > deploy-services.log
~~~

Inspect 'ready.json' and the logs for completed migrations, database errors,
relay failures, crash loops, and permission errors. If policy forbids
'api.github.com', use an approved HTTPS dependency and record the hostname; the
purpose is to exercise DNS, CA, and TLS from the final Alpine runtime.

### 4. Verify recovery on every recovery host class

Build or promote the recovery image from the exact backend build above. Record
the source backend image ID, both binary hashes, final recovery image ID, and
transfer/promotion mechanism. On each target host:

~~~bash
set -euo pipefail
export RECOVERY_IMAGE='<repository@sha256:digest or local immutable image ID>'

date -u +%FT%TZ
hostname -f
uname -m
docker version
docker image inspect "$RECOVERY_IMAGE" \
  --format 'id={{.Id}} os={{.Os}} arch={{.Architecture}} entrypoint={{json .Config.Entrypoint}}'

docker run --rm --network none "$RECOVERY_IMAGE" --help
docker run --rm --network none --entrypoint bahia-relay \
  "$RECOVERY_IMAGE" --help
docker run --rm --network none --entrypoint sh "$RECOVERY_IMAGE" -c \
  'cat /etc/alpine-release; ldd /usr/local/bin/bahia-server 2>&1 || true; docker --version; docker compose version'

docker run --rm -v /var/run/docker.sock:/var/run/docker.sock \
  --entrypoint docker "$RECOVERY_IMAGE" version \
  --format 'client={{.Client.Version}} server={{.Server.Version}} client_api={{.Client.APIVersion}} server_api={{.Server.APIVersion}}'
~~~

The last command is read-only API negotiation, but socket access is privileged.
Repeat it for every architecture/daemon class; do not infer arm64 acceptance
from the amd64 evidence.

Close 'bahia-8p0qf' only after the durable record has exact hosts, revisions,
image digests, binary hashes, all checks above, and a verified rollback to the
captured references (or an approved rollback rehearsal proving that exact
path).

## bahia-ycohv: authorized live window

### 1. Provision and preflight the sidecar runner

The sidecar job cannot schedule until the actual 'max' runtime has a runner with
'self-hosted,max,docker'. Require it to be online, idle, Linux/X64, at Actions
Runner '2.327.1' or newer, and verify its bundled
'externals/node24/bin/node --version' works. Do not retarget the workflow to
'edge-01'; the Compose path and incumbent checks are bound to 'max'.

### 2. Run the approved sidecar release and verify its named ZIP

This command deploys the sidecar:

~~~bash
export REV='<approved 40-hex Bahia revision>'
gh workflow run deploy-openclaw-soulfactory-sidecar.yml --ref master \
  -f release_sha="$REV"

RUN_ID="$(gh run list --workflow deploy-openclaw-soulfactory-sidecar.yml \
  --event workflow_dispatch --limit 1 --json databaseId \
  --jq '.[0].databaseId')"
gh run watch "$RUN_ID" --exit-status
gh run view "$RUN_ID" --json databaseId,headSha,status,conclusion,url,jobs

gh api "repos/chebizarro/bahia/actions/runs/$RUN_ID/artifacts" \
  | tee sidecar-artifacts.json \
  | jq '.artifacts[] | {id,name,size_in_bytes,expired,digest,archive_download_url}'

jq -e --arg name "openclaw-soulfactory-sidecar-$REV" \
  '.artifacts[] | select(.name == $name and .expired == false)' \
  sidecar-artifacts.json >/dev/null

mkdir -p sidecar-artifact
gh run download "$RUN_ID" \
  --name "openclaw-soulfactory-sidecar-$REV" \
  --dir sidecar-artifact
jq -e --arg rev "$REV" \
  '.schema == "bahia-openclaw-soulfactory-sidecar-release/v1" and
   .revision == $rev and .image_tag == $rev and
   (.image_digest | test("^sha256:[0-9a-f]{64}$"))' \
  sidecar-artifact/*.json
~~~

Record the run ID, runner/version, action result, artifact ID/name/digest,
release record, deployed container digest/revision, '/ready', and rollback
result. The named artifact is the 'upload-artifact@v7' evidence; a successful
image rollout alone is not.

### 3. Verify the real Hive act runtime

On the actual Hive runner before the next approved build:

~~~bash
set -euo pipefail
act --version
act_version="$(act --version | sed -E \
  's/.*version[[:space:]]+v?([0-9.]+).*/\1/')"
test -n "$act_version"
printf '%s\n%s\n' 0.2.81 "$act_version" | sort -V -C
~~~

Do not dispatch 'hive-ci-build.yml' through GitHub-hosted Actions and do not use
a reduced fake workflow as acceptance. The real Hive path must exercise
'actions/checkout@v7' and the Harbor/result boundary.

For the next approved Hive build, record and verify:

1. the kind-5401 run event ID, source revision, workflow path/digest, worker
   identity, 'act --version', and job image;
2. checkout v7 completes with 'HEAD' equal to the 5401 source revision and no
   Node runtime fallback/error;
3. backend and web pushes resolve immutable Harbor manifest digests;
4. '.hiveci-result.json' has non-empty 'imageRepo', 'imageTag', 'imageDigest',
   and 'logURL', with the digest matching Harbor; and
5. the correlated signed kind-5402 carries the same image metadata and Bahia
   registers the artifact rather than leaving it 'artifact_pending'.

Close 'bahia-ycohv' only after both the sidecar named-ZIP evidence and the real
Hive checkout/publication evidence are recorded. Until then it is narrowed, not
validated.
