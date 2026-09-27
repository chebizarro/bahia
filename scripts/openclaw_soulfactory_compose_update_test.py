#!/usr/bin/env python3

from __future__ import annotations

import unittest

import openclaw_soulfactory_compose_update as update

IMAGE = "sha256:" + "a" * 64


class ComposeUpdateTest(unittest.TestCase):
    def test_adds_group_and_persists_digest(self) -> None:
        source = """services:
  openclaw-soulfactory-sidecar:
    container_name: openclaw-soulfactory-sidecar
    image: local/sidecar:mutable
    restart: unless-stopped
networks: {}
"""
        rendered = update.render_compose(source, IMAGE, "988", "1002", "1002")
        self.assertIn(f"    image: {IMAGE}\n", rendered)
        self.assertIn('    user: "1002:1002"\n', rendered)
        self.assertIn('    group_add:\n      - "988"\n', rendered)
        update.check_compose(rendered, IMAGE, "988", "1002", "1002")

    def test_replaces_existing_group(self) -> None:
        source = f"""services:
  openclaw-soulfactory-sidecar:
    image: {IMAGE}
    group_add:
      - "101"
    restart: unless-stopped
"""
        rendered = update.render_compose(source, IMAGE, "988", "1002", "1002")
        self.assertNotIn('"101"', rendered)
        self.assertEqual(rendered.count("group_add:"), 1)

    def test_rejects_mutable_image(self) -> None:
        with self.assertRaises(update.ComposeUpdateError):
            update.render_compose("services: {}\n", "local/sidecar:latest", "988", "1002", "1002")


if __name__ == "__main__":
    unittest.main()
