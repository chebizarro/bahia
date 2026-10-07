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

TARGET_BACKEND_SERVICES = {"bahia"}
TARGET_WEB_SERVICE = "web"
TARGET_SERVICES = TARGET_BACKEND_SERVICES | {TARGET_WEB_SERVICE}
BACKEND_IMAGE = "local/bahia-controlplane-bahia"
WEB_IMAGE = "local/bahia-controlplane-web"
RELEASE_ROOT = "/srv/data/bahia-controlplane/releases"
TAG_PATTERN = re.compile(r"^github-[0-9a-f]{7}$")
DIGEST_IMAGE_PATTERN = re.compile(r"^(?:[a-z0-9./_-]+@)?sha256:[0-9a-f]{64}$")
SERVICE_HEADER_PATTERN = re.compile(r"^(?P<indent>\s*)(?P<name>[A-Za-z0-9_.-]+):(?:\s*(?:#.*)?)$")
ENVIRONMENT_HEADER_PATTERN = re.compile(r"^\s*environment:(?P<inline>.*)$")
MAPPING_ENTRY_PATTERN = re.compile(r"^\s*(?P<key>[A-Za-z_][A-Za-z0-9_]*)\s*:")
LIST_ENTRY_PATTERN = re.compile(r"""^\s*-\s*["']?(?P<key>[A-Za-z_][A-Za-z0-9_]*)""")

# Runtime trust roots the web container reads at start-up
# (web/docker-entrypoint.d/40-bahia-bootstrap-env.sh). Mirrors the repo
# docker-compose.yml web.environment block; the host compose file lives outside
# the repo, so the deploy injects any entry the host is missing.
WEB_ENVIRONMENT_SEED: tuple[tuple[str, str], ...] = (
    (
        "PUBLIC_BAHIA_BOOTSTRAP_RELAYS",
        "${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:?PUBLIC_BAHIA_BOOTSTRAP_RELAYS must be set}",
    ),
    (
        "PUBLIC_BAHIA_SERVICE_PUBKEYS",
        "${PUBLIC_BAHIA_SERVICE_PUBKEYS:?PUBLIC_BAHIA_SERVICE_PUBKEYS must identify the trusted Bahia signer}",
    ),
    ("PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS", "${PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS:-}"),
)
DEFAULT_INDENT_STEP = 2


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


def annotate_services(lines: list[str]) -> list[ServiceScope | None]:
    """Return, per line, the service block that contains it (None outside `services:`).

    Blank and comment lines inherit the surrounding service so a block spans up to
    the next service header or the next top-level key.
    """
    scopes: list[ServiceScope | None] = []
    in_services = False
    services_indent: int | None = None
    service_indent: int | None = None
    current_service: ServiceScope | None = None

    for line in lines:
        stripped = line.strip()
        indent = line_indent(line)

        if not in_services and stripped == "services:":
            in_services = True
            services_indent = indent
            service_indent = None
            current_service = None
            scopes.append(None)
            continue

        if in_services:
            assert services_indent is not None
            if stripped and not stripped.startswith("#") and indent <= services_indent:
                in_services = False
                current_service = None
                service_indent = None
            elif service_indent is None and stripped and not stripped.startswith("#"):
                candidate = service_from_line(line, None)
                if candidate is not None and candidate.indent > services_indent:
                    service_indent = candidate.indent
                    current_service = candidate
            elif service_indent is not None:
                candidate = service_from_line(line, service_indent)
                if candidate is not None:
                    current_service = candidate

        scopes.append(current_service)

    return scopes


def is_content(line: str) -> bool:
    stripped = line.strip()
    return bool(stripped) and not stripped.startswith("#")


