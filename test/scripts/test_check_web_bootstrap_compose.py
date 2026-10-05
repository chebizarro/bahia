import importlib.util
from pathlib import Path
import sys
import unittest

SCRIPT_PATH = Path(__file__).resolve().parents[2] / "scripts" / "check_web_bootstrap_compose.py"
SPEC = importlib.util.spec_from_file_location("check_web_bootstrap_compose", SCRIPT_PATH)
validator = importlib.util.module_from_spec(SPEC)
sys.modules["check_web_bootstrap_compose"] = validator
assert SPEC.loader is not None
SPEC.loader.exec_module(validator)


class CheckWebBootstrapComposeTests(unittest.TestCase):
    def config(self, environment):
        return {"services": {"web": {"environment": environment}}}

    def test_accepts_plural_or_singular_pubkey(self):
        for key in validator.PUBKEY_KEYS:
            with self.subTest(key=key):
                validator.require_web_runtime_seed(self.config({
                    validator.RELAY_KEY: "wss://relay.example",
                    key: "a" * 64,
                }))

    def test_rejects_missing_or_blank_values(self):
        for environment, missing in [
            ({"PUBLIC_BAHIA_SERVICE_PUBKEYS": "a" * 64}, validator.RELAY_KEY),
            ({validator.RELAY_KEY: "wss://relay.example"}, "PUBLIC_BAHIA_SERVICE_PUBKEYS"),
            ({validator.RELAY_KEY: "  ", "PUBLIC_BAHIA_SERVICE_PUBKEYS": "a" * 64}, validator.RELAY_KEY),
            ({validator.RELAY_KEY: "wss://relay.example", "PUBLIC_BAHIA_SERVICE_PUBKEYS": " "}, "PUBLIC_BAHIA_SERVICE_PUBKEYS"),
            ({validator.RELAY_KEY: "wss://relay.example", "PUBLIC_BAHIA_SERVICE_PUBKEYS": None}, "PUBLIC_BAHIA_SERVICE_PUBKEYS"),
        ]:
            with self.subTest(environment=environment):
                with self.assertRaisesRegex(ValueError, missing + ".*docs/web-app-setup.md"):
                    validator.require_web_runtime_seed(self.config(environment))

    def test_accepts_rendered_list_form_and_rejects_missing_web_service(self):
        validator.require_web_runtime_seed(self.config([
            f"{validator.RELAY_KEY}=ws://localhost:3334/relay",
            "PUBLIC_BAHIA_SERVICE_PUBKEY=" + "b" * 64,
        ]))
        with self.assertRaisesRegex(ValueError, "no web service"):
            validator.require_web_runtime_seed({"services": {}})


if __name__ == "__main__":
    unittest.main()
