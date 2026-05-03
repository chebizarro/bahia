#!/bin/bash
set -euo pipefail

npm install -g "$@"
npm cache clean --force
