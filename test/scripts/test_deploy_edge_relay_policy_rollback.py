from pathlib import Path
import subprocess
import unittest


WORKFLOW = Path(__file__).resolve().parents[2] / ".github/workflows/deploy-edge.yml"


def rollback_function() -> str:
    workflow = WORKFLOW.read_text(encoding="utf-8")
    body = workflow.split("          rollback() {\n", 1)[1].split("          trap rollback EXIT", 1)[0]
    return "rollback() {\n" + body


class RelayPolicyRollbackTrapTests(unittest.TestCase):
    def run_trap(self, failure: str, original_status: int = 0) -> subprocess.CompletedProcess[str]:
        script = """set -euo pipefail
rollback_armed=1
rollback=/tmp/rollback.yml
COMPOSE_FILE=/tmp/compose.yml
ready_url=http://edge.example:8080/ready
EDGE_HEALTH_HOST=edge.example
baseline=/tmp/baseline.json
cp() { [[ "$FAIL" != copy ]]; }
docker() {
  [[ "$FAIL" != compose-config || "$4" != config ]] &&
  [[ "$FAIL" != compose-up || "$4" != up ]]
}
curl() {
  case "$*" in
    *:8080/ready*) [[ "$FAIL" != readiness ]];;
    *:3334/relay*) [[ "$FAIL" != relay ]];;
    *:8081/*) [[ "$FAIL" != web ]];;
    *) return 0;;
  esac
}
python3() { [[ "$FAIL" != policy-gate ]]; }
""" + rollback_function() + f"\ntrap rollback EXIT\nexit {original_status}\n"
        return subprocess.run(
            ["bash", "-c", script], env={"FAIL": failure},
            capture_output=True, text=True, check=False,
        )

    def test_each_failed_rollback_check_is_loud_and_nonzero(self):
        checks = {
            "copy": "Compose backup copy",
            "compose-config": "Compose config validation",
            "compose-up": "Compose restart",
            "readiness": "Bahia readiness",
            "policy-gate": "relay-policy invariant",
            "relay": "relay HTTP endpoint",
            "web": "web HTTP endpoint",
        }
        self.assertNotIn("|| true", rollback_function())
        for failure, message in checks.items():
            with self.subTest(failure=failure):
                result = self.run_trap(failure)
                self.assertNotEqual(result.returncode, 0, result.stderr)
                self.assertIn(message, result.stderr)

    def test_successful_rollback_preserves_original_rollout_failure(self):
        result = self.run_trap("none", original_status=23)
        self.assertEqual(result.returncode, 23, result.stderr)
        self.assertNotIn("Rollback verification failed", result.stderr)


if __name__ == "__main__":
    unittest.main()
