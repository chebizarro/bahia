#!/usr/bin/env python3
"""Safely update the live edge compose file for a GitHub push deploy."""

from __future__ import annotations

import argparse
import os
import re
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable

TARGET_BACKEND_SERVICES = {"bahia", "relay"}
TARGET_WEB_SERVICE = "web"
TARGET_SERVICES = TARGET_BACKEND_SERVICES | {TARGET_WEB_SERVICE}
BACKEND_IMAGE = "local/bahia-controlplane-bahia"
WEB_IMAGE = "local/bahia-controlplane-web"
RELEASE_ROOT = "/srv/data/bahia-controlplane/releases"
TAG_PATTERN = re.compile(r"^github-[0-9a-f]{7}$")
DIGEST_IMAGE_PATTERN = re.compile(r"^(?:[a-z0-9./_-]+@)?sha256:[0-9a-f]{64}$")
SERVICE_HEADER_PATTERN = re.compile(r"^(?P<indent>\s*)(?P<name>[A-Za-z0-9_.-]+):(?:\s*(?:#.*)?)$")


class ComposeUpdateError(ValueError):
    """Raised when the compose file cannot be updated safely."""


@dataclass(frozen=True)
class ServiceScope:
    name: str
    indent: int


def validate_tag(tag: str) -> None:
    if not TAG_PATTERN.fullmatch(tag):
        raise ComposeUpdateError(
            "tag must match github-<7 lowercase hex characters>; got " + repr(tag)
        )


def validate_digest_image(image: str, name: str) -> None:
    if not DIGEST_IMAGE_PATTERN.fullmatch(image):
        raise ComposeUpdateError(f"{name} must be an immutable sha256 image ID or repo@sha256 manifest reference")


def validate_release_dir(release_dir: str, tag: str) -> None:
    if "\x00" in release_dir or "\n" in release_dir or "\r" in release_dir:
        raise ComposeUpdateError("release directory contains an unsafe control character")
    normalized = os.path.normpath(release_dir)
    expected = os.path.join(RELEASE_ROOT, tag)
    if normalized != expected:
        raise ComposeUpdateError(
            f"release directory must be {expected}; got {release_dir!r}"
        )


def line_indent(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def split_lines_preserving_terminal_newline(text: str) -> tuple[list[str], bool]:
    return text.splitlines(), text.endswith("\n")


def service_from_line(line: str, required_indent: int | None) -> ServiceScope | None:
    match = SERVICE_HEADER_PATTERN.match(line)
    if not match:
        return None
    indent = len(match.group("indent"))
    if required_indent is not None and indent != required_indent:
        return None
    return ServiceScope(name=match.group("name"), indent=indent)


def update_compose_text(
    text: str, tag: str, release_dir: str, backend_image: str, web_image: str
) -> str:
    validate_tag(tag)
    validate_release_dir(release_dir, tag)
    validate_digest_image(backend_image, "backend image")
    validate_digest_image(web_image, "web image")

    lines, had_terminal_newline = split_lines_preserving_terminal_newline(text)
    updated_lines: list[str] = []
    in_services = False
    services_indent: int | None = None
    service_indent: int | None = None
    current_service: ServiceScope | None = None
    seen_services: set[str] = set()
    image_counts = {service: 0 for service in TARGET_SERVICES}
    docs_mount_count = 0

    for line in lines:
        stripped = line.strip()
        indent = line_indent(line)

        if not in_services and stripped == "services:":
            in_services = True
            services_indent = indent
            service_indent = None
            current_service = None
            updated_lines.append(line)
            continue

        if in_services:
            assert services_indent is not None
            if stripped and not stripped.startswith("#") and indent <= services_indent:
                in_services = False
                current_service = None
                service_indent = None
            else:
                if service_indent is None and stripped and not stripped.startswith("#"):
                    candidate = service_from_line(line, None)
                    if candidate is not None and candidate.indent > services_indent:
                        service_indent = candidate.indent
                        current_service = candidate
                        seen_services.add(candidate.name)
                elif service_indent is not None:
                    candidate = service_from_line(line, service_indent)
                    if candidate is not None:
                        current_service = candidate
                        seen_services.add(candidate.name)

        replacement = line
        if current_service is not None and current_service.name in TARGET_SERVICES:
            if stripped.startswith("image:"):
                image_counts[current_service.name] += 1
                image_ref = backend_image if current_service.name in TARGET_BACKEND_SERVICES else web_image
                replacement = f"{line[:indent]}image: {image_ref}"

        if RELEASE_ROOT in line and ":/docs:ro" in line:
            docs_mount_count += 1
            replacement = f"{line[:indent]}- {release_dir}/docs:/docs:ro"

        updated_lines.append(replacement)

    missing_services = sorted(TARGET_SERVICES - seen_services)
    if missing_services:
        raise ComposeUpdateError("missing expected services: " + ", ".join(missing_services))

    missing_images = sorted(service for service, count in image_counts.items() if count == 0)
    if missing_images:
        raise ComposeUpdateError("missing image line for services: " + ", ".join(missing_images))

    duplicate_images = sorted(service for service, count in image_counts.items() if count > 1)
    if duplicate_images:
        raise ComposeUpdateError("duplicate image lines for services: " + ", ".join(duplicate_images))

    if docs_mount_count == 0:
        raise ComposeUpdateError("missing release docs mount under /srv/data/bahia-controlplane/releases")
    if docs_mount_count > 1:
        raise ComposeUpdateError("multiple release docs mounts found; refusing ambiguous update")

    updated = "\n".join(updated_lines)
    if had_terminal_newline:
        updated += "\n"
    return updated


def update_compose_file(path: Path, tag: str, release_dir: str, backend_image: str, web_image: str) -> None:
    original = path.read_text(encoding="utf-8")
    updated = update_compose_text(original, tag, release_dir, backend_image, web_image)
    path.write_text(updated, encoding="utf-8")


def parse_args(argv: Iterable[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description="Update Bahia edge compose images and release docs mount."
    )
    parser.add_argument("--compose-file", required=True, type=Path)
    parser.add_argument("--tag", required=True)
    parser.add_argument("--release-dir", required=True)
    parser.add_argument("--backend-image", required=True)
    parser.add_argument("--web-image", required=True)
    return parser.parse_args(list(argv))


def main(argv: Iterable[str] = sys.argv[1:]) -> int:
    args = parse_args(argv)
    try:
        update_compose_file(args.compose_file, args.tag, args.release_dir, args.backend_image, args.web_image)
    except (OSError, ComposeUpdateError) as exc:
        print(f"deploy_edge_compose_update: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
