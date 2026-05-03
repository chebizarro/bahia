#!/bin/bash
set -euo pipefail

LOCK_FILE="${1:-}"
if [[ -z "$LOCK_FILE" || ! -f "$LOCK_FILE" ]]; then
  echo "usage: install-tools.sh <tools.lock.json>" >&2
  exit 1
fi

python3 - "$LOCK_FILE" <<'PY'
import json, os, subprocess, sys

lock_file = sys.argv[1]
with open(lock_file, "r", encoding="utf-8") as f:
    lock = json.load(f)

groups = {}
for tool in lock.get("tools", []):
    mgr = tool.get("manager", "").strip()
    name = tool.get("name", "").strip()
    version = tool.get("version", "").strip()
    if not mgr or not name:
        continue
    groups.setdefault(mgr, []).append((name, version))

for manager, items in groups.items():
    if manager == "apt":
        args = [f"{name}={version}" if version else name for name, version in items]
        subprocess.check_call(["/usr/local/bin/install-apt", *args])
    elif manager == "pip":
        args = [f"{name}=={version}" if version else name for name, version in items]
        subprocess.check_call(["/usr/local/bin/install-pip", *args])
    elif manager == "npm":
        args = [f"{name}@{version}" if version else name for name, version in items]
        subprocess.check_call(["/usr/local/bin/install-npm", *args])
    elif manager == "cargo":
        for name, version in items:
            if version:
                subprocess.check_call(["/usr/local/bin/install-cargo", name, version])
            else:
                subprocess.check_call(["/usr/local/bin/install-cargo", name])
    else:
        raise SystemExit(f"unsupported package manager: {manager}")
PY
