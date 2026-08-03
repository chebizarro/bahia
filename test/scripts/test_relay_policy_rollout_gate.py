import importlib.util
from pathlib import Path
import sys
import unittest

SCRIPT_PATH = Path(__file__).resolve().parents[2] / "scripts" / "relay_policy_rollout_gate.py"
SPEC = importlib.util.spec_from_file_location("relay_policy_rollout_gate", SCRIPT_PATH)
gate = importlib.util.module_from_spec(SPEC)
sys.modules["relay_policy_rollout_gate"] = gate
assert SPEC.loader is not None
SPEC.loader.exec_module(gate)


def policy(event: str = "a", digest: str = "b", created: str = "2026-08-03T06:00:00Z"):
    return {
        "event_id": event * 64,
        "hash": digest * 64,
        "author": "c" * 64,
        "event_created_at": created,
        "confirmation": "cached",
    }


class RelayPolicyRolloutGateTests(unittest.TestCase):
    def test_same_projection_survives_relay_outage_as_cached(self):
        gate.require_same_or_newer(policy(), policy())

    def test_newer_valid_projection_is_accepted(self):
        gate.require_same_or_newer(
            policy(),
            policy(event="d", digest="e", created="2026-08-03T06:01:00Z"),
        )

    def test_hash_mismatch_at_baseline_timestamp_is_rejected(self):
        with self.assertRaisesRegex(gate.RolloutGateError, "hash mismatch"):
            gate.require_same_or_newer(policy(), policy(digest="e"))

    def test_older_projection_is_rejected(self):
        with self.assertRaisesRegex(gate.RolloutGateError, "older event"):
            gate.require_same_or_newer(
                policy(created="2026-08-03T06:01:00Z"),
                policy(created="2026-08-03T06:00:00Z"),
            )

    def test_empty_by_absence_is_rejected(self):
        with self.assertRaisesRegex(gate.RolloutGateError, "did not pass"):
            gate.projection_from_readiness({"checks": []})


if __name__ == "__main__":
    unittest.main()