def ensure_web_environment(lines: list[str]) -> list[str]:
    """Return `lines` with every WEB_ENVIRONMENT_SEED key present under web.environment.

    Existing keys keep their values; missing keys are appended in the style the
    block already uses (`KEY: value` mapping or `- KEY=value` list). When the web
    service has no `environment:` block a mapping is created. All other lines are
    preserved byte-for-byte, so the result is stable under repeated application.
    """
    scopes = annotate_services(lines)
    block = [index for index, scope in enumerate(scopes) if scope is not None and scope.name == TARGET_WEB_SERVICE]
    if not block:
        raise ComposeUpdateError("missing expected services: " + TARGET_WEB_SERVICE)

    header_index = block[0]
    block_end = block[-1] + 1
    web_scope = scopes[header_index]
    assert web_scope is not None
    service_indent = web_scope.indent
    body_indices = [
        index
        for index in range(header_index + 1, block_end)
        if is_content(lines[index]) and line_indent(lines[index]) > service_indent
    ]
    if body_indices:
        child_indent = line_indent(lines[body_indices[0]])
    else:
        child_indent = service_indent + DEFAULT_INDENT_STEP
    step = child_indent - service_indent

    env_index: int | None = None
    for index in body_indices:
        if line_indent(lines[index]) == child_indent and ENVIRONMENT_HEADER_PATTERN.match(lines[index]):
            env_index = index
            break

    updated = list(lines)

    if env_index is None:
        last_body = max(
            (
                index
                for index in range(header_index + 1, block_end)
                if lines[index].strip() and line_indent(lines[index]) > service_indent
            ),
            default=header_index,
        )
        new_lines = [" " * child_indent + "environment:"]
        new_lines.extend(f"{' ' * (child_indent + step)}{key}: {value}" for key, value in WEB_ENVIRONMENT_SEED)
        updated[last_body + 1 : last_body + 1] = new_lines
        return updated

    header_match = ENVIRONMENT_HEADER_PATTERN.match(lines[env_index])
    assert header_match is not None
    inline = header_match.group("inline").strip()
    inline_style: str | None = None
    if inline and not inline.startswith("#"):
        if inline == "{}":
            inline_style = "mapping"
        elif inline == "[]":
            inline_style = "list"
        else:
            raise ComposeUpdateError(
                "web.environment uses inline flow syntax; convert it to a block mapping or list before deploying"
            )

    entry_indices: list[int] = []
    last_entry = env_index
    for index in range(env_index + 1, block_end):
        line = lines[index]
        if is_content(line) and line_indent(line) <= child_indent:
            break
        if line.strip() and line_indent(line) > child_indent:
            last_entry = index
            if is_content(line):
                entry_indices.append(index)

    if entry_indices:
        first_entry = lines[entry_indices[0]]
        style = "list" if first_entry.lstrip().startswith("-") else "mapping"
        entry_indent = line_indent(first_entry)
    else:
        style = inline_style or "mapping"
        entry_indent = child_indent + step

    pattern = LIST_ENTRY_PATTERN if style == "list" else MAPPING_ENTRY_PATTERN
    present: set[str] = set()
    for index in entry_indices:
        if line_indent(lines[index]) != entry_indent:
            continue
        match = pattern.match(lines[index])
        if match:
            present.add(match.group("key"))

    missing = [(key, value) for key, value in WEB_ENVIRONMENT_SEED if key not in present]
    if not missing:
        return updated

    if inline_style is not None:
        updated[env_index] = " " * child_indent + "environment:"
    if style == "list":
        new_lines = [f"{' ' * entry_indent}- {key}={value}" for key, value in missing]
    else:
        new_lines = [f"{' ' * entry_indent}{key}: {value}" for key, value in missing]
    updated[last_entry + 1 : last_entry + 1] = new_lines
    return updated


def seed_web_environment_text(text: str) -> str:
    lines, had_terminal_newline = split_lines_preserving_terminal_newline(text)
    updated = "\n".join(ensure_web_environment(lines))
    if had_terminal_newline:
        updated += "\n"
    return updated


def update_compose_text(
    text: str, tag: str, release_dir: str, backend_image: str, web_image: str
) -> str:
    validate_tag(tag)
    validate_release_dir(release_dir, tag)
    validate_digest_image(backend_image, "backend image")
    validate_digest_image(web_image, "web image")

    lines, had_terminal_newline = split_lines_preserving_terminal_newline(text)
    updated_lines: list[str] = []
    scopes = annotate_services(lines)
    seen_services = {scope.name for scope in scopes if scope is not None}
    image_counts = {service: 0 for service in TARGET_SERVICES}
    docs_mount_count = 0

    for line, current_service in zip(lines, scopes):
        stripped = line.strip()
        indent = line_indent(line)

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

    updated_lines = ensure_web_environment(updated_lines)

    updated = "\n".join(updated_lines)
    if had_terminal_newline:
        updated += "\n"
    return updated


def write_if_changed(path: Path, original: str, updated: str) -> None:
    if updated != original:
        path.write_text(updated, encoding="utf-8")


def update_compose_file(path: Path, tag: str, release_dir: str, backend_image: str, web_image: str) -> None:
    original = path.read_text(encoding="utf-8")
    updated = update_compose_text(original, tag, release_dir, backend_image, web_image)
    write_if_changed(path, original, updated)


def seed_web_environment_file(path: Path) -> None:
    original = path.read_text(encoding="utf-8")
    write_if_changed(path, original, seed_web_environment_text(original))


def parse_args(argv: Iterable[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(
        description=(
            "Update Bahia edge compose images and release docs mount, and make sure "
            "web.environment forwards the runtime bootstrap seed."
        )
    )
    parser.add_argument("--compose-file", required=True, type=Path)
    parser.add_argument(
        "--seed-web-env-only",
        action="store_true",
        help="only add missing web.environment seed entries; image/tag arguments are not required",
    )
    parser.add_argument("--tag")
    parser.add_argument("--release-dir")
    parser.add_argument("--backend-image")
    parser.add_argument("--web-image")
    args = parser.parse_args(list(argv))
    if not args.seed_web_env_only:
        missing = [name for name in ("tag", "release_dir", "backend_image", "web_image") if getattr(args, name) is None]
        if missing:
            parser.error("the following arguments are required: " + ", ".join("--" + name.replace("_", "-") for name in missing))
    return args


def main(argv: Iterable[str] = sys.argv[1:]) -> int:
    args = parse_args(argv)
    try:
        if args.seed_web_env_only:
            seed_web_environment_file(args.compose_file)
        else:
            update_compose_file(args.compose_file, args.tag, args.release_dir, args.backend_image, args.web_image)
    except (OSError, ComposeUpdateError) as exc:
        print(f"deploy_edge_compose_update: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
