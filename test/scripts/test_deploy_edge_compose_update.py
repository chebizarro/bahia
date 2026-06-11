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


class DeployEdgeComposeUpdateTests(unittest.TestCase):
    def test_updates_images_and_docs_mount_preserving_unrelated_content(self):
        updated = deploy_edge_compose_update.update_compose_text(
            BASE_COMPOSE, VALID_TAG, VALID_RELEASE_DIR
        )

        self.assertIn(f"image: local/bahia-controlplane-bahia:{VALID_TAG}", updated)
        self.assertIn(f"image: local/bahia-controlplane-web:{VALID_TAG}", updated)
        self.assertIn(f"- {VALID_RELEASE_DIR}/docs:/docs:ro", updated)
        self.assertIn("image: postgres:16", updated)
        self.assertIn("POSTGRES_DB: bahia", updated)
        self.assertIn("name: bahia-controlplane", updated)
        self.assertTrue(updated.endswith("\n"))

    def test_missing_service_fails_without_writing(self):
        compose = BASE_COMPOSE.replace(
            "  relay:\n    image: local/bahia-controlplane-bahia:github-0000000\n    ports:\n      - \"3334:3334\"\n",
            "",
        )
        self.assert_helper_fails_without_writing(compose, "missing expected services: relay")

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
                ]
            )

            self.assertEqual(1, rc)
            self.assertEqual(compose, compose_path.read_text(encoding="utf-8"))
            with self.assertRaisesRegex(
                deploy_edge_compose_update.ComposeUpdateError, expected_message
            ):
                deploy_edge_compose_update.update_compose_text(compose, tag, release_dir)


if __name__ == "__main__":
    unittest.main()
