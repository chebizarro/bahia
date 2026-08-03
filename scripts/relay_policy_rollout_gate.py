#!/usr/bin/env python3
"""Capture and verify Bahia's relay-policy rollout invariant via /ready."""

from __future__ import annotations

import argparse
import json
import re
import sys
import urllib.error
import urllib.request
from datetime import datetime
from pathlib import Path
from typing import Any

HEX64 = re.compile(r"^[0-9a-f]{64}$")
CHECK_NAME = "relay_policy_projection"


class RolloutGateError(ValueError):
    pass


def _parse_time(value: str) -> datetime:
    try:
        return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except ValueError as exc:
        raise RolloutGateError("relay policy event_created_at is invalid") from exc


def projection_from_readiness(payload: dict[str, Any]) -> dict[str, str]:
    checks = payload.get("checks")
    if not isinstance(checks, list):
        raise RolloutGateError("readiness response has no health checks")
    check = next((item for item in checks if isinstance(item, dict) and item.get("name") == CHECK_NAME), None)
    if check is None or check.get("status") != "pass":
        raise RolloutGateError("relay policy projection health check did not pass")
    details = check.get("details")
    if not isinstance(details, dict) or details.get("availability") != "available":
        raise RolloutGateError("relay policy projection is unavailable")
    result = {key: str(details.get(key, "")).strip().lower() for key in ("event_id", "hash", "author")}
    result["event_created_at"] = str(details.get("event_created_at", "")).strip()
    result["confirmation"] = str(details.get("confirmation", "")).strip()
    for key in ("event_id", "hash", "author"):
        if not HEX64.fullmatch(result[key]):
            raise RolloutGateError(f"relay policy {key} is missing or invalid")
    _parse_time(result["event_created_at"])
    if result["confirmation"] not in {"cached", "relay_confirmed"}:
        raise RolloutGateError("relay policy confirmation state is invalid")
    return result


def require_same_or_newer(before: dict[str, str], after: dict[str, str]) -> None:
    if before["author"] != after["author"]:
        raise RolloutGateError("relay policy author changed during rollout")
    before_time = _parse_time(before["event_created_at"])
    after_time = _parse_time(after["event_created_at"])
    if after_time < before_time:
        raise RolloutGateError("relay policy regressed to an older event")
    if after_time == before_time:
        if after["event_id"] == before["event_id"]:
            if after["hash"] != before["hash"]:
                raise RolloutGateError("relay policy hash mismatch for the rollout baseline event")
        elif after["event_id"] > before["event_id"]:
            raise RolloutGateError("relay policy regressed by replaceable-event ordering")


def fetch_readiness(url: str) -> dict[str, Any]:
    try:
        with urllib.request.urlopen(url, timeout=15) as response:
            raw = response.read()
    except urllib.error.HTTPError as exc:
        raw = exc.read()
    except OSError as exc:
        raise RolloutGateError(f"readiness request failed: {exc}") from exc
    try:
        payload = json.loads(raw)
    except (TypeError, json.JSONDecodeError) as exc:
        raise RolloutGateError("readiness response is not valid JSON") from exc
    if not isinstance(payload, dict):
        raise RolloutGateError("readiness response must be an object")
    return payload


def capture(url: str, output: Path) -> None:
    projection = projection_from_readiness(fetch_readiness(url))
    output.write_text(json.dumps(projection, sort_keys=True) + "\n", encoding="utf-8")


def verify(url: str, baseline_path: Path) -> None:
    before = json.loads(baseline_path.read_text(encoding="utf-8"))
    if not isinstance(before, dict):
        raise RolloutGateError("rollout baseline must be an object")
    after = projection_from_readiness(fetch_readiness(url))
    require_same_or_newer(before, after)


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("command", choices=("capture", "verify"))
    parser.add_argument("--url", required=True)
    parser.add_argument("--baseline", required=True, type=Path)
    args = parser.parse_args()
    try:
        if args.command == "capture":
            capture(args.url, args.baseline)
        else:
            verify(args.url, args.baseline)
    except (OSError, json.JSONDecodeError, RolloutGateError) as exc:
        print(f"relay_policy_rollout_gate: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
