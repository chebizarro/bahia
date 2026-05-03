#!/bin/bash
set -euo pipefail

if command -v uv >/dev/null 2>&1; then
    uv pip install --system "$@"
else
    pip install --no-cache-dir "$@"
fi
