import copy
import importlib.util
import io
import json
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

SCRIPT_PATH = Path(__file__).resolve().parents[2] / "scripts" / "deploy_edge_compose_override.py"
SPEC = importlib.util.spec_from_file_location("deploy_edge_compose_override", SCRIPT_PATH)
module = importlib.util.module_from_spec(SPEC)
assert SPEC.loader is not None
SPEC.loader.exec_module(module)

TAG = "github-1a2b3c4"
RELEASE_DIR = f"/srv/data/bahia-controlplane/releases/{TAG}"
BACKEND_IMAGE = "sha256:" + "a" * 64
WEB_IMAGE = "sha256:" + "b" * 64
BASE = {
    "services": {
        "postgres": {"image": "postgres:16"},
        "bahia": {
            "image": "local/bahia-controlplane-bahia:github-0000000",
            "environment": {"BAHIA_CONFIG": "/config/config.yaml"},
            "volumes": [
                {"type": "bind", "source": "/srv/data/bahia-controlplane/releases/github-0000000/docs", "target": "/docs", "read_only": True},
                {"type": "bind", "source": "/srv/data/bahia-controlplane/config.yaml", "target": "/config/config.yaml", "read_only": True},
            ],
        },
        "relay": {"image": "registry.example/relay:stable"},
        "web": {"image": "local/bahia-controlplane-web:github-0000000", "ports": ["8081:80"]},
    }
}


