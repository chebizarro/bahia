#!/usr/bin/env python3

from __future__ import annotations

import json
import stat
import tempfile
import unittest
from pathlib import Path

import openclaw_soulfactory_relay_policy as policy


RELAYS = ("wss://relay.sharegap.net", "wss://bahia.sharegap.net/relay")


def env_text(value: str) -> str:
    return "HEADER=preserved\n" + "\n".join(f"{key}={value}" for key in policy.RELAY_KEYS) + "\n"


class RelayPolicyTest(unittest.TestCase):
    def test_render_replaces_only_relay_keys(self) -> None:
        rendered = policy.render_env(env_text("ws://retired.invalid:3337"), RELAYS)
        self.assertTrue(rendered.startswith("HEADER=preserved\n"))
        _, values = policy.parse_env_lines(rendered)
        policy.check_values(values, RELAYS)

    def test_duplicate_key_is_rejected(self) -> None:
        with self.assertRaises(policy.RelayPolicyError):
            policy.parse_env_lines(env_text("wss://relay.sharegap.net") + "SOULFACTORY_RELAYS=wss://duplicate\n")

    def test_container_environment_is_checked(self) -> None:
        entries = ["UNRELATED=secret"] + [f"{key}={','.join(RELAYS)}" for key in policy.RELAY_KEYS]
        policy.check_values(policy.parse_environment_json(json.dumps(entries)), RELAYS)

    def test_atomic_write_preserves_mode(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "compose.env"
            path.write_text(env_text("ws://retired.invalid:3337"), encoding="utf-8")
            path.chmod(0o600)
            policy.atomic_write(path, policy.render_env(path.read_text(encoding="utf-8"), RELAYS))
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)
            _, values = policy.parse_env_lines(path.read_text(encoding="utf-8"))
            policy.check_values(values, RELAYS)


if __name__ == "__main__":
    unittest.main()
