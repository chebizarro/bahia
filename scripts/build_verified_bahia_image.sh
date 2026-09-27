#!/bin/sh
set -eu

# Build one exact Bahia image and prove its OCI and independently-deployed DNS
# agent provenance before it can be handed to deployment tooling.
#
# Nothing extracted from the image is executed here: the Dockerfile asserts the
# DNS agent's version on the build platform and bakes its digest into
# /usr/local/share/bahia/dns-agent-provenance.json. This helper only reads
# files and metadata, so it runs unchanged on Linux and macOS hosts.
repo=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repo"

revision=$(git rev-parse HEAD)
case "$revision" in
	????????????????????????????????????????) ;;
	*) echo "invalid full source revision" >&2; exit 1 ;;
esac
if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then
	echo "refusing to build a release image from a dirty tree" >&2
	exit 1
fi

tag=${1:-local/bahia:verified-${revision}}
version="0.1.0-${revision}"
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
docker build \
	--build-arg VERSION_BASE=0.1.0 \
	--build-arg GIT_COMMIT="$revision" \
	--build-arg BUILD_DATE="$build_date" \
	--build-arg RELAY_FLOOD_GUARD=2026-09-15-v1 \
	--build-arg VERSION="$version" \
	-t "$tag" .

python3 scripts/edge_image_admission.py verify-image \
	--repo . \
	--image "$tag" \
	--expected-revision "$revision"

image_revision=$(docker image inspect --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$tag")
[ "$image_revision" = "$revision" ]

container=$(docker create "$tag")
tmp=$(mktemp -d)
cleanup() {
	docker rm "$container" >/dev/null 2>&1 || true
	rm -rf "$tmp"
}
trap cleanup EXIT HUP INT TERM
docker cp "$container:/usr/local/share/bahia/dns-agent-provenance.json" "$tmp/dns-agent-provenance.json"
docker cp "$container:/usr/local/bin/bahia-dns-agent" "$tmp/bahia-dns-agent"

image_id=$(docker image inspect --format '{{.Id}}' "$tag")
python3 - "$tmp/dns-agent-provenance.json" "$tmp/bahia-dns-agent" "$tag" "$image_id" "$revision" "$version" <<'EOF'
import hashlib
import json
import sys

manifest_path, agent_path, tag, image_id, revision, version = sys.argv[1:]
with open(manifest_path, encoding="utf-8") as handle:
	manifest = json.load(handle)
with open(agent_path, "rb") as handle:
	digest = hashlib.sha256(handle.read()).hexdigest()

failures = []
if manifest.get("revision") != revision:
	failures.append(f"agent provenance revision {manifest.get('revision')!r} != {revision!r}")
if manifest.get("version") != version:
	failures.append(f"agent provenance version {manifest.get('version')!r} != {version!r}")
if manifest.get("sha256") != digest:
	failures.append(f"agent digest {digest} does not match provenance {manifest.get('sha256')!r}")
if failures:
	sys.exit("DNS agent provenance verification failed: " + "; ".join(failures))

print("BAHIA_VERIFIED_IMAGE=" + json.dumps(
	{"image": tag, "image_id": image_id, "revision": revision, "dns_agent_sha256": digest},
	separators=(",", ":"),
))
EOF
