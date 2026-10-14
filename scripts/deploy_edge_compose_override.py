#!/usr/bin/env python3
"""Generate an edge Compose override without modifying the host base file."""

from __future__ import annotations

import argparse
import json
import os
import re
import sys
import tempfile
from pathlib import Path
from typing import Any, Iterable

BACKEND_SERVICE = "bahia"
WEB_SERVICE = "web"
RELEASE_ROOT = "/srv/data/bahia-controlplane/releases"
TAG_PATTERN = re.compile(r"github-[0-9a-f]{7}\Z")
DIGEST_PATTERN = re.compile(r"(?:[a-z0-9./_-]+@)?sha256:[0-9a-f]{64}\Z")
WEB_ENVIRONMENT_SEED = {
    "PUBLIC_BAHIA_BOOTSTRAP_RELAYS": "${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:?PUBLIC_BAHIA_BOOTSTRAP_RELAYS must be set}",
    "PUBLIC_BAHIA_SERVICE_PUBKEYS": "${PUBLIC_BAHIA_SERVICE_PUBKEYS:?PUBLIC_BAHIA_SERVICE_PUBKEYS must identify the trusted Bahia signer}",
    "PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS": "${PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS:-}",
}


class ComposeOverrideError(ValueError):
    """The rendered base cannot be safely overridden."""


def service(config: dict[str, Any], name: str) -> dict[str, Any]:
    services = config.get("services")
    found = services.get(name) if isinstance(services, dict) else None
    if not isinstance(found, dict):
        raise ComposeOverrideError(f"missing expected services: {name}")
    return found


def environment_keys(value: Any) -> set[str]:
    if value is None:
        return set()
    if isinstance(value, dict):
        return set(value)
    if isinstance(value, list):
        if not all(isinstance(item, str) for item in value):
            raise ComposeOverrideError("web.environment contains a non-string list entry")
        return {item.split("=", 1)[0] for item in value}
    raise ComposeOverrideError("web.environment must be a mapping or list")


def docs_mount(backend: dict[str, Any]) -> None:
    volumes = backend.get("volumes")
    if not isinstance(volumes, list):
        raise ComposeOverrideError("missing release docs mount under " + RELEASE_ROOT)
    matches = []
    for volume in volumes:
        if isinstance(volume, dict):
            source = volume.get("source")
            target = volume.get("target")
            read_only = volume.get("read_only")
        elif isinstance(volume, str):
            parts = volume.rsplit(":", 2)
            if len(parts) != 3:
                continue
            source, target, mode = parts
            read_only = mode == "ro"
        else:
            continue
        if target == "/docs":
            matches.append((source, read_only))
    if len(matches) != 1:
        raise ComposeOverrideError("expected exactly one release docs mount at bahia:/docs")
    source, read_only = matches[0]
    if not isinstance(source, str) or not source.startswith(RELEASE_ROOT + "/") or not source.endswith("/docs") or read_only is not True:
        raise ComposeOverrideError("missing read-only release docs mount under " + RELEASE_ROOT)


def quote(value: str) -> str:
    return json.dumps(value, ensure_ascii=True)


def generate_override(
    config: dict[str, Any], *, tag: str | None = None, release_dir: str | None = None,
    backend_image: str | None = None, web_image: str | None = None, seed_only: bool = False,
    rollback_backend_image: str | None = None,
) -> str:
    web = service(config, WEB_SERVICE)
    missing_env = {key: value for key, value in WEB_ENVIRONMENT_SEED.items()
                   if key not in environment_keys(web.get("environment"))}
    lines = ["services:"]
    if rollback_backend_image is not None:
        if not seed_only or not DIGEST_PATTERN.fullmatch(rollback_backend_image):
            raise ComposeOverrideError("rollback backend image requires --seed-only and an immutable sha256 image")
        backend = service(config, BACKEND_SERVICE)
        if not isinstance(web.get("image"), str) or not web["image"]:
            raise ComposeOverrideError("missing image for service: web")
        if not isinstance(backend.get("image"), str) or not backend["image"]:
            raise ComposeOverrideError("missing image for service: bahia")
        docs_mount(backend)
        lines.extend(("  bahia:", f"    image: {quote(rollback_backend_image)}"))
    if not seed_only:
        backend = service(config, BACKEND_SERVICE)
        if not tag or not TAG_PATTERN.fullmatch(tag):
            raise ComposeOverrideError("tag must match github-<7 lowercase hex characters>")
        expected_dir = f"{RELEASE_ROOT}/{tag}"
        if release_dir != expected_dir:
            raise ComposeOverrideError(f"release directory must be {expected_dir}")
        for name, image in (("backend", backend_image), ("web", web_image)):
            if not isinstance(image, str) or not DIGEST_PATTERN.fullmatch(image):
                raise ComposeOverrideError(f"{name} image must be an immutable sha256 image ID or repo@sha256 manifest reference")
        if not isinstance(backend.get("image"), str) or not backend["image"]:
            raise ComposeOverrideError("missing image for service: bahia")
        if not isinstance(web.get("image"), str) or not web["image"]:
            raise ComposeOverrideError("missing image for service: web")
        docs_mount(backend)
        lines.extend((
            "  bahia:",
            f"    image: {quote(backend_image)}",
            "    volumes:",
            "      - type: bind",
            f"        source: {quote(release_dir + '/docs')}",
            "        target: /docs",
            "        read_only: true",
            "  web:",
            f"    image: {quote(web_image)}",
        ))
    elif missing_env:
        lines.append("  web:")
    if missing_env:
        lines.append("    environment:")
        lines.extend(f"      {key}: {quote(value)}" for key, value in missing_env.items())
    if seed_only and not missing_env and rollback_backend_image is None:
        return "services: {}\n"
    return "\n".join(lines) + "\n"


def write_override(path: Path, content: str) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary: str | None = None
    try:
        with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", newline="\n",
                                         prefix=path.name + ".", dir=path.parent,
                                         delete=False) as output:
            temporary = output.name
            output.write(content)
        os.replace(temporary, path)
    finally:
        if temporary is not None and os.path.exists(temporary):
            os.unlink(temporary)


def parse_args(argv: Iterable[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-file", required=True, type=Path)
    parser.add_argument("--override-file", required=True, type=Path)
    parser.add_argument("--seed-only", action="store_true")
    parser.add_argument("--tag")
    parser.add_argument("--release-dir")
    parser.add_argument("--backend-image")
    parser.add_argument("--web-image")
    parser.add_argument("--rollback-backend-image")
    args = parser.parse_args(list(argv))
    if not args.seed_only:
        missing = [name for name in ("tag", "release_dir", "backend_image", "web_image") if getattr(args, name) is None]
        if missing:
            parser.error("the following arguments are required: " + ", ".join("--" + name.replace("_", "-") for name in missing))
    return args


def main(argv: Iterable[str] = sys.argv[1:]) -> int:
    args = parse_args(argv)
    try:
        if args.base_file.resolve() == args.override_file.resolve():
            raise ComposeOverrideError("override must not overwrite the base Compose file")
        config = json.load(sys.stdin)
        if not isinstance(config, dict):
            raise ComposeOverrideError("rendered Compose config must be an object")
        content = generate_override(config, tag=args.tag, release_dir=args.release_dir,
                                    backend_image=args.backend_image, web_image=args.web_image,
                                    seed_only=args.seed_only,
                                    rollback_backend_image=args.rollback_backend_image)
        write_override(args.override_file, content)
    except (OSError, ValueError) as exc:
        print(f"deploy_edge_compose_override: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
