import difflib
import importlib.util
from pathlib import Path
import sys
import tempfile
import unittest

SCRIPT_PATH = Path(__file__).resolve().parents[2] / "scripts" / "deploy_edge_compose_update.py"
SPEC = importlib.util.spec_from_file_location("deploy_edge_compose_update", SCRIPT_PATH)
deploy_edge_compose_update = importlib.util.module_from_spec(SPEC)
sys.modules["deploy_edge_compose_update"] = deploy_edge_compose_update
assert SPEC.loader is not None
SPEC.loader.exec_module(deploy_edge_compose_update)

VALID_TAG = "github-1a2b3c4"
VALID_RELEASE_DIR = f"/srv/data/bahia-controlplane/releases/{VALID_TAG}"
BACKEND_IMAGE = "local/bahia-controlplane-bahia@sha256:" + "a" * 64
WEB_IMAGE = "local/bahia-controlplane-web@sha256:" + "b" * 64


BASE_COMPOSE = """version: "3.9"

services:
  postgres:
    image: postgres:16
    environment:
      POSTGRES_DB: bahia
  bahia:
    image: local/bahia-controlplane-bahia:github-0000000
    environment:
      BAHIA_CONFIG: /config/config.yaml
    volumes:
      - /srv/data/bahia-controlplane/releases/github-0000000/docs:/docs:ro
      - /srv/data/bahia-controlplane/config.yaml:/config/config.yaml:ro
  relay:
    image: local/bahia-controlplane-bahia:github-0000000
    ports:
      - "3334:3334"
  web:
    image: local/bahia-controlplane-web:github-0000000
    ports:
      - "8081:80"

networks:
  default:
    name: bahia-controlplane
"""


SEED_MAPPING_LINES = (
    "      PUBLIC_BAHIA_BOOTSTRAP_RELAYS: ${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:?PUBLIC_BAHIA_BOOTSTRAP_RELAYS must be set}\n"
    "      PUBLIC_BAHIA_SERVICE_PUBKEYS: ${PUBLIC_BAHIA_SERVICE_PUBKEYS:?PUBLIC_BAHIA_SERVICE_PUBKEYS must identify the trusted Bahia signer}\n"
    "      PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS: ${PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS:-}\n"
)
SEED_LIST_LINES = (
    "      - PUBLIC_BAHIA_BOOTSTRAP_RELAYS=${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:?PUBLIC_BAHIA_BOOTSTRAP_RELAYS must be set}\n"
    "      - PUBLIC_BAHIA_SERVICE_PUBKEYS=${PUBLIC_BAHIA_SERVICE_PUBKEYS:?PUBLIC_BAHIA_SERVICE_PUBKEYS must identify the trusted Bahia signer}\n"
    "      - PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS=${PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS:-}\n"
)
WEB_BLOCK = "  web:\n    image: local/bahia-controlplane-web:github-0000000\n    ports:\n      - \"8081:80\"\n"


def with_web_block(web_block):
    assert WEB_BLOCK in BASE_COMPOSE
    return BASE_COMPOSE.replace(WEB_BLOCK, web_block)


