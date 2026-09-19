import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

SCRIPT = Path(__file__).resolve().parents[2] / "scripts" / "edge_image_admission.py"
SPEC = importlib.util.spec_from_file_location("edge_image_admission", SCRIPT)
edge = importlib.util.module_from_spec(SPEC)
sys.modules["edge_image_admission"] = edge
assert SPEC.loader is not None
SPEC.loader.exec_module(edge)

FLOOR_A = "a" * 40
FLOOR_B = "b" * 40
REVISION = "c" * 40
IMAGE_ID = "sha256:" + "d" * 64
LEGACY_ID = "sha256:" + "e" * 64


def policy():
    return {
        "schema": "cascadia.bahia.edge-image-admission.v1",
        "guard": "2026-09-15-v1",
        "required_ancestors": [FLOOR_A, FLOOR_B],
        "approved_legacy_images": {LEGACY_ID: REVISION},
    }


class EdgeImageAdmissionTests(unittest.TestCase):
    @mock.patch.object(edge, "command")
    @mock.patch.object(edge.subprocess, "run")
    def test_revision_requires_both_floors(self, run, command):
        command.return_value = REVISION
        run.side_effect = [subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 0)]
        edge.verify_revision(Path("."), REVISION, policy())
        self.assertEqual(2, run.call_count)

    def test_rejects_short_dev_and_pre_floor_revisions(self):
        for revision in ("dev", "c" * 7, "C" * 40):
            with self.subTest(revision=revision), self.assertRaises(edge.AdmissionError):
                edge.verify_revision(Path("."), revision, policy())
        with mock.patch.object(edge, "command", return_value=REVISION), mock.patch.object(
            edge.subprocess,
            "run",
            side_effect=[subprocess.CompletedProcess([], 0), subprocess.CompletedProcess([], 1)],
        ), self.assertRaisesRegex(edge.AdmissionError, "predates required"):
            edge.verify_revision(Path("."), REVISION, policy())

    @mock.patch.object(edge, "verify_revision")
    @mock.patch.object(edge, "inspect_image")
    def test_modern_image_requires_guard_and_revision(self, inspect, verify_revision):
        inspect.return_value = {
            "Id": IMAGE_ID,
            "Config": {"Labels": {
                "org.opencontainers.image.revision": REVISION,
                "io.cascadia.bahia.relay-flood-guard": "2026-09-15-v1",
            }},
        }
        self.assertEqual((IMAGE_ID, REVISION), edge.verify_image(Path("."), IMAGE_ID, policy()))
        verify_revision.assert_called_once_with(Path("."), REVISION, policy())

    @mock.patch.object(edge, "verify_revision")
    @mock.patch.object(edge, "inspect_image")
    def test_image_revision_must_equal_selected_release(self, inspect, _verify_revision):
        inspect.return_value = {
            "Id": IMAGE_ID,
            "Config": {"Labels": {
                "org.opencontainers.image.revision": REVISION,
                "io.cascadia.bahia.relay-flood-guard": "2026-09-15-v1",
            }},
        }
        with self.assertRaisesRegex(edge.AdmissionError, "does not equal expected"):
            edge.verify_image(Path("."), IMAGE_ID, policy(), "f" * 40)

    @mock.patch.object(edge, "inspect_image")
    def test_rejects_missing_dev_or_wrong_guard(self, inspect):
        for labels in ({}, {"org.opencontainers.image.revision": "dev"}, {
            "org.opencontainers.image.revision": REVISION,
            "io.cascadia.bahia.relay-flood-guard": "wrong",
        }):
            inspect.return_value = {"Id": IMAGE_ID, "Config": {"Labels": labels}}
            with self.subTest(labels=labels), self.assertRaises(edge.AdmissionError):
                edge.verify_image(Path("."), IMAGE_ID, policy())

    @mock.patch.object(edge, "verify_revision")
    @mock.patch.object(edge, "inspect_image")
    def test_only_exact_legacy_digest_is_accepted(self, inspect, verify_revision):
        inspect.return_value = {"Id": LEGACY_ID, "Config": {"Labels": {}}}
        self.assertEqual((LEGACY_ID, REVISION), edge.verify_image(Path("."), LEGACY_ID, policy()))
        inspect.return_value = {"Id": IMAGE_ID, "Config": {"Labels": {}}}
        with self.assertRaises(edge.AdmissionError):
            edge.verify_image(Path("."), IMAGE_ID, policy())

    @mock.patch.object(edge, "verify_image", return_value=(LEGACY_ID, REVISION))
    def test_rollback_replaces_only_bahia_image(self, _verify):
        compose = """services:\n  bahia:\n    image: sha256:old\n  relay:\n    image: sha256:relay\n  web:\n    image: sha256:web\n"""
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp) / "raw.yml"
            output = Path(tmp) / "safe.yml"
            source.write_text(compose, encoding="utf-8")
            edge.sanitize_rollback(Path("."), source, output, LEGACY_ID, policy())
            rendered = output.read_text(encoding="utf-8")
            self.assertIn(f"  bahia:\n    image: {LEGACY_ID}", rendered)
            self.assertIn("  relay:\n    image: sha256:relay", rendered)
            self.assertEqual(compose, source.read_text(encoding="utf-8"))

    def test_policy_rejects_invalid_legacy_mapping(self):
        broken = policy()
        broken["approved_legacy_images"] = {"sha256:bad": "dev"}
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "policy.json"
            path.write_text(json.dumps(broken), encoding="utf-8")
            with self.assertRaises(edge.AdmissionError):
                edge.load_policy(path)


if __name__ == "__main__":
    unittest.main()