class ComposeOverrideTests(unittest.TestCase):
    def generate(self, config=BASE, **kwargs):
        return module.generate_override(
            config, tag=kwargs.get("tag", TAG), release_dir=kwargs.get("release_dir", RELEASE_DIR),
            backend_image=kwargs.get("backend_image", BACKEND_IMAGE),
            web_image=kwargs.get("web_image", WEB_IMAGE), seed_only=kwargs.get("seed_only", False),
            rollback_backend_image=kwargs.get("rollback_backend_image"),
        )

    def test_digest_docs_and_missing_seed_only_override_target_services(self):
        result = self.generate()
        self.assertIn(f'    image: "{BACKEND_IMAGE}"', result)
        self.assertIn(f'    image: "{WEB_IMAGE}"', result)
        self.assertIn(f'        source: "{RELEASE_DIR}/docs"', result)
        self.assertIn('        target: /docs\n        read_only: true', result)
        self.assertIn('PUBLIC_BAHIA_BOOTSTRAP_RELAYS: "${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:', result)
        self.assertNotIn('postgres:', result)
        self.assertNotIn('relay:', result)
        self.assertNotIn('BAHIA_CONFIG', result)

    def test_existing_mapping_and_list_environment_values_are_not_rewritten(self):
        for environment in (
            {"PUBLIC_BAHIA_BOOTSTRAP_RELAYS": "wss://relay.example/relay?a=1&b=2|literal", "TZ": "UTC"},
            ["PUBLIC_BAHIA_BOOTSTRAP_RELAYS=wss://relay.example/a?x=1&y=2|literal", "TZ=UTC"],
        ):
            with self.subTest(environment=environment):
                config = copy.deepcopy(BASE)
                config["services"]["web"]["environment"] = environment
                before = copy.deepcopy(config)
                result = self.generate(config)
                self.assertEqual(before, config)
                self.assertNotIn("      PUBLIC_BAHIA_BOOTSTRAP_RELAYS:", result)
                self.assertIn("      PUBLIC_BAHIA_SERVICE_PUBKEYS:", result)
                self.assertNotIn("literal", result)

    def test_seed_only_handles_absent_and_complete_environment(self):
        result = self.generate(seed_only=True)
        self.assertNotIn("  bahia:", result)
        self.assertNotIn("    image:", result)
        config = copy.deepcopy(BASE)
        config["services"]["web"]["environment"] = dict.fromkeys(module.WEB_ENVIRONMENT_SEED, "existing")
        self.assertEqual("services: {}\n", self.generate(config, seed_only=True))

    def test_first_rollout_rollback_keeps_missing_bootstrap_env(self):
        result = self.generate(seed_only=True, rollback_backend_image=BACKEND_IMAGE)
        self.assertIn(f'  bahia:\n    image: "{BACKEND_IMAGE}"', result)
        self.assertIn('  web:\n    environment:', result)
        self.assertIn('PUBLIC_BAHIA_SERVICE_PUBKEYS', result)
        self.assertNotIn('volumes:', result)
        with self.assertRaisesRegex(module.ComposeOverrideError, "rollback backend image"):
            self.generate(seed_only=True, rollback_backend_image="latest")
        no_backend = copy.deepcopy(BASE)
        no_backend["services"].pop("bahia")
        with self.assertRaisesRegex(module.ComposeOverrideError, "missing expected services: bahia"):
            self.generate(no_backend, seed_only=True, rollback_backend_image=BACKEND_IMAGE)

    def test_first_rollout_snapshot_is_compatible_with_admission_sanitizer(self):
        admission_path = SCRIPT_PATH.with_name("edge_image_admission.py")
        spec = importlib.util.spec_from_file_location("edge_image_admission", admission_path)
        admission = importlib.util.module_from_spec(spec)
        assert spec.loader is not None
        spec.loader.exec_module(admission)
        snapshot = self.generate(seed_only=True, rollback_backend_image=BACKEND_IMAGE)
        safe_image = "sha256:" + "c" * 64
        sanitized = admission.replace_service_image(snapshot, "bahia", safe_image)
        self.assertIn(f"    image: {safe_image}", sanitized)
        self.assertIn("PUBLIC_BAHIA_BOOTSTRAP_RELAYS", sanitized)
        self.assertNotIn(BACKEND_IMAGE, sanitized)

    def test_missing_service_image_or_docs_mount_is_refused(self):
        cases = [
            (lambda c: c["services"].pop("bahia"), "missing expected services: bahia"),
            (lambda c: c["services"].pop("web"), "missing expected services: web"),
            (lambda c: c["services"]["bahia"].pop("image"), "missing image for service: bahia"),
            (lambda c: c["services"]["web"].pop("image"), "missing image for service: web"),
            (lambda c: c["services"]["bahia"].pop("volumes"), "missing release docs mount"),
            (lambda c: c["services"]["bahia"]["volumes"][0].update(read_only=False), "read-only release docs mount"),
            (lambda c: c["services"]["bahia"]["volumes"].append(copy.deepcopy(c["services"]["bahia"]["volumes"][0])), "exactly one release docs mount"),
        ]
        for mutation, message in cases:
            with self.subTest(message=message):
                config = copy.deepcopy(BASE)
                mutation(config)
                with self.assertRaisesRegex(module.ComposeOverrideError, message):
                    self.generate(config)

    def test_short_syntax_docs_mount_and_unsafe_inputs(self):
        config = copy.deepcopy(BASE)
        config["services"]["bahia"]["volumes"] = [
            "/srv/data/bahia-controlplane/releases/github-0000000/docs:/docs:ro"
        ]
        self.assertIn(f"{RELEASE_DIR}/docs", self.generate(config))
        for argument, value, message in (
            ("tag", "github-1a2b3c4;docker", "tag must match"),
            ("release_dir", RELEASE_DIR + "|evil", "release directory must be"),
            ("backend_image", BACKEND_IMAGE + "|evil", "backend image must be"),
            ("web_image", "latest", "web image must be"),
        ):
            with self.subTest(argument=argument):
                with self.assertRaisesRegex(module.ComposeOverrideError, message):
                    self.generate(**{argument: value})

    def test_cli_never_changes_base_and_atomically_replaces_override(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory) / "base.yml"
            override = Path(directory) / "override.yml"
            base.write_text("# unchanged base | & / :\n", encoding="utf-8")
            override.write_text("old override\n", encoding="utf-8")
            argv = ["--base-file", str(base), "--override-file", str(override),
                    "--tag", TAG, "--release-dir", RELEASE_DIR,
                    "--backend-image", BACKEND_IMAGE, "--web-image", WEB_IMAGE]
            with patch.object(sys, "stdin", io.StringIO(json.dumps(BASE))):
                self.assertEqual(0, module.main(argv))
            self.assertEqual("# unchanged base | & / :\n", base.read_text(encoding="utf-8"))
            self.assertIn(f'    image: "{BACKEND_IMAGE}"', override.read_text(encoding="utf-8"))
            self.assertEqual(["base.yml", "override.yml"], sorted(p.name for p in Path(directory).iterdir()))
            original = override.read_text(encoding="utf-8")
            with patch.object(sys, "stdin", io.StringIO(json.dumps(BASE))):
                self.assertEqual(1, module.main([*argv[:-1], "invalid-image"]))
            self.assertEqual(original, override.read_text(encoding="utf-8"))
            with patch.object(sys, "stdin", io.StringIO(json.dumps(BASE))):
                self.assertEqual(1, module.main(["--base-file", str(base), "--override-file", str(base), "--seed-only"]))
            self.assertEqual("# unchanged base | & / :\n", base.read_text(encoding="utf-8"))


if __name__ == "__main__":
    unittest.main()
