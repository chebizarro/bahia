#!/usr/bin/env python3
"""Persist an immutable sidecar image and Docker-socket group in Compose."""

from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path
from typing import Iterable

from openclaw_soulfactory_relay_policy import atomic_write

SERVICE = "openclaw-soulfactory-sidecar"
IMAGE_PATTERN = re.compile(r"^sha256:[0-9a-f]{64}$")
HEADER_PATTERN = re.compile(r"^(?P<indent>\s*)(?P<name>[A-Za-z0-9_.-]+):(?:\s*(?:#.*)?)$")


class ComposeUpdateError(ValueError):
    """Raised when the production Compose file cannot be updated safely."""


def validate_id(value: str, name: str) -> None:
    if not value.isdigit() or not 1 <= int(value) <= 2**31 - 1:
        raise ComposeUpdateError(f"{name} must be a positive integer")


def validate_inputs(image: str, socket_gid: str, runtime_uid: str, runtime_gid: str) -> None:
    if not IMAGE_PATTERN.fullmatch(image):
        raise ComposeUpdateError("image must be an immutable sha256 image ID")
    validate_id(socket_gid, "Docker socket gid")
    validate_id(runtime_uid, "runtime uid")
    validate_id(runtime_gid, "runtime gid")


def line_indent(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def service_bounds(lines: list[str]) -> tuple[int, int, int]:
    services_index = next((index for index, line in enumerate(lines) if line.strip() == "services:"), None)
    if services_index is None:
        raise ComposeUpdateError("missing services mapping")
    services_indent = line_indent(lines[services_index])
    candidates: list[tuple[int, int]] = []
    for index in range(services_index + 1, len(lines)):
        line = lines[index]
        stripped = line.strip()
        if stripped and not stripped.startswith("#") and line_indent(line) <= services_indent:
            break
        match = HEADER_PATTERN.match(line)
        if match and len(match.group("indent")) > services_indent:
            candidates.append((index, len(match.group("indent"))))
    service_matches = [(index, indent) for index, indent in candidates if HEADER_PATTERN.match(lines[index]).group("name") == SERVICE]
    if len(service_matches) != 1:
        raise ComposeUpdateError(f"expected exactly one {SERVICE} service; found {len(service_matches)}")
    start, service_indent = service_matches[0]
    end = len(lines)
    for index in range(start + 1, len(lines)):
        stripped = lines[index].strip()
        if stripped and not stripped.startswith("#") and line_indent(lines[index]) <= service_indent:
            end = index
            break
    return start, end, service_indent


def replace_or_insert_scalar(lines: list[str], start: int, end: int, child_indent: int, key: str, value: str, after: int) -> tuple[list[str], int]:
    indexes = [
        index for index in range(start + 1, end)
        if line_indent(lines[index]) == child_indent and lines[index].strip().startswith(f"{key}:")
    ]
    if len(indexes) > 1:
        raise ComposeUpdateError(f"multiple {key} entries found")
    rendered = " " * child_indent + f'{key}: "{value}"'
    if indexes:
        lines[indexes[0]] = rendered
        return lines, end
    lines[after + 1:after + 1] = [rendered]
    return lines, end + 1


def render_compose(text: str, image: str, socket_gid: str, runtime_uid: str, runtime_gid: str) -> str:
    validate_inputs(image, socket_gid, runtime_uid, runtime_gid)
    lines = text.splitlines()
    had_newline = text.endswith("\n")
    start, end, service_indent = service_bounds(lines)
    child_indent = service_indent + 2

    image_indexes = [
        index for index in range(start + 1, end)
        if line_indent(lines[index]) == child_indent and lines[index].strip().startswith("image:")
    ]
    if len(image_indexes) != 1:
        raise ComposeUpdateError(f"expected exactly one image line; found {len(image_indexes)}")
    image_index = image_indexes[0]
    lines[image_index] = " " * child_indent + f"image: {image}"
    lines, end = replace_or_insert_scalar(
        lines, start, end, child_indent, "user", f"{runtime_uid}:{runtime_gid}", image_index
    )

    group_indexes = [
        index for index in range(start + 1, end)
        if line_indent(lines[index]) == child_indent and lines[index].strip().startswith("group_add:")
    ]
    if len(group_indexes) > 1:
        raise ComposeUpdateError("multiple group_add blocks found")
    group_block = [" " * child_indent + "group_add:", " " * (child_indent + 2) + f'- "{socket_gid}"']
    if group_indexes:
        group_start = group_indexes[0]
        group_end = group_start + 1
        while group_end < end:
            stripped = lines[group_end].strip()
            if stripped and not stripped.startswith("#") and line_indent(lines[group_end]) <= child_indent:
                break
            group_end += 1
        lines[group_start:group_end] = group_block
    else:
        lines[image_index + 1:image_index + 1] = group_block

    rendered = "\n".join(lines) + ("\n" if had_newline else "")
    return rendered


def check_compose(text: str, image: str, socket_gid: str, runtime_uid: str, runtime_gid: str) -> None:
    if render_compose(text, image, socket_gid, runtime_uid, runtime_gid) != text:
        raise ComposeUpdateError("Compose deployment state does not match the requested image and socket gid")


def parse_args(argv: Iterable[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--compose-file", required=True, type=Path)
    parser.add_argument("--image", required=True)
    parser.add_argument("--docker-socket-gid", required=True)
    parser.add_argument("--runtime-uid", required=True)
    parser.add_argument("--runtime-gid", required=True)
    parser.add_argument("--apply", action="store_true")
    return parser.parse_args(list(argv))


def main(argv: Iterable[str] = sys.argv[1:]) -> int:
    args = parse_args(argv)
    try:
        original = args.compose_file.read_text(encoding="utf-8")
        if args.apply:
            rendered = render_compose(original, args.image, args.docker_socket_gid, args.runtime_uid, args.runtime_gid)
            if rendered != original:
                atomic_write(args.compose_file, rendered)
            original = rendered
        check_compose(original, args.image, args.docker_socket_gid, args.runtime_uid, args.runtime_gid)
    except (OSError, ComposeUpdateError) as exc:
        print(f"openclaw_soulfactory_compose_update: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
