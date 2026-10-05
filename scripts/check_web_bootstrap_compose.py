#!/usr/bin/env python3
"""Fail an edge deploy unless rendered Compose passes runtime trust roots to web."""

import json
import sys

DOCS = "docs/web-app-setup.md"
RELAY_KEY = "PUBLIC_BAHIA_BOOTSTRAP_RELAYS"
PUBKEY_KEYS = ("PUBLIC_BAHIA_SERVICE_PUBKEYS", "PUBLIC_BAHIA_SERVICE_PUBKEY")


def require_web_runtime_seed(config):
    services = config.get("services") if isinstance(config, dict) else None
    web = services.get("web") if isinstance(services, dict) else None
    if not isinstance(web, dict):
        raise ValueError(f"rendered Compose config has no web service; see {DOCS}")

    environment = web.get("environment") or {}
    if isinstance(environment, list):
        environment = dict(item.split("=", 1) if "=" in item else (item, "") for item in environment if isinstance(item, str))
    if not isinstance(environment, dict):
        environment = {}

    def present(name):
        value = environment.get(name)
        return isinstance(value, str) and bool(value.strip())

    missing = []
    if not present(RELAY_KEY):
        missing.append(RELAY_KEY)
    if not any(present(name) for name in PUBKEY_KEYS):
        missing.append("PUBLIC_BAHIA_SERVICE_PUBKEYS (or PUBLIC_BAHIA_SERVICE_PUBKEY)")
    if missing:
        raise ValueError(f"web.environment missing non-empty {', '.join(missing)}; add the runtime seed before deploying (see {DOCS}). Running stack unchanged.")


def main():
    try:
        require_web_runtime_seed(json.load(sys.stdin))
    except (ValueError, json.JSONDecodeError) as error:
        print(f"ERROR: {error}", file=sys.stderr)
        return 1
    print("web runtime bootstrap seed present in rendered Compose config")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
