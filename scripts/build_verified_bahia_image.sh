#!/bin/sh
set -eu

# Build one exact Bahia image and prove its OCI and independently-deployed DNS
# agent provenance before it can be handed to deployment tooling.
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
build_date=$(date -u +%Y-%m-%dT%H:%M:%SZ)
docker build \
	--build-arg VERSION_BASE=0.1.0 \
	--build-arg GIT_COMMIT="$revision" \
	--build-arg BUILD_DATE="$build_date" \
	--build-arg RELAY_FLOOD_GUARD=2026-09-15-v1 \
	--build-arg VERSION="0.1.0-${revision}" \
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
	rm -f "$tmp/bahia-dns-agent"
	rmdir "$tmp" 2>/dev/null || true
}
trap cleanup EXIT HUP INT TERM
docker cp "$container:/usr/local/bin/bahia-dns-agent" "$tmp/bahia-dns-agent"
dns_version=$($tmp/bahia-dns-agent --version)
[ "$dns_version" = "0.1.0-${revision}" ]

image_id=$(docker image inspect --format '{{.Id}}' "$tag")
dns_sha256=$(sha256sum "$tmp/bahia-dns-agent" | awk '{print $1}')
printf 'BAHIA_VERIFIED_IMAGE={"image":"%s","image_id":"%s","revision":"%s","dns_agent_sha256":"%s"}\n' \
	"$tag" "$image_id" "$revision" "$dns_sha256"
