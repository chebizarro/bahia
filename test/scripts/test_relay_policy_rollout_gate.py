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

    def test_absent_projection_is_not_a_readiness_failure(self):
        self.assertIsNone(gate.projection_from_readiness({"checks": []}))

    def test_capture_records_absent_baseline(self):
        from tempfile import TemporaryDirectory
        from unittest.mock import patch

        with TemporaryDirectory() as directory, patch.object(gate, "fetch_readiness", return_value={"checks": []}):
            output = Path(directory) / "baseline.json"
            gate.capture("http://ready", output)
            self.assertEqual(output.read_text(encoding="utf-8"), '{"present": false}\n')

    def test_verify_skips_policy_comparison_when_no_pre_rollout_head_existed(self):
        from tempfile import TemporaryDirectory
        from unittest.mock import patch

        with TemporaryDirectory() as directory, patch.object(gate, "fetch_readiness") as fetch:
            baseline = Path(directory) / "baseline.json"
            baseline.write_text('{"present": false}\n', encoding="utf-8")
            gate.verify("http://ready", baseline)
            fetch.assert_not_called()

    def test_verify_requires_post_rollout_head_when_baseline_was_present(self):
        from tempfile import TemporaryDirectory
        from unittest.mock import patch

        with TemporaryDirectory() as directory, patch.object(gate, "fetch_readiness", return_value={"checks": []}):
            baseline = Path(directory) / "baseline.json"
            baseline.write_text(
                __import__("json").dumps({"present": True, "projection": policy()}) + "\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(gate.RolloutGateError, "present before rollout"):
                gate.verify("http://ready", baseline)


if __name__ == "__main__":
    unittest.main()
