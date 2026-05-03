#!/bin/bash
set -euo pipefail

apt-get update
apt-get install -y --no-install-recommends "$@"
apt-get clean
rm -rf /var/lib/apt/lists/*
