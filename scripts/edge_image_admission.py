#!/usr/bin/env python3
"""Fail-closed admission for Bahia edge images and rollback Compose files."""

from __future__ import annotations

import argparse
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any, Iterable

SHA_PATTERN = re.compile(r"^[0-9a-f]{40}$")
IMAGE_ID_PATTERN = re.compile(r"^sha256:[0-9a-f]{64}$")
SERVICE_PATTERN = re.compile(r"^(?P<indent>\s*)(?P<name>[A-Za-z0-9_.-]+):(?:\s*(?:#.*)?)$")
IMAGE_PATTERN = re.compile(r"^(?P<indent>\s*)image:\s*(?P<image>\S+)\s*$")
DEFAULT_POLICY = Path(__file__).resolve().parents[1] / "deploy" / "edge" / "image-admission-policy.json"


class AdmissionError(ValueError):
    """Raised when an image or rollback candidate is unsafe."""


def command(argv: list[str], *, cwd: Path | None = None) -> str:
    result = subprocess.run(argv, cwd=cwd, text=True, capture_output=True, check=False)
    if result.returncode != 0:
        detail = result.stderr.strip() or result.stdout.strip() or f"exit {result.returncode}"
        raise AdmissionError(f"command failed: {argv[0]}: {detail}")
    return result.stdout.strip()


def load_policy(path: Path) -> dict[str, Any]:
    try:
        policy = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise AdmissionError(f"cannot load admission policy {path}: {exc}") from exc
    if policy.get("schema") != "cascadia.bahia.edge-image-admission.v1":
        raise AdmissionError("unsupported or missing image-admission policy schema")
    guard = policy.get("guard")
    ancestors = policy.get("required_ancestors")
    legacy = policy.get("approved_legacy_images")
    if not isinstance(guard, str) or not guard:
        raise AdmissionError("policy guard must be a non-empty string")
    if not isinstance(ancestors, list) or not ancestors:
        raise AdmissionError("policy required_ancestors must be non-empty")
    if not all(isinstance(item, str) and SHA_PATTERN.fullmatch(item) for item in ancestors):
        raise AdmissionError("policy contains an invalid required ancestor")
    if not isinstance(legacy, dict):
        raise AdmissionError("policy approved_legacy_images must be an object")
    for image_id, revision in legacy.items():
        if not IMAGE_ID_PATTERN.fullmatch(image_id) or not SHA_PATTERN.fullmatch(str(revision)):
            raise AdmissionError("policy contains an invalid legacy image mapping")
    return policy


def verify_revision(repo: Path, revision: str, policy: dict[str, Any]) -> None:
    if not SHA_PATTERN.fullmatch(revision):
        raise AdmissionError("revision must be a full 40-character lowercase Git commit")
    resolved = command(["git", "rev-parse", "--verify", f"{revision}^{{commit}}"], cwd=repo)
    if resolved != revision:
        raise AdmissionError(f"revision resolved to unexpected commit {resolved}")
    for floor in policy["required_ancestors"]:
        result = subprocess.run(
            ["git", "merge-base", "--is-ancestor", floor, revision],
            cwd=repo,
            text=True,
            capture_output=True,
            check=False,
        )
        if result.returncode == 1:
            raise AdmissionError(f"revision {revision} predates required flood-fix commit {floor}")
        if result.returncode != 0:
            raise AdmissionError(f"cannot prove required ancestor {floor} for {revision}")


def inspect_image(image: str) -> dict[str, Any]:
    payload = command(["docker", "image", "inspect", image])
    try:
        records = json.loads(payload)
    except json.JSONDecodeError as exc:
        raise AdmissionError(f"docker returned invalid image metadata for {image}") from exc
    if not isinstance(records, list) or len(records) != 1 or not isinstance(records[0], dict):
        raise AdmissionError(f"docker returned ambiguous image metadata for {image}")
    return records[0]


