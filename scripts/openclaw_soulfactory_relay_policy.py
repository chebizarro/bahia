#!/usr/bin/env python3
"""Reconcile the OpenClaw SoulFactory sidecar relay environment fail-closed."""

from __future__ import annotations

import argparse
import json
import os
import stat
import sys
import tempfile
from pathlib import Path
from typing import Iterable
from urllib.parse import urlparse

SCHEMA = "cascadia.bahia.openclaw-soulfactory-relay-policy.v1"
RELAY_KEYS = (
    "SOULFACTORY_RELAYS",
    "OPENCLAW_SOULFACTORY_READ_RELAYS",
    "OPENCLAW_SOULFACTORY_WRITE_RELAYS",
    "OPENCLAW_SOULFACTORY_CONTROL_RELAYS",
)


class RelayPolicyError(ValueError):
    """Raised when relay policy or environment state is unsafe."""


def load_policy(path: Path) -> tuple[str, ...]:
    try:
        policy = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise RelayPolicyError(f"cannot load relay policy: {exc}") from exc
    if policy.get("schema") != SCHEMA:
        raise RelayPolicyError("unsupported or missing relay-policy schema")
    relays = policy.get("relays")
    retired = policy.get("retired_relays")
    if not isinstance(relays, list) or not relays:
        raise RelayPolicyError("relay policy must contain a non-empty relays list")
    if not isinstance(retired, list):
        raise RelayPolicyError("relay policy retired_relays must be a list")
    if len(set(relays)) != len(relays):
        raise RelayPolicyError("relay policy contains duplicate relays")
    if set(relays) & set(retired):
        raise RelayPolicyError("active and retired relay sets overlap")
    for relay in [*relays, *retired]:
        if not isinstance(relay, str) or any(char in relay for char in "\x00\r\n,="):
            raise RelayPolicyError("relay policy contains an invalid relay value")
        parsed = urlparse(relay)
        if not parsed.hostname or parsed.scheme not in {"ws", "wss"}:
            raise RelayPolicyError("relay policy contains an invalid WebSocket URL")
    for relay in relays:
        if urlparse(relay).scheme != "wss":
            raise RelayPolicyError("active relays must use wss")
    return tuple(relays)


def parse_env_lines(text: str) -> tuple[list[str], dict[str, str]]:
    lines = text.splitlines()
    values: dict[str, str] = {}
    counts = {key: 0 for key in RELAY_KEYS}
    for line in lines:
        if not line or line.lstrip().startswith("#") or "=" not in line:
            continue
        key, value = line.split("=", 1)
        if key in counts:
            counts[key] += 1
            values[key] = value
    missing = [key for key, count in counts.items() if count == 0]
    duplicate = [key for key, count in counts.items() if count > 1]
    if missing:
        raise RelayPolicyError("missing required relay keys: " + ", ".join(missing))
    if duplicate:
        raise RelayPolicyError("duplicate relay keys: " + ", ".join(duplicate))
    return lines, values


def desired_value(relays: tuple[str, ...]) -> str:
    return ",".join(relays)


def check_values(values: dict[str, str], relays: tuple[str, ...]) -> None:
    expected = desired_value(relays)
    drifted = [key for key in RELAY_KEYS if values.get(key) != expected]
    if drifted:
        raise RelayPolicyError("relay policy drift on keys: " + ", ".join(drifted))


def render_env(text: str, relays: tuple[str, ...]) -> str:
    lines, _ = parse_env_lines(text)
    expected = desired_value(relays)
    rendered: list[str] = []
    for line in lines:
        if "=" in line and line.split("=", 1)[0] in RELAY_KEYS:
            rendered.append(f"{line.split('=', 1)[0]}={expected}")
        else:
            rendered.append(line)
    return "\n".join(rendered) + ("\n" if text.endswith("\n") else "")


def atomic_write(path: Path, text: str) -> None:
    current = path.stat()
    fd, temporary_name = tempfile.mkstemp(prefix=f".{path.name}.", dir=path.parent)
    temporary = Path(temporary_name)
    try:
        with os.fdopen(fd, "w", encoding="utf-8") as handle:
            os.fchmod(handle.fileno(), stat.S_IMODE(current.st_mode))
            os.fchown(handle.fileno(), current.st_uid, current.st_gid)
            handle.write(text)
            handle.flush()
            os.fsync(handle.fileno())
        os.replace(temporary, path)
        directory_fd = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(directory_fd)
        finally:
            os.close(directory_fd)
    finally:
        temporary.unlink(missing_ok=True)


def parse_environment_json(payload: str) -> dict[str, str]:
    try:
        entries = json.loads(payload)
    except json.JSONDecodeError as exc:
        raise RelayPolicyError(f"invalid container environment JSON: {exc}") from exc
    if not isinstance(entries, list) or not all(isinstance(item, str) for item in entries):
        raise RelayPolicyError("container environment JSON must be a list of strings")
    selected = [entry for entry in entries if entry.split("=", 1)[0] in RELAY_KEYS]
    _, values = parse_env_lines("\n".join(selected))
    return values


def parse_args(argv: Iterable[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--policy", required=True, type=Path)
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument("--env-file", type=Path)
    source.add_argument("--environment-json", help="container environment JSON file, or - for stdin")
    parser.add_argument("--apply", action="store_true", help="atomically reconcile the env file")
    return parser.parse_args(list(argv))


def main(argv: Iterable[str] = sys.argv[1:]) -> int:
    args = parse_args(argv)
    try:
        relays = load_policy(args.policy)
        if args.environment_json is not None:
            if args.apply:
                raise RelayPolicyError("--apply is only valid with --env-file")
            payload = sys.stdin.read() if args.environment_json == "-" else Path(args.environment_json).read_text(encoding="utf-8")
            check_values(parse_environment_json(payload), relays)
        else:
            assert args.env_file is not None
            original = args.env_file.read_text(encoding="utf-8")
            if args.apply:
                rendered = render_env(original, relays)
                if rendered != original:
                    atomic_write(args.env_file, rendered)
                original = rendered
            _, values = parse_env_lines(original)
            check_values(values, relays)
    except (OSError, RelayPolicyError) as exc:
        print(f"openclaw_soulfactory_relay_policy: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
