"""Pure guard tests for the live Signet interop runner; never start a daemon."""
import importlib.util
import json
from pathlib import Path
import subprocess
import sys
import unittest
from unittest import mock

SCRIPT_PATH = Path(__file__).resolve().parents[2] / "scripts" / "signet_live_interop.py"
SPEC = importlib.util.spec_from_file_location("signet_live_interop", SCRIPT_PATH)
runner = importlib.util.module_from_spec(SPEC)
sys.modules["signet_live_interop"] = runner
assert SPEC.loader is not None
SPEC.loader.exec_module(runner)

TEST = runner.SIGNER_TEST


def events(*actions, test=TEST):
    return "\n".join(json.dumps({"Action": action, "Test": test}) for action in actions)


class SignetLiveInteropRunnerTests(unittest.TestCase):
    def test_pubkey_secret_is_stdin_not_argv(self):
        fake = subprocess.CompletedProcess(["nak", "key", "public"], 0, "a" * 64 + "\n", "")
        with mock.patch.object(runner.subprocess, "run", return_value=fake) as call:
            self.assertEqual(runner.pubkey("synthetic-secret"), "a" * 64)
        self.assertEqual(call.call_args.args[0], ["nak", "key", "public"])
        self.assertEqual(call.call_args.kwargs["input"], "synthetic-secret\n")

    def test_exact_run_pass_required_per_named_test(self):
        runner.require_live_test_pass(events("run", "output", "pass"), TEST)
        runner.require_live_test_pass(events("run", "pass") + "\n" + events("run", "pass", test="Other"), TEST)
        for stream in ("", events("run"), events("skip"),
                       events("run", "skip"), events("run", "fail"),
                       events("pass"), events("run", "pass", "pass"),
                       events("run", "run", "pass"),
                       events("run", "pass", test="OtherTest"),
                       "not-json"):
            with self.subTest(stream=stream), self.assertRaises(RuntimeError):
                runner.require_live_test_pass(stream, TEST)

    def test_failure_does_not_echo_private_output(self):
        secret_uri = "bunker://" + "b" * 64 + "?relay=ws%3A%2F%2F127.0.0.1%3A7777&secret=private-pairing-secret"
        failed = subprocess.CompletedProcess(["go"], 1, f"{secret_uri}\n    interop_integration_test.go:123: boom\n", secret_uri)
        with mock.patch.object(runner.subprocess, "run", return_value=failed):
            with self.assertRaises(RuntimeError) as raised:
                runner.run_go_test(Path("/bahia"), {}, runner.ADAPTER_PACKAGE, TEST, "interop_integration_test.go")
        message = str(raised.exception)
        self.assertIn("interop_integration_test.go:123", message)
        self.assertNotIn("private-pairing-secret", message)
        self.assertNotIn(secret_uri, message)

    def test_go_test_targets_one_named_test_with_json(self):
        passed = subprocess.CompletedProcess(["go"], 0, events("run", "pass"), "")
        with mock.patch.object(runner.subprocess, "run", return_value=passed) as call:
            runner.run_go_test(Path("/bahia"), {}, runner.ADAPTER_PACKAGE, TEST, "interop_integration_test.go")
        argv = call.call_args.args[0]
        self.assertEqual(argv[:4], ["go", "test", "-tags", "signetinterop"])
        self.assertIn(f"^{TEST}$", argv)
        self.assertIn("-json", argv)
        self.assertIn("-count=1", argv)

    def test_commit_must_be_full_lowercase_sha(self):
        self.assertEqual(runner.require_commit("a" * 40, "x"), "a" * 40)
        for bad in ("", "a" * 39, "A" * 40, "g" * 40, "a" * 41, None):
            with self.subTest(bad=bad), self.assertRaises(RuntimeError):
                runner.require_commit(bad, "x")

    def test_clean_checkout_pins_commit(self):
        outputs = {"rev-parse": "c" * 40 + "\n", "status": ""}

        def fake_run(argv, **_):
            return outputs[argv[3]]

        with mock.patch.object(runner, "run", side_effect=fake_run):
            self.assertEqual(runner.require_clean_checkout(Path("/repo"), "Signet", "c" * 40), "c" * 40)
            with self.assertRaises(RuntimeError):
                runner.require_clean_checkout(Path("/repo"), "Signet", "d" * 40)
            outputs["status"] = " M file\n"
            with self.assertRaises(RuntimeError):
                runner.require_clean_checkout(Path("/repo"), "Signet", "c" * 40)

    def test_connect_secret_replacement_keeps_single_loopback_relay(self):
        bunker = "b" * 64
        relay = "ws://127.0.0.1:7777"
        first = f"bunker://{bunker}?relay=ws%3A%2F%2F127.0.0.1%3A7777&secret=first"
        second = runner.with_connect_secret(first, "second")
        self.assertNotIn("first", second)
        self.assertTrue(second.endswith("secret=second"))
        runner.require_loopback_bunker(first, bunker, relay)
        runner.require_loopback_bunker(second, bunker, relay)
        for bad in (f"bunker://{'c' * 64}?relay=ws%3A%2F%2F127.0.0.1%3A7777",
                    f"bunker://{bunker}?relay=wss%3A%2F%2Frelay.example",
                    f"bunker://{bunker}?relay=ws%3A%2F%2F127.0.0.1%3A7777&relay=wss%3A%2F%2Frelay.example",
                    f"nostrconnect://{bunker}?relay=ws%3A%2F%2F127.0.0.1%3A7777"):
            with self.subTest(bad=bad), self.assertRaises(RuntimeError):
                runner.require_loopback_bunker(bad, bunker, relay)

    def test_writer_acquire_has_no_ttl_and_policy_has_only_standard_methods(self):
        source = SCRIPT_PATH.read_text()
        self.assertNotIn("--ttl", source)
        self.assertNotIn("writer_renew", source)
        self.assertNotIn("sign_bahia_sbom_dsse", source)
        methods = {method.strip() for method in runner.ALLOWED_METHODS.split(",")}
        self.assertEqual(methods, {"connect", "get_public_key", "sign_event", "nip44_encrypt",
                                   "nip44_decrypt", "nip44_encrypt_b64"})

    def test_management_reply_parsing(self):
        reply = {"jsonrpc": "2.0", "result": {"result": {"agent_id": "bahia-interop"}}}
        with mock.patch.object(runner, "run", return_value="sent\nReply received:\n" + json.dumps(reply)):
            self.assertEqual(runner.management("ctl", "conf", {}, "writer-acquire", "bahia-interop", "a" * 64),
                             {"agent_id": "bahia-interop"})
        for output in ("no marker", "Reply received:\n" + json.dumps({"error": {"code": 1}})):
            with self.subTest(output=output), mock.patch.object(runner, "run", return_value=output):
                with self.assertRaises(RuntimeError):
                    runner.management("ctl", "conf", {}, "status")


if __name__ == "__main__":
    unittest.main()