def verify_image(
    repo: Path,
    image: str,
    policy: dict[str, Any],
    expected_revision: str | None = None,
) -> tuple[str, str]:
    record = inspect_image(image)
    image_id = str(record.get("Id", ""))
    if not IMAGE_ID_PATTERN.fullmatch(image_id):
        raise AdmissionError(f"image {image} has no immutable sha256 ID")

    legacy_revision = policy["approved_legacy_images"].get(image_id)
    if legacy_revision is not None:
        verify_revision(repo, legacy_revision, policy)
        if expected_revision is not None and legacy_revision != expected_revision:
            raise AdmissionError(
                f"image {image_id} revision {legacy_revision} does not equal expected {expected_revision}"
            )
        return image_id, legacy_revision

    labels = ((record.get("Config") or {}).get("Labels") or {})
    revision = str(labels.get("org.opencontainers.image.revision", ""))
    guard = str(labels.get("io.cascadia.bahia.relay-flood-guard", ""))
    if guard != policy["guard"]:
        raise AdmissionError(
            f"image {image_id} has flood guard {guard!r}; required {policy['guard']!r}"
        )
    verify_revision(repo, revision, policy)
    if expected_revision is not None and revision != expected_revision:
        raise AdmissionError(
            f"image {image_id} revision {revision} does not equal expected {expected_revision}"
        )
    return image_id, revision


def replace_service_image(text: str, service: str, image: str) -> str:
    lines = text.splitlines()
    terminal_newline = text.endswith("\n")
    in_services = False
    services_indent: int | None = None
    service_indent: int | None = None
    current_service: str | None = None
    replacements = 0
    output: list[str] = []
    for line in lines:
        stripped = line.strip()
        indent = len(line) - len(line.lstrip(" "))
        if not in_services and stripped == "services:":
            in_services = True
            services_indent = indent
            output.append(line)
            continue
        if in_services:
            assert services_indent is not None
            if stripped and not stripped.startswith("#") and indent <= services_indent:
                in_services = False
                current_service = None
            else:
                match = SERVICE_PATTERN.match(line)
                if match and int(len(match.group("indent"))) > services_indent:
                    if service_indent is None:
                        service_indent = len(match.group("indent"))
                    if len(match.group("indent")) == service_indent:
                        current_service = match.group("name")
        replacement = line
        image_match = IMAGE_PATTERN.match(line)
        if in_services and current_service == service and image_match:
            replacement = f"{image_match.group('indent')}image: {image}"
            replacements += 1
        output.append(replacement)
    if replacements != 1:
        raise AdmissionError(f"expected exactly one image for service {service}; found {replacements}")
    rendered = "\n".join(output)
    return rendered + ("\n" if terminal_newline else "")


def sanitize_rollback(
    repo: Path,
    source: Path,
    output: Path,
    safe_bahia_image: str,
    policy: dict[str, Any],
) -> None:
    safe_id, _ = verify_image(repo, safe_bahia_image, policy)
    original = source.read_text(encoding="utf-8")
    rendered = replace_service_image(original, "bahia", safe_id)
    if output.resolve() == source.resolve():
        raise AdmissionError("rollback output must not overwrite the raw pre-deploy snapshot")
    output.write_text(rendered, encoding="utf-8")


def parse_args(argv: Iterable[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--policy", type=Path, default=DEFAULT_POLICY)
    sub = parser.add_subparsers(dest="action", required=True)
    revision = sub.add_parser("verify-revision")
    revision.add_argument("--repo", required=True, type=Path)
    revision.add_argument("--revision", required=True)
    image = sub.add_parser("verify-image")
    image.add_argument("--repo", required=True, type=Path)
    image.add_argument("--image", required=True)
    image.add_argument("--expected-revision")
    rollback = sub.add_parser("sanitize-rollback")
    rollback.add_argument("--repo", required=True, type=Path)
    rollback.add_argument("--source", required=True, type=Path)
    rollback.add_argument("--output", required=True, type=Path)
    rollback.add_argument("--safe-bahia-image", required=True)
    return parser.parse_args(list(argv))


def main(argv: Iterable[str] = sys.argv[1:]) -> int:
    args = parse_args(argv)
    try:
        policy = load_policy(args.policy)
        if args.action == "verify-revision":
            verify_revision(args.repo, args.revision, policy)
        elif args.action == "verify-image":
            image_id, revision = verify_image(
                args.repo, args.image, policy, args.expected_revision
            )
            print(json.dumps({"image_id": image_id, "revision": revision}, sort_keys=True))
        elif args.action == "sanitize-rollback":
            sanitize_rollback(args.repo, args.source, args.output, args.safe_bahia_image, policy)
        else:  # pragma: no cover
            raise AdmissionError(f"unsupported action {args.action}")
    except (AdmissionError, OSError) as exc:
        print(f"edge_image_admission: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