class DeployEdgeComposeUpdateTests(unittest.TestCase):
    def update(self, compose):
        return deploy_edge_compose_update.update_compose_text(
            compose, VALID_TAG, VALID_RELEASE_DIR, BACKEND_IMAGE, WEB_IMAGE
        )

    def test_updates_images_and_docs_mount_preserving_unrelated_content(self):
        updated = deploy_edge_compose_update.update_compose_text(
            BASE_COMPOSE, VALID_TAG, VALID_RELEASE_DIR, BACKEND_IMAGE, WEB_IMAGE
        )

        self.assertIn(f"image: {BACKEND_IMAGE}", updated)
        self.assertIn(f"image: {WEB_IMAGE}", updated)
        self.assertIn("  relay:\n    image: local/bahia-controlplane-bahia:github-0000000", updated)
        self.assertIn(f"- {VALID_RELEASE_DIR}/docs:/docs:ro", updated)
        self.assertIn("image: postgres:16", updated)
        self.assertIn("POSTGRES_DB: bahia", updated)
        self.assertIn("name: bahia-controlplane", updated)
        self.assertTrue(updated.endswith("\n"))

    def test_missing_bahia_service_fails_without_writing(self):
        compose = BASE_COMPOSE.replace(
            "  bahia:\n    image: local/bahia-controlplane-bahia:github-0000000\n    environment:\n      BAHIA_CONFIG: /config/config.yaml\n    volumes:\n      - /srv/data/bahia-controlplane/releases/github-0000000/docs:/docs:ro\n      - /srv/data/bahia-controlplane/config.yaml:/config/config.yaml:ro\n",
            "",
        )
        self.assert_helper_fails_without_writing(compose, "missing expected services: bahia")

    def test_relay_image_is_never_rewritten(self):
        dedicated_relay = "registry.example/relay@sha256:" + "c" * 64
        compose = BASE_COMPOSE.replace(
            "  relay:\n    image: local/bahia-controlplane-bahia:github-0000000",
            f"  relay:\n    image: {dedicated_relay}",
        )
        updated = deploy_edge_compose_update.update_compose_text(
            compose, VALID_TAG, VALID_RELEASE_DIR, BACKEND_IMAGE, WEB_IMAGE
        )
        self.assertIn(f"  relay:\n    image: {dedicated_relay}", updated)

    def test_missing_docs_mount_fails_without_writing(self):
        compose = BASE_COMPOSE.replace(
            "      - /srv/data/bahia-controlplane/releases/github-0000000/docs:/docs:ro\n",
            "",
        )
        self.assert_helper_fails_without_writing(compose, "missing release docs mount")

    def test_duplicate_image_lines_fail_without_writing(self):
        compose = BASE_COMPOSE.replace(
            "  web:\n    image: local/bahia-controlplane-web:github-0000000\n",
            "  web:\n    image: local/bahia-controlplane-web:github-0000000\n    image: local/bahia-controlplane-web:github-1111111\n",
        )
        self.assert_helper_fails_without_writing(compose, "duplicate image lines for services: web")

    def test_unsafe_tag_fails_without_writing(self):
        self.assert_helper_fails_without_writing(
            BASE_COMPOSE,
            "tag must match",
            tag="github-1a2b3c4;docker",
            release_dir="/srv/data/bahia-controlplane/releases/github-1a2b3c4;docker",
        )

    def test_web_environment_block_created_when_absent(self):
        updated = self.update(BASE_COMPOSE)
        expected_web = (
            f"  web:\n    image: {WEB_IMAGE}\n    ports:\n      - \"8081:80\"\n"
            "    environment:\n" + SEED_MAPPING_LINES
        )
        self.assertIn(expected_web + "\nnetworks:\n", updated)
        self.assertEqual(
            {"    environment:"} | set(SEED_MAPPING_LINES.splitlines()),
            self.added_lines(BASE_COMPOSE, updated) - self.image_and_mount_changes(),
        )

    def test_seed_keys_added_to_existing_mapping_environment(self):
        compose = with_web_block(
            "  web:\n    image: local/bahia-controlplane-web:github-0000000\n"
            "    environment:\n      TZ: UTC\n    ports:\n      - \"8081:80\"\n"
        )
        updated = self.update(compose)
        self.assertIn(
            "    environment:\n      TZ: UTC\n" + SEED_MAPPING_LINES + "    ports:\n      - \"8081:80\"\n",
            updated,
        )

    def test_seed_keys_added_to_list_style_environment(self):
        compose = with_web_block(
            "  web:\n    image: local/bahia-controlplane-web:github-0000000\n"
            "    environment:\n      - TZ=UTC\n      - \"PUBLIC_BAHIA_SERVICE_PUBKEYS=${PUBLIC_BAHIA_SERVICE_PUBKEYS}\"\n"
        )
        updated = self.update(compose)
        self.assertIn(
            "    environment:\n      - TZ=UTC\n      - \"PUBLIC_BAHIA_SERVICE_PUBKEYS=${PUBLIC_BAHIA_SERVICE_PUBKEYS}\"\n"
            "      - PUBLIC_BAHIA_BOOTSTRAP_RELAYS=${PUBLIC_BAHIA_BOOTSTRAP_RELAYS:?PUBLIC_BAHIA_BOOTSTRAP_RELAYS must be set}\n"
            "      - PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS=${PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS:-}\n"
            "\nnetworks:\n",
            updated,
        )
        self.assertNotIn("PUBLIC_BAHIA_SERVICE_PUBKEYS:", updated)

    def test_existing_seed_values_are_preserved(self):
        compose = with_web_block(
            "  web:\n    image: local/bahia-controlplane-web:github-0000000\n"
            "    environment:\n"
            "      PUBLIC_BAHIA_BOOTSTRAP_RELAYS: wss://edge.example/relay\n"
            "      PUBLIC_BAHIA_SERVICE_PUBKEYS: \"${PUBLIC_BAHIA_SERVICE_PUBKEYS:?required}\"  # pinned\n"
            "      PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS: ${PUBLIC_WHEELHOUSE_ALLOWED_PUBKEYS:-}\n"
        )
        updated = self.update(compose)
        self.assertEqual(self.image_and_mount_changes(), self.added_lines(compose, updated))
        self.assertIn("PUBLIC_BAHIA_BOOTSTRAP_RELAYS: wss://edge.example/relay", updated)

    def test_seed_injection_is_idempotent(self):
        for compose in (
            BASE_COMPOSE,
            with_web_block("  web:\n    image: local/bahia-controlplane-web:github-0000000\n    environment:\n      - TZ=UTC\n"),
        ):
            with self.subTest(compose=compose):
                once = self.update(compose)
                self.assertNotEqual(compose, once)
                self.assertEqual(once, self.update(once))
                seeded = deploy_edge_compose_update.seed_web_environment_text(compose)
                self.assertEqual(seeded, deploy_edge_compose_update.seed_web_environment_text(seeded))
                self.assertNotIn("sha256", seeded)

    def test_seed_injection_leaves_other_services_untouched(self):
        updated = self.update(BASE_COMPOSE)
        self.assertIn("  postgres:\n    image: postgres:16\n    environment:\n      POSTGRES_DB: bahia\n  bahia:\n", updated)
        self.assertIn(f"    environment:\n      BAHIA_CONFIG: /config/config.yaml\n    volumes:\n      - {VALID_RELEASE_DIR}/docs:/docs:ro\n", updated)
        self.assertEqual(1, updated.count("      PUBLIC_BAHIA_BOOTSTRAP_RELAYS: "))
        self.assertEqual(3, updated.count("environment:"))

    def test_seed_only_mode_refuses_missing_web_service(self):
        compose = with_web_block("")
        with tempfile.TemporaryDirectory() as tmpdir:
            compose_path = Path(tmpdir) / "docker-compose.yml"
            compose_path.write_text(compose, encoding="utf-8")
            rc = deploy_edge_compose_update.main(["--compose-file", str(compose_path), "--seed-web-env-only"])
            self.assertEqual(1, rc)
            self.assertEqual(compose, compose_path.read_text(encoding="utf-8"))
        with self.assertRaisesRegex(deploy_edge_compose_update.ComposeUpdateError, "missing expected services: web"):
            deploy_edge_compose_update.seed_web_environment_text(compose)
        self.assert_helper_fails_without_writing(compose, "missing expected services: web")

    def test_seed_only_mode_writes_only_when_changed(self):
        with tempfile.TemporaryDirectory() as tmpdir:
            compose_path = Path(tmpdir) / "docker-compose.yml"
            compose_path.write_text(BASE_COMPOSE, encoding="utf-8")
            self.assertEqual(0, deploy_edge_compose_update.main(["--compose-file", str(compose_path), "--seed-web-env-only"]))
            seeded = compose_path.read_text(encoding="utf-8")
            self.assertIn("    environment:\n" + SEED_MAPPING_LINES, seeded)
            self.assertIn("image: local/bahia-controlplane-web:github-0000000", seeded)
            before = compose_path.stat().st_mtime_ns
            self.assertEqual(0, deploy_edge_compose_update.main(["--compose-file", str(compose_path), "--seed-web-env-only"]))
            self.assertEqual(before, compose_path.stat().st_mtime_ns)
            self.assertEqual(seeded, compose_path.read_text(encoding="utf-8"))

    def test_inline_flow_environment_is_refused(self):
        compose = with_web_block(
            "  web:\n    image: local/bahia-controlplane-web:github-0000000\n    environment: {TZ: UTC}\n"
        )
        self.assert_helper_fails_without_writing(compose, "inline flow syntax")

    def added_lines(self, before, after):
        return {
            line[2:]
            for line in difflib.ndiff(before.splitlines(), after.splitlines())
            if line.startswith("+ ")
        }

    def image_and_mount_changes(self):
        return {
            f"    image: {BACKEND_IMAGE}",
            f"    image: {WEB_IMAGE}",
            f"      - {VALID_RELEASE_DIR}/docs:/docs:ro",
        }

    def assert_helper_fails_without_writing(
        self,
        compose,
        expected_message,
        tag=VALID_TAG,
        release_dir=VALID_RELEASE_DIR,
    ):
        with tempfile.TemporaryDirectory() as tmpdir:
            compose_path = Path(tmpdir) / "docker-compose.yml"
            compose_path.write_text(compose, encoding="utf-8")

            rc = deploy_edge_compose_update.main(
                [
                    "--compose-file",
                    str(compose_path),
                    "--tag",
                    tag,
                    "--release-dir",
                    release_dir,
                    "--backend-image",
                    BACKEND_IMAGE,
                    "--web-image",
                    WEB_IMAGE,
                ]
            )

            self.assertEqual(1, rc)
            self.assertEqual(compose, compose_path.read_text(encoding="utf-8"))
            with self.assertRaisesRegex(
                deploy_edge_compose_update.ComposeUpdateError, expected_message
            ):
                deploy_edge_compose_update.update_compose_text(
                    compose, tag, release_dir, BACKEND_IMAGE, WEB_IMAGE
                )


if __name__ == "__main__":
    unittest.main()
