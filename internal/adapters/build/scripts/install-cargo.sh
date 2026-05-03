#!/bin/bash
set -euo pipefail

# Cargo provisioning typically uses a multi-stage Docker pattern.
# For now this script installs directly when cargo is available.
CRATE="${1:-}"
VERSION="${2:-}"
if [[ -z "$CRATE" ]]; then
  echo "usage: install-cargo.sh <crate> [version]" >&2
  exit 1
fi

if ! command -v cargo >/dev/null 2>&1; then
  echo "cargo not available in runtime image; use multi-stage rust builder for $CRATE" >&2
  exit 1
fi

if [[ -n "$VERSION" ]]; then
  cargo install --locked --version "$VERSION" "$CRATE"
else
  cargo install --locked "$CRATE"
fi
